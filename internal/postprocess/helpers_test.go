package postprocess

import (
	"archive/zip"
	"bytes"
	"compress/flate"
	"crypto/hmac"
	"crypto/sha1" //nolint:gosec
	"encoding/binary"
	"hash/crc32"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"admindescargas/internal/config"
)

type zEntry struct {
	name   string
	data   []byte
	mode   fs.FileMode // e.g. fs.ModeSymlink
	method uint16      // zip.Store or zip.Deflate
	// enc: "" | "zipcrypto" | "aes"
	enc        string
	pw         string
	strength   byte // AES: 1, 2, 3
	ae1        bool // AES: AE-1 (real CRC) instead of AE-2
	descriptor bool // ZipCrypto: data descriptor flag (check byte from time)
	tamperMAC  bool
}

func compress(t *testing.T, method uint16, data []byte) []byte {
	t.Helper()
	if method == zip.Store {
		return data
	}
	var buf bytes.Buffer
	w, err := flate.NewWriter(&buf, flate.DefaultCompression)
	if err != nil {
		t.Fatal(err)
	}
	w.Write(data)
	w.Close()
	return buf.Bytes()
}

// writeZip creates a zip at path, encrypting entries as requested.
func writeZip(t *testing.T, path string, entries []zEntry) {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		switch e.enc {
		case "":
			fh := &zip.FileHeader{Name: e.name, Method: e.method}
			if e.mode != 0 {
				fh.SetMode(e.mode | 0o777)
			}
			w, err := zw.CreateHeader(fh)
			if err != nil {
				t.Fatal(err)
			}
			w.Write(e.data)
		case "zipcrypto":
			comp := compress(t, e.method, e.data)
			crc := crc32.ChecksumIEEE(e.data)
			fh := &zip.FileHeader{Name: e.name, Method: e.method, Flags: zipFlagEncrypted, CRC32: crc,
				UncompressedSize64: uint64(len(e.data)), ModifiedTime: 0x6e5a, ModifiedDate: 0x5c21} //nolint:staticcheck // SA1019: raw DOS time fields are written on purpose
			hdr := []byte("adesc-head\x00\x00")
			hdr[11] = byte(crc >> 24)
			if e.descriptor {
				fh.Flags |= zipFlagDescriptor
				hdr[11] = byte(fh.ModifiedTime >> 8) //nolint:staticcheck // SA1019: raw DOS time fields are written on purpose
			}
			payload := append(append([]byte(nil), hdr...), comp...)
			newZipCryptoKeys(e.pw).encrypt(payload)
			fh.CompressedSize64 = uint64(len(payload))
			w, err := zw.CreateRaw(fh)
			if err != nil {
				t.Fatal(err)
			}
			w.Write(payload)
		case "aes":
			comp := compress(t, e.method, e.data)
			keyLen := aesKeyLen(e.strength)
			salt := bytes.Repeat([]byte{0x5a}, keyLen/2)
			aesKey, macKey, verifier, err := winzipKeys(e.pw, salt, keyLen)
			if err != nil {
				t.Fatal(err)
			}
			ctr, err := newWinzipCTR(aesKey)
			if err != nil {
				t.Fatal(err)
			}
			ct := make([]byte, len(comp))
			ctr.XORKeyStream(ct, comp)
			mac := hmac.New(sha1.New, macKey)
			mac.Write(ct)
			code := mac.Sum(nil)[:10]
			if e.tamperMAC {
				code[0] ^= 0xff
			}
			payload := bytes.Join([][]byte{salt, verifier, ct, code}, nil)
			version := uint16(2)
			var crc uint32
			if e.ae1 {
				version, crc = 1, crc32.ChecksumIEEE(e.data)
			}
			extra := binary.LittleEndian.AppendUint16(nil, zipExtraAES)
			extra = binary.LittleEndian.AppendUint16(extra, 7)
			extra = binary.LittleEndian.AppendUint16(extra, version)
			extra = append(extra, 'A', 'E', e.strength)
			extra = binary.LittleEndian.AppendUint16(extra, e.method)
			fh := &zip.FileHeader{Name: e.name, Method: zipMethodAES, Flags: zipFlagEncrypted, CRC32: crc,
				UncompressedSize64: uint64(len(e.data)), CompressedSize64: uint64(len(payload)), Extra: extra}
			w, err := zw.CreateRaw(fh)
			if err != nil {
				t.Fatal(err)
			}
			w.Write(payload)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

func newTestProcessor(cfg config.PostProcessConfig) *Processor {
	return New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func extractCfg(passwords ...string) config.PostProcessConfig {
	return config.PostProcessConfig{VerifyChecksums: true, Extract: true, Passwords: passwords}
}

// copyFixtures copies testdata/postprocess files into a temp dir.
func copyFixtures(t *testing.T, names ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, n := range names {
		b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "postprocess", n))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, n), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// assertNoStaging fails if a staging directory survived under dir.
func assertNoStaging(t *testing.T, dir string) {
	t.Helper()
	m, _ := filepath.Glob(filepath.Join(dir, ".adesc-extract-*"))
	if len(m) > 0 {
		t.Fatalf("staging dirs left behind: %v", m)
	}
}

func ptr[T any](v T) *T { return &v }
