package postprocess

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/md5"  //nolint:gosec
	"crypto/sha1" //nolint:gosec
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/crc32"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"admindescargas/internal/config"
	"admindescargas/internal/hosters"
)

func TestChecksums(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file.bin")
	data := bytes.Repeat([]byte("checksum data "), 100000) // > 1 MiB for progress
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	s256 := sha256.Sum256(data)
	s1 := sha1.Sum(data) //nolint:gosec
	m5 := md5.Sum(data)  //nolint:gosec
	crc := crc32.ChecksumIEEE(data)

	tests := []struct {
		name    string
		algo    string
		value   string
		wantErr error
		wantOK  bool
	}{
		{"sha256", hosters.AlgoSHA256, hex.EncodeToString(s256[:]), nil, true},
		{"sha256 upper", "SHA256", strings.ToUpper(hex.EncodeToString(s256[:])), nil, true},
		{"sha1", hosters.AlgoSHA1, hex.EncodeToString(s1[:]), nil, true},
		{"md5", hosters.AlgoMD5, hex.EncodeToString(m5[:]), nil, true},
		{"crc32", hosters.AlgoCRC32, fmt.Sprintf("%08x", crc), nil, true},
		{"crc32 0x no padding", hosters.AlgoCRC32, fmt.Sprintf("0x%X", crc), nil, true},
		{"sha256 mismatch", hosters.AlgoSHA256, strings.Repeat("0", 64), ErrChecksumMismatch, false},
		{"md5 mismatch", hosters.AlgoMD5, strings.Repeat("a", 32), ErrChecksumMismatch, false},
		{"unknown algo skipped", "whirlpool", "abc", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := newTestProcessor(config.PostProcessConfig{VerifyChecksums: true})
			var calls int
			res, err := p.Run(context.Background(), Item{
				Path:     path,
				Checksum: &hosters.Checksum{Algo: tt.algo, Value: tt.value},
				Progress: func(pr Progress) {
					if pr.Stage != StageVerify || pr.Total != int64(len(data)) {
						t.Errorf("bad progress %+v", pr)
					}
					calls++
				},
			})
			if !errors.Is(err, tt.wantErr) || (tt.wantErr == nil && err != nil) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if res.Verified != tt.wantOK {
				t.Fatalf("Verified = %v, want %v", res.Verified, tt.wantOK)
			}
			if tt.wantOK && calls < 2 {
				t.Fatalf("progress called %d times", calls)
			}
		})
	}
}

func TestVerifyDisabledAndCustomVerifier(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.bin")
	os.WriteFile(path, []byte("x"), 0o644)
	bad := &hosters.Checksum{Algo: hosters.AlgoSHA256, Value: "00"}

	p := newTestProcessor(config.PostProcessConfig{VerifyChecksums: false})
	if res, err := p.Run(context.Background(), Item{Path: path, Checksum: bad}); err != nil || res.Verified {
		t.Fatalf("disabled verify: %v %+v", err, res)
	}
	// Item option overrides config.
	if err := p.Process(context.Background(), Item{Path: path, Checksum: bad, Options: Options{Verify: ptr(true)}}); !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("override verify: %v", err)
	}

	var gotPath, gotValue string
	p.RegisterVerifier("MEGA-MAC", func(_ context.Context, path, value string) error {
		gotPath, gotValue = path, value
		if value != "good" {
			return fmt.Errorf("%w: mac", ErrChecksumMismatch)
		}
		return nil
	})
	p2 := p
	p2.cfg.VerifyChecksums = true
	res, err := p2.Run(context.Background(), Item{Path: path, Checksum: &hosters.Checksum{Algo: hosters.AlgoMEGAMAC, Value: "good"}})
	if err != nil || !res.Verified || res.Algo != hosters.AlgoMEGAMAC || gotPath != path || gotValue != "good" {
		t.Fatalf("custom verifier: %v %+v %q %q", err, res, gotPath, gotValue)
	}
	if err := p2.Process(context.Background(), Item{Path: path, Checksum: &hosters.Checksum{Algo: hosters.AlgoMEGAMAC, Value: "bad"}}); !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("custom verifier mismatch: %v", err)
	}
	// Overriding a built-in.
	p2.RegisterVerifier(hosters.AlgoSHA256, func(context.Context, string, string) error { return nil })
	if err := p2.Process(context.Background(), Item{Path: path, Checksum: bad}); err != nil {
		t.Fatalf("overridden sha256: %v", err)
	}
}

