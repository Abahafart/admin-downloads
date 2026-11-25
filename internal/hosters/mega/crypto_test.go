package mega

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/rand/v2"
	"os"
	"path/filepath"
	"testing"
)

// Vectors in testdata/mega/vectors.json come from an independent Python
// implementation (testdata/mega/gen_vectors.py, using "cryptography").
type vectors struct {
	Files []struct {
		Size       int    `json:"size"`
		LinkKey    string `json:"link_key"`
		AESKeyHex  string `json:"aes_key_hex"`
		NonceHex   string `json:"nonce_hex"`
		MetaMACHex string `json:"meta_mac_hex"`
		CTSHA256   string `json:"ciphertext_sha256"`
		Attr       string `json:"attr"`
		Name       string `json:"name"`
	} `json:"files"`
	Login struct {
		Password     string `json:"password"`
		Email        string `json:"email"`
		V1PassKeyHex string `json:"v1_passkey_hex"`
		V1UH         string `json:"v1_uh"`
		V2Salt       string `json:"v2_salt"`
		V2PassKeyHex string `json:"v2_passkey_hex"`
		V2UH         string `json:"v2_uh"`
	} `json:"login"`
	Folder struct {
		Handle        string `json:"handle"`
		Key           string `json:"key"`
		SubHandle     string `json:"sub_handle"`
		FileHandle    string `json:"file_handle"`
		Ep01Key       string `json:"ep01_key"`
		BadAttrHandle string `json:"bad_attr_handle"`
		OrphanHandle  string `json:"orphan_handle"`
	} `json:"folder"`
}

var testdataDir = filepath.Join("..", "..", "..", "testdata", "mega")

func loadVectors(t *testing.T) vectors {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(testdataDir, "vectors.json"))
	if err != nil {
		t.Fatal(err)
	}
	var v vectors
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

// testPlaintext reproduces gen_vectors.py: SHA-256(be64(i)) blocks.
func testPlaintext(n int) []byte {
	out := make([]byte, 0, n+32)
	var ctr [8]byte
	for i := uint64(0); len(out) < n; i++ {
		binary.BigEndian.PutUint64(ctr[:], i)
		s := sha256.Sum256(ctr[:])
		out = append(out, s[:]...)
	}
	return out[:n]
}

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestUnpackKeyAndAttrs(t *testing.T) {
	v := loadVectors(t)
	for _, f := range v.Files {
		k, err := parseFileKey(f.LinkKey)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(k.AES[:], mustHex(t, f.AESKeyHex)) || !bytes.Equal(k.Nonce[:], mustHex(t, f.NonceHex)) || !bytes.Equal(k.MetaMAC[:], mustHex(t, f.MetaMACHex)) {
			t.Fatalf("size %d: key unpacked wrong", f.Size)
		}
		a, err := decryptAttrs(k.AES[:], f.Attr)
		if err != nil || a.Name != f.Name {
			t.Fatalf("attrs = %q, %v; want %q", a.Name, err, f.Name)
		}
		raw, _ := b64Decode(f.LinkKey)
		ak, err := attrKey(raw)
		if err != nil || !bytes.Equal(ak, k.AES[:]) {
			t.Fatal("attrKey for a file key must be the folded key")
		}
	}
}

func TestDecryptAttrsMalformed(t *testing.T) {
	key := make([]byte, 16)
	for _, in := range []string{"", "!!!", "AAAA", "AAAAAAAAAAAAAAAAAAAAAA", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"} {
		if _, err := decryptAttrs(key, in); err == nil {
			t.Errorf("decryptAttrs(%q) succeeded", in)
		}
	}
	if _, err := decryptAttrs([]byte{1, 2, 3}, "AAAAAAAAAAAAAAAAAAAAAA"); err == nil {
		t.Error("bad key length accepted")
	}
	for _, bad := range []string{"", "AAAA", "not base64 !"} {
		if _, err := parseFileKey(bad); err == nil {
			t.Errorf("parseFileKey(%q) succeeded", bad)
		}
	}
}

func TestCTRFullStreamMatchesReference(t *testing.T) {
	v := loadVectors(t)
	for _, f := range v.Files {
		k, _ := parseFileKey(f.LinkKey)
		pt := testPlaintext(f.Size)
		ct := make([]byte, len(pt))
		s, err := newCTRAt(k, 0)
		if err != nil {
			t.Fatal(err)
		}
		s.XORKeyStream(ct, pt)
		if got := sha256.Sum256(ct); hex.EncodeToString(got[:]) != f.CTSHA256 {
			t.Fatalf("size %d: ciphertext differs from reference", f.Size)
		}
	}
}

func TestCTRAtRandomOffsets(t *testing.T) {
	v := loadVectors(t)
	f := v.Files[len(v.Files)-1]
	k, _ := parseFileKey(f.LinkKey)
	pt := testPlaintext(f.Size)
	ct := make([]byte, len(pt))
	s, _ := newCTRAt(k, 0)
	s.XORKeyStream(ct, pt)

	rng := rand.New(rand.NewPCG(1, 2))
	offsets := []int{0, 1, 15, 16, 17, 131071, 131072, 131073, f.Size - 1}
	for i := 0; i < 50; i++ {
		offsets = append(offsets, rng.IntN(f.Size))
	}
	for _, off := range offsets {
		n := min(f.Size-off, 1+rng.IntN(70000))
		s, err := newCTRAt(k, int64(off))
		if err != nil {
			t.Fatal(err)
		}
		got := make([]byte, n)
		// Decrypt in uneven pieces, as a downloader would.
		for p := 0; p < n; {
			step := min(n-p, 1+rng.IntN(5000))
			s.XORKeyStream(got[p:p+step], ct[off+p:off+p+step])
			p += step
		}
		if !bytes.Equal(got, pt[off:off+n]) {
			t.Fatalf("offset %d: decrypted segment differs", off)
		}
	}
	if _, err := newCTRAt(k, -1); err == nil {
		t.Fatal("negative offset accepted")
	}
}

