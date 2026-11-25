package fsutil

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSanitizeName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"normal.mkv", "normal.mkv"},
		{"Temporada 1", "Temporada 1"},
		{"", "_"},
		{".", "_"},
		{"..", "_"},
		{"...", "_"},
		{"   ", "_"},
		{"a/b", "a_b"},
		{`a\b`, "a_b"},
		{"a:b", "a_b"},
		{`<>:"/\|?*`, "_________"},
		{"tab\there", "tab_here"},
		{"nul\x00byte", "nul_byte"},
		{"del\x7f", "del_"},
		{"trailing. . ", "trailing"},
		{"trailing...", "trailing"},
		{"  leading", "leading"},
		{".hidden", ".hidden"},
		{"CON", "_CON"},
		{"con", "_con"},
		{"Con.txt", "_Con.txt"},
		{"con.tar.gz", "_con.tar.gz"},
		{"CON .txt", "_CON .txt"},
		{"PRN", "_PRN"},
		{"AUX", "_AUX"},
		{"NUL", "_NUL"},
		{"COM1", "_COM1"},
		{"com9.log", "_com9.log"},
		{"LPT1", "_LPT1"},
		{"lpt9", "_lpt9"},
		{"COM¹", "_COM¹"},
		{"COM0", "COM0"},
		{"COM10", "COM10"},
		{"CONSOLE", "CONSOLE"},
		{"xCON", "xCON"},
		{"película", "película"},
		{"pelı\u0301cula", "pelı\u0301cula"},
		{"e\u0301", "\u00e9"}, // NFC normalization
		{"bad\xffutf8", "bad_utf8"},
	}
	for _, c := range cases {
		if got := SanitizeName(c.in); got != c.want {
			t.Errorf("SanitizeName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSanitizeNameLength(t *testing.T) {
	long := strings.Repeat("ñ", 300) + ".mkv" // 2 bytes per rune
	got := SanitizeName(long)
	if len(got) > MaxComponentBytes {
		t.Fatalf("len = %d > %d", len(got), MaxComponentBytes)
	}
	if !utf8.ValidString(got) {
		t.Fatalf("invalid utf8 after truncation: %q", got)
	}
	if !strings.HasSuffix(got, ".mkv") {
		t.Fatalf("extension lost: %q", got)
	}

	noExt := strings.Repeat("a", 400)
	if got := SanitizeName(noExt); len(got) != MaxComponentBytes {
		t.Fatalf("len = %d, want %d", len(got), MaxComponentBytes)
	}

	// A truncation that leaves a reserved stem must still be neutralized.
	tricky := "CON" + strings.Repeat(".", 300) + ".txt"
	if got := SanitizeName(tricky); isReserved(got) || len(got) > MaxComponentBytes {
		t.Fatalf("reserved after truncation: %q", got)
	}
}

func TestSanitizeNameIdempotent(t *testing.T) {
	inputs := []string{"CON", "a:b", "..", "x. ", strings.Repeat("z", 500), "ok.txt", "COM¹"}
	for _, in := range inputs {
		once := SanitizeName(in)
		if twice := SanitizeName(once); twice != once {
			t.Errorf("not idempotent for %q: %q -> %q", in, once, twice)
		}
	}
}

func TestSanitizeRelPath(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Temporada 1/ep01.mkv", "Temporada 1/ep01.mkv"},
		{`Temporada 1\ep01.mkv`, "Temporada 1/ep01.mkv"},
		{"../../etc/passwd", "etc/passwd"},
		{"/abs/path", "abs/path"},
		{`C:\Windows\system32`, "C_/Windows/system32"},
		{"a/./b//c/", "a/b/c"},
		{"a/.../b", "a/b"},
		{"CON/aux.txt", "_CON/_aux.txt"},
		{"", ""},
		{"../..", ""},
	}
	for _, c := range cases {
		if got := SanitizeRelPath(c.in); got != c.want {
			t.Errorf("SanitizeRelPath(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSafeJoin(t *testing.T) {
	base := t.TempDir()
	cases := []struct {
		rel  string
		want string // relative to base, slash separated
	}{
		{"file.bin", "file.bin"},
		{"Temporada 1/ep01.mkv", "Temporada 1/ep01.mkv"},
		{"../escape.txt", "escape.txt"},
		{"../../../../etc/passwd", "etc/passwd"},
		{`..\..\windows\win.ini`, "windows/win.ini"},
		{"/etc/shadow", "etc/shadow"},
		{`\\server\share\x`, "server/share/x"},
		{`C:\boot.ini`, "C_/boot.ini"},
		{"a/../../b", "a/b"},
		{"sub/CON", "sub/_CON"},
		{"sub/a:b", "sub/a_b"},
		{"dots.../x. ", "dots/x"},
	}
	for _, c := range cases {
		got, err := SafeJoin(base, c.rel)
		if err != nil {
			t.Errorf("SafeJoin(%q): %v", c.rel, err)
			continue
		}
		want := filepath.Join(base, filepath.FromSlash(c.want))
		if got != want {
			t.Errorf("SafeJoin(%q) = %q, want %q", c.rel, got, want)
		}
		rel, err := filepath.Rel(base, got)
		if err != nil || escapes(rel) {
			t.Errorf("SafeJoin(%q) = %q escapes base", c.rel, got)
		}
	}

	for _, bad := range []string{"", ".", "..", "../..", "/", `\`, "./.", "..."} {
		if _, err := SafeJoin(base, bad); !errors.Is(err, ErrEmptyPath) {
			t.Errorf("SafeJoin(%q) err = %v, want ErrEmptyPath", bad, err)
		}
	}
}

func TestSafeJoinRelativeBase(t *testing.T) {
	got, err := SafeJoin("downloads", "../x")
	if err != nil {
		t.Fatal(err)
	}
	abs, _ := filepath.Abs("downloads")
	if got != filepath.Join(abs, "x") {
		t.Fatalf("got %q", got)
	}
}

func TestUniquePath(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "video.mkv")
	if got := UniquePath(p); got != p {
		t.Fatalf("free path changed: %q", got)
	}
	if err := os.WriteFile(p, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	want1 := filepath.Join(dir, "video (1).mkv")
	if got := UniquePath(p); got != want1 {
		t.Fatalf("got %q, want %q", got, want1)
	}
	if err := os.WriteFile(want1, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := UniquePath(p); got != filepath.Join(dir, "video (2).mkv") {
		t.Fatalf("got %q", got)
	}
}

func FuzzSafeJoin(f *testing.F) {
	for _, s := range []string{"a/b", "../x", `C:\x`, "CON", "\x00", "a/../../.."} {
		f.Add(s)
	}
	base := f.TempDir()
	f.Fuzz(func(t *testing.T, rel string) {
		got, err := SafeJoin(base, rel)
		if err != nil {
			if errors.Is(err, ErrEmptyPath) {
				return
			}
			t.Fatalf("unexpected error: %v", err)
		}
		r, err := filepath.Rel(base, got)
		if err != nil || r == "." || escapes(r) {
			t.Fatalf("escape: %q -> %q", rel, got)
		}
		for _, part := range strings.Split(r, string(filepath.Separator)) {
			if SanitizeName(part) != part {
				t.Fatalf("unsanitized component %q in %q", part, got)
			}
		}
	})
}

func escapes(rel string) bool {
	return rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel)
}