func TestZipExtract(t *testing.T) {
	dir := t.TempDir()
	arc := filepath.Join(dir, "pack.zip")
	writeZip(t, arc, []zEntry{
		{name: "top.txt", data: []byte("top"), method: zip.Deflate},
		{name: "sub/", data: nil},
		{name: "sub/deep/file.txt", data: []byte("deep"), method: zip.Store},
		{name: `win\style.txt`, data: []byte("win"), method: zip.Deflate},
	})
	out := filepath.Join(dir, "out")
	var extractCalls int
	p := newTestProcessor(extractCfg())
	res, err := p.Run(context.Background(), Item{Path: arc, PackageDir: out, Progress: func(pr Progress) {
		if pr.Stage == StageExtract {
			extractCalls++
		}
	}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Archive != arc || res.Waiting || len(res.Extracted) != 3 || len(res.Rejected) != 0 {
		t.Fatalf("result %+v", res)
	}
	for rel, want := range map[string]string{"top.txt": "top", "sub/deep/file.txt": "deep", "win/style.txt": "win"} {
		if got := readFile(t, filepath.Join(out, filepath.FromSlash(rel))); got != want {
			t.Errorf("%s = %q", rel, got)
		}
	}
	if extractCalls == 0 {
		t.Error("no extract progress")
	}
	assertNoStaging(t, out)
	if _, err := os.Stat(arc); err != nil {
		t.Error("archive deleted without delete_archives")
	}
	// A second call for the same archive is a no-op.
	res, err = p.Run(context.Background(), Item{Path: arc, PackageDir: out})
	if err != nil || len(res.Extracted) != 0 {
		t.Fatalf("second run: %v %+v", err, res)
	}
}

func TestZipTraversalRejected(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "pkg")
	os.MkdirAll(dir, 0o755)
	arc := filepath.Join(dir, "evil.zip")
	evil := []string{"../evil1.txt", "/abs.txt", `C:\evil2.txt`, "a/../../evil3.txt", `..\evil4.txt`, `sub\..\..\evil5.txt`}
	var entries []zEntry
	for _, n := range evil {
		entries = append(entries, zEntry{name: n, data: []byte("pwned"), method: zip.Deflate})
	}
	entries = append(entries,
		zEntry{name: "link", data: []byte("/etc/passwd"), mode: fs.ModeSymlink},
		zEntry{name: "ok.txt", data: []byte("fine"), method: zip.Deflate})
	writeZip(t, arc, entries)

	res, err := newTestProcessor(extractCfg()).Run(context.Background(), Item{Path: arc})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rejected) != len(evil)+1 {
		t.Fatalf("rejected %v", res.Rejected)
	}
	if len(res.Extracted) != 1 || readFile(t, filepath.Join(dir, "ok.txt")) != "fine" {
		t.Fatalf("extracted %v", res.Extracted)
	}
	// Nothing outside the package dir, nothing else inside it.
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		switch filepath.Base(p) {
		case "evil.zip", "ok.txt":
			return nil
		}
		t.Errorf("unexpected file %s", p)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "link")); err == nil {
		t.Error("symlink entry was created")
	}
}