func TestVerifyMAC(t *testing.T) {
	v := loadVectors(t)
	for _, f := range v.Files {
		pt := testPlaintext(f.Size)
		if err := VerifyMAC(bytes.NewReader(pt), f.LinkKey); err != nil {
			t.Fatalf("size %d: %v", f.Size, err)
		}
		// Uneven reads must not change the result.
		if err := VerifyMAC(&oneByteThenRest{r: bytes.NewReader(pt)}, f.LinkKey); err != nil {
			t.Fatalf("size %d (uneven reads): %v", f.Size, err)
		}
		bad := bytes.Clone(pt)
		bad[len(bad)/2] ^= 1
		if err := VerifyMAC(bytes.NewReader(bad), f.LinkKey); !errors.Is(err, ErrMACMismatch) {
			t.Fatalf("size %d: corrupted content: err = %v", f.Size, err)
		}
		if err := VerifyMAC(bytes.NewReader(pt[:len(pt)-1]), f.LinkKey); !errors.Is(err, ErrMACMismatch) {
			t.Fatalf("size %d: truncated content: err = %v", f.Size, err)
		}
	}
	if err := VerifyMAC(bytes.NewReader(nil), v.Files[0].LinkKey); err != nil {
		t.Fatalf("empty file: %v", err)
	}
	if err := VerifyMAC(bytes.NewReader(nil), "bogus"); err == nil {
		t.Fatal("bad key accepted")
	}
}

type oneByteThenRest struct {
	r    *bytes.Reader
	done bool
}

func (o *oneByteThenRest) Read(p []byte) (int, error) {
	if !o.done && len(p) > 1 {
		o.done = true
		p = p[:1]
	} else if len(p) > 7777 {
		p = p[:7777]
	}
	return o.r.Read(p)
}

func TestChunkSizes(t *testing.T) {
	// Same table as go-mega's getChunkSizes test.
	const k = 1024
	want := []int64{128 * k, 256 * k, 384 * k, 512 * k, 640 * k, 768 * k, 896 * k, 1024 * k, 1024 * k, 1024 * k}
	for i, w := range want {
		if got := chunkSize(i); got != w {
			t.Errorf("chunk %d = %d, want %d", i, got, w)
		}
	}
}

func TestLoginKeyDerivation(t *testing.T) {
	v := loadVectors(t).Login
	pk, err := prepareKeyV1(v.Password)
	if err != nil {
		t.Fatal(err)
	}
	if hex.EncodeToString(pk) != v.V1PassKeyHex {
		t.Fatalf("prepare_key = %x, want %s", pk, v.V1PassKeyHex)
	}
	uh, err := stringHash(lower(v.Email), pk)
	if err != nil || uh != v.V1UH {
		t.Fatalf("stringhash = %s, %v; want %s", uh, err, v.V1UH)
	}
	salt, _ := b64Decode(v.V2Salt)
	pk2, uh2 := deriveV2(v.Password, salt)
	if hex.EncodeToString(pk2) != v.V2PassKeyHex || uh2 != v.V2UH {
		t.Fatalf("v2 = %x %s; want %s %s", pk2, uh2, v.V2PassKeyHex, v.V2UH)
	}
}

func lower(s string) string { return string(bytes.ToLower([]byte(s))) }

func TestReadMPIMalformed(t *testing.T) {
	for _, in := range [][]byte{nil, {0}, {0, 64, 1}, {0xff, 0xff}} {
		if _, _, err := readMPI(in); err == nil {
			t.Errorf("readMPI(%x) succeeded", in)
		}
	}
	if _, err := decryptSID(make([]byte, 16), "AAAA", "AAAA"); err == nil {
		t.Error("decryptSID accepted garbage")
	}
}

func TestHashcashSolve(t *testing.T) {
	tok := make([]byte, 48)
	for i := range tok {
		tok[i] = byte(i * 7)
	}
	token := b64Encode(tok)
	hdr, err := solveHashcashHeader(t.Context(), "1:255:1700000000:"+token, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !validHashcash(t, hdr, token, 255) {
		t.Fatalf("invalid solution %q", hdr)
	}
	if _, err := solveHashcashHeader(t.Context(), "2:1:x:y", 0); err == nil {
		t.Fatal("unsupported version accepted")
	}
}

// validHashcash checks "1:<token>:<cash>" like the server does.
func validHashcash(t *testing.T, hdr, token string, easiness int) bool {
	t.Helper()
	var ver, tok, cash string
	parts := bytes.Split([]byte(hdr), []byte(":"))
	if len(parts) != 3 {
		return false
	}
	ver, tok, cash = string(parts[0]), string(parts[1]), string(parts[2])
	if ver != "1" || tok != token {
		return false
	}
	prefix, err := b64Decode(cash)
	if err != nil || len(prefix) != 4 {
		return false
	}
	tb, _ := b64Decode(token)
	tb = padZero(tb, 16)
	buf := make([]byte, 4+hashcashReplications*hashcashSlot)
	copy(buf, prefix)
	for i := 0; i < hashcashReplications; i++ {
		copy(buf[4+i*hashcashSlot:], tb)
	}
	sum := sha256.Sum256(buf)
	return binary.BigEndian.Uint32(sum[:4]) <= hashcashThreshold(easiness)
}