func TestIsUnsafeName(t *testing.T) {
	for name, want := range map[string]bool{
		"a/b.txt": false, "./a.txt": false, "a..b": false, "...": false,
		"../a": true, "a/../../b": true, "/etc/x": true, `\\server\share\x`: true,
		`C:\x`: true, "c:x": true, `a\..\..\b`: true, "a\x00b": true, ".. /x": true,
	} {
		if got := isUnsafeName(name); got != want {
			t.Errorf("isUnsafeName(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestZipPasswords(t *testing.T) {
	secret := []byte(strings.Repeat("datos secretos ", 50))
	variants := []struct {
		name  string
		entry zEntry
	}{
		{"zipcrypto deflate", zEntry{enc: "zipcrypto", method: zip.Deflate}},
		{"zipcrypto store descriptor", zEntry{enc: "zipcrypto", method: zip.Store, descriptor: true}},
		{"aes256 ae2 deflate", zEntry{enc: "aes", strength: 3, method: zip.Deflate}},
		{"aes128 ae1 store", zEntry{enc: "aes", strength: 1, method: zip.Store, ae1: true}},
		{"aes192 ae2 store", zEntry{enc: "aes", strength: 2, method: zip.Store}},
	}
	for _, v := range variants {
		t.Run(v.name, func(t *testing.T) {
			dir := t.TempDir()
			arc := filepath.Join(dir, "s.zip")
			e := v.entry
			e.name, e.data, e.pw = "secret.txt", secret, "c0rrecta"
			writeZip(t, arc, []zEntry{{name: "plain.txt", data: []byte("plain"), method: zip.Store}, e})

			// No password at all.
			_, err := newTestProcessor(extractCfg()).Run(context.Background(), Item{Path: arc})
			if !errors.Is(err, ErrPasswordRequired) {
				t.Fatalf("no password: %v", err)
			}
			assertNoStaging(t, dir)
			if _, err := os.Stat(filepath.Join(dir, "plain.txt")); err == nil {
				t.Fatal("partial extraction left plain.txt behind")
			}

			// Wrong passwords: the error says how many, never which.
			_, err = newTestProcessor(extractCfg("nope1")).Run(context.Background(), Item{Path: arc, Passwords: []string{"nope2", "nope1", ""}})
			if !errors.Is(err, ErrWrongPassword) || !strings.Contains(err.Error(), "tried 2 password(s)") || strings.Contains(err.Error(), "nope") {
				t.Fatalf("wrong password: %v", err)
			}
			assertNoStaging(t, dir)

			// Right password after a wrong one (config list).
			res, err := newTestProcessor(extractCfg("nope", "c0rrecta")).Run(context.Background(), Item{Path: arc})
			if err != nil {
				t.Fatalf("right password: %v", err)
			}
			if len(res.Extracted) != 2 || readFile(t, filepath.Join(dir, "secret.txt")) != string(secret) {
				t.Fatalf("extracted %v", res.Extracted)
			}
		})
	}
}

func TestZipAESTamperedMAC(t *testing.T) {
	dir := t.TempDir()
	arc := filepath.Join(dir, "t.zip")
	writeZip(t, arc, []zEntry{{name: "x.txt", data: []byte("hello"), enc: "aes", strength: 3, pw: "pw", method: zip.Store, tamperMAC: true}})
	_, err := newTestProcessor(extractCfg("pw")).Run(context.Background(), Item{Path: arc})
	if !errors.Is(err, ErrCorrupt) {
		t.Fatalf("err = %v", err)
	}
	assertNoStaging(t, dir)
}

func TestSplitZip(t *testing.T) {
	src := t.TempDir()
	full := filepath.Join(src, "full.zip")
	writeZip(t, full, []zEntry{
		{name: "a.txt", data: bytes.Repeat([]byte("A"), 3000), method: zip.Store},
		{name: "b/b.txt", data: []byte("bbb"), method: zip.Deflate},
	})
	b, _ := os.ReadFile(full)
	dir := t.TempDir()
	v1, v2 := filepath.Join(dir, "set.zip.001"), filepath.Join(dir, "set.zip.002")
	os.WriteFile(v1, b[:2000], 0o644)

	p := newTestProcessor(extractCfg())
	res, err := p.Run(context.Background(), Item{Path: v1})
	if err != nil || !res.Waiting {
		t.Fatalf("incomplete set: %v %+v", err, res)
	}
	os.WriteFile(v2, b[2000:], 0o644)
	res, err = p.Run(context.Background(), Item{Path: v2, Options: Options{DeleteArchives: ptr(true)}})
	if err != nil || res.Waiting || res.Archive != v1 || len(res.Extracted) != 2 {
		t.Fatalf("complete set: %v %+v", err, res)
	}
	if readFile(t, filepath.Join(dir, "b", "b.txt")) != "bbb" {
		t.Fatal("bad content")
	}
	sort.Strings(res.Deleted)
	if len(res.Deleted) != 2 || res.Deleted[0] != v1 || res.Deleted[1] != v2 {
		t.Fatalf("deleted %v", res.Deleted)
	}
	for _, v := range []string{v1, v2} {
		if _, err := os.Stat(v); err == nil {
			t.Errorf("%s not deleted", v)
		}
	}
	// A late call for a deleted volume of the extracted set is a no-op.
	if err := p.Process(context.Background(), Item{Path: v1}); err != nil {
		t.Fatalf("late call: %v", err)
	}
}

func TestSpannedZipUnsupported(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "x.z01"), []byte("PK\x07\x08junk"), 0o644)
	writeZip(t, filepath.Join(dir, "x.zip"), []zEntry{{name: "a", data: []byte("a")}})
	p := newTestProcessor(extractCfg())
	for _, n := range []string{"x.z01", "x.zip"} {
		if err := p.Process(context.Background(), Item{Path: filepath.Join(dir, n)}); !errors.Is(err, ErrUnsupported) {
			t.Errorf("%s: %v", n, err)
		}
	}
}

func TestNotArchives(t *testing.T) {
	dir := t.TempDir()
	p := newTestProcessor(extractCfg())
	// Plain file and a zip-based document: left alone.
	os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("hi"), 0o644)
	writeZip(t, filepath.Join(dir, "doc.docx"), []zEntry{{name: "word/document.xml", data: []byte("<x/>")}})
	for _, n := range []string{"notes.txt", "doc.docx"} {
		res, err := p.Run(context.Background(), Item{Path: filepath.Join(dir, n)})
		if err != nil || res.Archive != "" || len(res.Extracted) != 0 {
			t.Errorf("%s: %v %+v", n, err, res)
		}
	}
	// An HTML error page saved as .zip.
	os.WriteFile(filepath.Join(dir, "bad.zip"), []byte("<html>quota exceeded</html>"), 0o644)
	if err := p.Process(context.Background(), Item{Path: filepath.Join(dir, "bad.zip")}); !errors.Is(err, ErrCorrupt) {
		t.Errorf("bad.zip: %v", err)
	}
	// Extraction disabled.
	writeZip(t, filepath.Join(dir, "real.zip"), []zEntry{{name: "r.txt", data: []byte("r")}})
	res, err := newTestProcessor(config.PostProcessConfig{}).Run(context.Background(), Item{Path: filepath.Join(dir, "real.zip")})
	if err != nil || len(res.Extracted) != 0 {
		t.Fatalf("disabled: %v %+v", err, res)
	}
	// A 7z misnamed as .zip is extracted as 7z.
	b, _ := os.ReadFile(filepath.Join("..", "..", "testdata", "postprocess", "copy.7z"))
	os.WriteFile(filepath.Join(dir, "misnamed.zip"), b, 0o644)
	res, err = p.Run(context.Background(), Item{Path: filepath.Join(dir, "misnamed.zip"), PackageDir: filepath.Join(dir, "m")})
	if err != nil || len(res.Extracted) != 2 {
		t.Fatalf("misnamed: %v %+v", err, res)
	}
}

func TestCancellation(t *testing.T) {
	dir := t.TempDir()
	arc := filepath.Join(dir, "big.zip")
	big := bytes.Repeat([]byte("0123456789"), 600000) // 6 MB
	writeZip(t, arc, []zEntry{{name: "big.bin", data: big, method: zip.Store}, {name: "after.txt", data: []byte("x")}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, err := newTestProcessor(extractCfg()).Run(ctx, Item{Path: arc, Progress: func(p Progress) {
		if p.Stage == StageExtract && p.Done > 0 {
			cancel()
		}
	}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	assertNoStaging(t, dir)
	for _, n := range []string{"big.bin", "after.txt"} {
		if _, err := os.Stat(filepath.Join(dir, n)); err == nil {
			t.Errorf("%s written despite cancellation", n)
		}
	}
	// Already canceled before starting.
	if err := newTestProcessor(extractCfg()).Process(ctx, Item{Path: arc}); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-canceled: %v", err)
	}
}

func TestSubdirAndConflicts(t *testing.T) {
	dir := t.TempDir()
	arc := filepath.Join(dir, "Mi Serie.zip")
	writeZip(t, arc, []zEntry{{name: "ep01.txt", data: []byte("new")}})

	res, err := newTestProcessor(extractCfg()).Run(context.Background(), Item{Path: arc, Options: Options{ExtractSubdir: true}})
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, "Mi Serie", "ep01.txt")
	if len(res.Extracted) != 1 || res.Extracted[0] != want {
		t.Fatalf("extracted %v", res.Extracted)
	}

	// Existing file, no overwrite: unique name.
	out := filepath.Join(dir, "out")
	os.MkdirAll(out, 0o755)
	os.WriteFile(filepath.Join(out, "ep01.txt"), []byte("old"), 0o644)
	res, err = newTestProcessor(extractCfg()).Run(context.Background(), Item{Path: arc, PackageDir: out})
	if err != nil {
		t.Fatal(err)
	}
	if readFile(t, filepath.Join(out, "ep01.txt")) != "old" || readFile(t, filepath.Join(out, "ep01 (1).txt")) != "new" {
		t.Fatalf("conflict handling: %v", res.Extracted)
	}
	// Overwrite.
	_, err = newTestProcessor(extractCfg()).Run(context.Background(), Item{Path: arc, PackageDir: out, Options: Options{Overwrite: true}})
	if err != nil || readFile(t, filepath.Join(out, "ep01.txt")) != "new" {
		t.Fatalf("overwrite: %v", err)
	}
}

func TestConcurrentSameSet(t *testing.T) {
	dir := t.TempDir()
	arc := filepath.Join(dir, "c.zip")
	writeZip(t, arc, []zEntry{{name: "one.txt", data: []byte("1")}})
	p := newTestProcessor(extractCfg())
	var wg sync.WaitGroup
	var files atomic.Int64
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := p.Run(context.Background(), Item{Path: arc})
			if err != nil {
				t.Error(err)
				return
			}
			files.Add(int64(len(res.Extracted)))
		}()
	}
	wg.Wait()
	if files.Load() != 1 {
		t.Fatalf("extracted %d times", files.Load())
	}
}

func TestSevenZipFixtures(t *testing.T) {
	t.Run("copy", func(t *testing.T) {
		dir := copyFixtures(t, "copy.7z")
		res, err := newTestProcessor(extractCfg()).Run(context.Background(), Item{Path: filepath.Join(dir, "copy.7z")})
		if err != nil || len(res.Extracted) != 2 {
			t.Fatalf("%v %+v", err, res)
		}
		if readFile(t, filepath.Join(dir, "dir", "nested.txt")) != "archivo anidado\n" {
			t.Fatal("bad content")
		}
	})
	t.Run("aes", func(t *testing.T) {
		dir := copyFixtures(t, "aes.7z")
		arc := filepath.Join(dir, "aes.7z")
		if err := newTestProcessor(extractCfg()).Process(context.Background(), Item{Path: arc}); !errors.Is(err, ErrPasswordRequired) {
			t.Fatalf("no password: %v", err)
		}
		if err := newTestProcessor(extractCfg("bad")).Process(context.Background(), Item{Path: arc}); !errors.Is(err, ErrWrongPassword) {
			t.Fatalf("wrong password: %v", err)
		}
		assertNoStaging(t, dir)
		res, err := newTestProcessor(extractCfg("bad", "secreto7z")).Run(context.Background(), Item{Path: arc})
		if err != nil || len(res.Extracted) != 1 || readFile(t, filepath.Join(dir, "secret.txt")) != "contenido cifrado con 7zAES\n" {
			t.Fatalf("right password: %v %+v", err, res)
		}
	})
	t.Run("traversal", func(t *testing.T) {
		root := copyFixtures(t, "traversal.7z")
		out := filepath.Join(root, "out")
		res, err := newTestProcessor(extractCfg()).Run(context.Background(), Item{Path: filepath.Join(root, "traversal.7z"), PackageDir: out})
		if err != nil || len(res.Rejected) != 1 || len(res.Extracted) != 1 {
			t.Fatalf("%v %+v", err, res)
		}
		if _, err := os.Stat(filepath.Join(root, "evil.txt")); err == nil {
			t.Fatal("traversal entry written")
		}
	})
	t.Run("split", func(t *testing.T) {
		dir := copyFixtures(t, "split.7z.001")
		p := newTestProcessor(extractCfg())
		res, err := p.Run(context.Background(), Item{Path: filepath.Join(dir, "split.7z.001")})
		if err != nil || !res.Waiting {
			t.Fatalf("incomplete: %v %+v", err, res)
		}
		b, _ := os.ReadFile(filepath.Join("..", "..", "testdata", "postprocess", "split.7z.002"))
		os.WriteFile(filepath.Join(dir, "split.7z.002"), b, 0o644)
		res, err = p.Run(context.Background(), Item{Path: filepath.Join(dir, "split.7z.002")})
		if err != nil || len(res.Extracted) != 1 {
			t.Fatalf("complete: %v %+v", err, res)
		}
		if got := readFile(t, filepath.Join(dir, "split.txt")); got != strings.Repeat("0123456789abcdef", 40) {
			t.Fatalf("content %q", got)
		}
	})
}

func TestRarFixtures(t *testing.T) {
	t.Run("stored", func(t *testing.T) {
		dir := copyFixtures(t, "stored.rar")
		res, err := newTestProcessor(extractCfg()).Run(context.Background(), Item{Path: filepath.Join(dir, "stored.rar")})
		if err != nil || len(res.Extracted) != 2 {
			t.Fatalf("%v %+v", err, res)
		}
		if readFile(t, filepath.Join(dir, "hello.txt")) != "Hola desde AdminDescargas\n" {
			t.Fatal("bad content")
		}
	})
	t.Run("encrypted", func(t *testing.T) {
		dir := copyFixtures(t, "enc.rar")
		arc := filepath.Join(dir, "enc.rar")
		if err := newTestProcessor(extractCfg()).Process(context.Background(), Item{Path: arc}); !errors.Is(err, ErrPasswordRequired) {
			t.Fatalf("no password: %v", err)
		}
		err := newTestProcessor(extractCfg("x", "y")).Process(context.Background(), Item{Path: arc})
		if !errors.Is(err, ErrWrongPassword) || !strings.Contains(err.Error(), "tried 2") {
			t.Fatalf("wrong password: %v", err)
		}
		res, err := newTestProcessor(extractCfg("x")).Run(context.Background(), Item{Path: arc, Passwords: []string{"secretorar"}})
		if err != nil || len(res.Extracted) != 1 || readFile(t, filepath.Join(dir, "secret.txt")) != "contenido cifrado con RAR5\n" {
			t.Fatalf("right password: %v %+v", err, res)
		}
	})
	t.Run("traversal", func(t *testing.T) {
		root := copyFixtures(t, "traversal.rar")
		out := filepath.Join(root, "out")
		res, err := newTestProcessor(extractCfg()).Run(context.Background(), Item{Path: filepath.Join(root, "traversal.rar"), PackageDir: out})
		if err != nil || len(res.Rejected) != 1 || res.Rejected[0] != "../evil.txt" || len(res.Extracted) != 1 {
			t.Fatalf("%v %+v", err, res)
		}
		if _, err := os.Stat(filepath.Join(root, "evil.txt")); err == nil {
			t.Fatal("traversal entry written")
		}
	})
	t.Run("multi new naming", func(t *testing.T) {
		dir := copyFixtures(t, "multi.part1.rar")
		p := newTestProcessor(extractCfg())
		res, err := p.Run(context.Background(), Item{Path: filepath.Join(dir, "multi.part1.rar")})
		if err != nil || !res.Waiting {
			t.Fatalf("incomplete: %v %+v", err, res)
		}
		b, _ := os.ReadFile(filepath.Join("..", "..", "testdata", "postprocess", "multi.part2.rar"))
		os.WriteFile(filepath.Join(dir, "multi.part2.rar"), b, 0o644)
		res, err = p.Run(context.Background(), Item{Path: filepath.Join(dir, "multi.part2.rar"), Options: Options{DeleteArchives: ptr(true)}})
		if err != nil || len(res.Extracted) != 2 || len(res.Deleted) != 2 {
			t.Fatalf("complete: %v %+v", err, res)
		}
		if got := readFile(t, filepath.Join(dir, "big.txt")); got != strings.Repeat("RAR5 multi-volume line\n", 30) {
			t.Fatalf("content %q", got)
		}
	})
	t.Run("multi old naming", func(t *testing.T) {
		dir := copyFixtures(t, "old.rar")
		p := newTestProcessor(extractCfg())
		res, err := p.Run(context.Background(), Item{Path: filepath.Join(dir, "old.rar")})
		if err != nil || !res.Waiting {
			t.Fatalf("incomplete: %v %+v", err, res)
		}
		b, _ := os.ReadFile(filepath.Join("..", "..", "testdata", "postprocess", "old.r00"))
		os.WriteFile(filepath.Join(dir, "old.r00"), b, 0o644)
		res, err = p.Run(context.Background(), Item{Path: filepath.Join(dir, "old.r00")})
		if err != nil || res.Archive != filepath.Join(dir, "old.rar") || len(res.Extracted) != 1 {
			t.Fatalf("complete: %v %+v", err, res)
		}
		if got := readFile(t, filepath.Join(dir, "old.txt")); got != strings.Repeat("RAR4 old naming\n", 20) {
			t.Fatalf("content %q", got)
		}
	})
}

func TestDetectSetNames(t *testing.T) {
	dir := t.TempDir()
	touch := func(names ...string) {
		for _, n := range names {
			os.WriteFile(filepath.Join(dir, n), []byte("Rar!\x1a\x07\x01\x00"), 0o644)
		}
	}
	touch("a.part01.rar", "a.part02.rar", "a.part03.rar", "b.rar", "b.r00", "b.r01")
	for in, wantVols := range map[string]int{"a.part02.rar": 3, "a.part03.rar": 3, "b.r01": 3, "b.rar": 3} {
		set, err := detectSet(filepath.Join(dir, in))
		if err != nil || set == nil {
			t.Fatalf("%s: %v", in, err)
		}
		if len(set.volumes) != wantVols || set.volumes[0] != set.first {
			t.Errorf("%s: volumes %v first %s", in, set.volumes, set.first)
		}
	}
	if set, _ := detectSet(filepath.Join(dir, "c.part2.rar")); set == nil || len(set.volumes) != 0 || filepath.Base(set.first) != "c.part1.rar" {
		t.Errorf("missing first volume: %+v", set)
	}
	if got := subdirName("/x/Serie.S01.part001.rar"); got != "Serie.S01" {
		t.Errorf("subdirName = %q", got)
	}
	if got := subdirName("/x/pack.7z.001"); got != "pack" {
		t.Errorf("subdirName = %q", got)
	}
}
