//go:build genfixtures

// This file regenerates the 7z and RAR fixtures in testdata/postprocess.
// There is no 7z/rar creator in the build environment, so the archives are
// hand-written here following the format specifications (7z: 7zFormat.txt
// from the 7-Zip sources; RAR 5.0 and RAR 4.x: the technotes on
// rarlab.com). Only the "store"/"copy" methods are used, plus 7zAES and
// RAR5 AES-256 encryption, which is enough to exercise volume handling,
// password handling and path sanitizing; decompression itself is the
// libraries' job. Every input below is fixed, so the output is
// reproducible:
//
//	go test -tags genfixtures -run TestGenerateFixtures ./internal/postprocess
package postprocess

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"hash/crc32"
	"os"
	"path/filepath"
	"testing"
	"unicode/utf16"
)

type fxFile struct {
	name string
	data []byte
	dir  bool
}

func le16(v uint16) []byte { return binary.LittleEndian.AppendUint16(nil, v) }
func le32(v uint32) []byte { return binary.LittleEndian.AppendUint32(nil, v) }
func le64(v uint64) []byte { return binary.LittleEndian.AppendUint64(nil, v) }

func cat(parts ...[]byte) []byte { return bytes.Join(parts, nil) }

func zeroPad16(b []byte) []byte {
	out := append([]byte(nil), b...)
	for len(out)%aes.BlockSize != 0 {
		out = append(out, 0)
	}
	return out
}

func cbcEncrypt(key, iv, plain []byte) []byte {
	blk, err := aes.NewCipher(key)
	if err != nil {
		panic(err)
	}
	out := zeroPad16(plain)
	cipher.NewCBCEncrypter(blk, iv).CryptBlocks(out, out)
	return out
}

// --- 7z ---

func sz7Num(v uint64) []byte {
	if v < 0x80 {
		return []byte{byte(v)}
	}
	for n := 1; n < 8; n++ {
		if v < 1<<(7*(n+1)) {
			b := []byte{byte(0xff<<(8-n)) | byte(v>>(8*n))}
			for i := 0; i < n; i++ {
				b = append(b, byte(v>>(8*i)))
			}
			return b
		}
	}
	return cat([]byte{0xff}, le64(v))
}

// build7z writes a 7z archive with every file in one folder using the Copy
// coder, or a single 7zAES coder when password != "".
func build7z(files []fxFile, password string, cycles byte, iv []byte) []byte {
	var plain []byte
	for _, f := range files {
		plain = append(plain, f.data...)
	}
	pack := plain
	var coder []byte
	if password == "" {
		coder = []byte{0x01, 0x00} // id size 1, id 00 = Copy
	} else {
		var salt []byte
		pw := utf16le(password)
		h := sha256.New()
		for i := uint64(0); i < 1<<cycles; i++ {
			h.Write(salt)
			h.Write(pw)
			h.Write(le64(i))
		}
		key := h.Sum(nil)
		pack = cbcEncrypt(key, iv, plain)
		props := cat([]byte{0x40 | cycles, byte(len(iv) - 1)}, iv) // no salt, 16-byte IV
		coder = cat([]byte{0x24}, aes7zID, sz7Num(uint64(len(props))), props)
	}

	var h []byte
	h = append(h, 0x01, 0x04) // Header, MainStreamsInfo
	h = append(h, 0x06)       // PackInfo
	h = cat(h, sz7Num(0), sz7Num(1), []byte{0x09}, sz7Num(uint64(len(pack))), []byte{0x00})
	h = append(h, 0x07, 0x0b) // UnPackInfo, Folder
	h = cat(h, sz7Num(1), []byte{0x00}, sz7Num(1), coder)
	h = cat(h, []byte{0x0c}, sz7Num(uint64(len(plain))), []byte{0x00})
	h = append(h, 0x08, 0x0d) // SubStreamsInfo, NumUnPackStream
	h = append(h, sz7Num(uint64(len(files)))...)
	if len(files) > 1 {
		h = append(h, 0x09)
		for _, f := range files[:len(files)-1] {
			h = append(h, sz7Num(uint64(len(f.data)))...)
		}
	}
	h = append(h, 0x0a, 0x01)
	for _, f := range files {
		h = append(h, le32(crc32.ChecksumIEEE(f.data))...)
	}
	h = append(h, 0x00, 0x00) // end SubStreamsInfo, end MainStreamsInfo
	h = cat(h, []byte{0x05}, sz7Num(uint64(len(files))))
	names := []byte{0x00}
	for _, f := range files {
		names = cat(names, utf16le(f.name), []byte{0, 0})
	}
	h = cat(h, []byte{0x11}, sz7Num(uint64(len(names))), names)
	h = append(h, 0x00, 0x00) // end FilesInfo, end Header

	start := cat(le64(uint64(len(pack))), le64(uint64(len(h))), le32(crc32.ChecksumIEEE(h)))
	return cat(magic7z, []byte{0x00, 0x04}, le32(crc32.ChecksumIEEE(start)), start, pack, h)
}

func utf16le(s string) []byte {
	var b []byte
	for _, u := range utf16.Encode([]rune(s)) {
		b = append(b, le16(u)...)
	}
	return b
}

// --- RAR 5 ---

func vint(v uint64) []byte {
	var out []byte
	for {
		b := byte(v & 0x7f)
		v >>= 7
		if v == 0 {
			return append(out, b)
		}
		out = append(out, b|0x80)
	}
}

func rar5Block(hdr []byte) []byte {
	size := vint(uint64(len(hdr)))
	return cat(le32(crc32.ChecksumIEEE(cat(size, hdr))), size, hdr)
}

func rar5Main(volume bool, volNum int) []byte {
	var flags uint64
	if volume {
		flags |= 0x01
	}
	if volNum > 0 {
		flags |= 0x02
	}
	h := cat(vint(1), vint(0), vint(flags))
	if volNum > 0 {
		h = append(h, vint(uint64(volNum))...)
	}
	return rar5Block(h)
}

func rar5End(more bool) []byte {
	var f uint64
	if more {
		f = 1
	}
	return rar5Block(cat(vint(5), vint(0), vint(f)))
}

const fxMtime = 1767225600 // 2026-01-01T00:00:00Z

type rar5Part struct {
	file        fxFile
	data        []byte // packed data of this part
	unpSize     int
	crc         uint32
	splitBefore bool
	splitAfter  bool
	extra       []byte
}

func rar5File(p rar5Part) []byte {
	var hflags uint64
	if len(p.extra) > 0 {
		hflags |= 0x01
	}
	if !p.file.dir {
		hflags |= 0x02
	}
	if p.splitBefore {
		hflags |= 0x08
	}
	if p.splitAfter {
		hflags |= 0x10
	}
	ff := uint64(0x02 | 0x04) // mtime, crc
	attr := uint64(0o100644)
	if p.file.dir {
		ff = 0x01 | 0x02
		attr = 0o40755
	}
	h := cat(vint(2), vint(hflags))
	if len(p.extra) > 0 {
		h = append(h, vint(uint64(len(p.extra)))...)
	}
	if !p.file.dir {
		h = append(h, vint(uint64(len(p.data)))...)
	}
	h = cat(h, vint(ff), vint(uint64(p.unpSize)), vint(attr), le32(fxMtime))
	if !p.file.dir {
		h = append(h, le32(p.crc)...)
	}
	h = cat(h, vint(0), vint(1), vint(uint64(len(p.file.name))), []byte(p.file.name), p.extra)
	return cat(rar5Block(h), p.data)
}

func rar5Stored(f fxFile) []byte {
	return rar5File(rar5Part{file: f, data: f.data, unpSize: len(f.data), crc: crc32.ChecksumIEEE(f.data)})
}

// rar5Keys mirrors the RAR5 KDF: PBKDF2-HMAC-SHA256 continued 16 and 32
// more rounds for the hash key and the password check value.
func rar5Keys(pw string, salt []byte, lg2 byte) (key, check []byte) {
	prf := hmac.New(sha256.New, []byte(pw))
	prf.Write(salt)
	prf.Write([]byte{0, 0, 0, 1})
	t := prf.Sum(nil)
	u := append([]byte(nil), t...)
	var keys [3][]byte
	for i, iter := range []int{1<<lg2 - 1, 16, 16} {
		for ; iter > 0; iter-- {
			prf.Reset()
			prf.Write(u)
			u = prf.Sum(u[:0])
			for j := range u {
				t[j] ^= u[j]
			}
		}
		keys[i] = append([]byte(nil), t...)
	}
	pc := make([]byte, 8)
	for i, v := range keys[2] {
		pc[i%8] ^= v
	}
	sum := sha256.Sum256(pc)
	return keys[0], cat(pc, sum[:4])
}

func rar5Encrypted(f fxFile, pw string, salt, iv []byte, lg2 byte) []byte {
	key, check := rar5Keys(pw, salt, lg2)
	rec := cat(vint(1), vint(0), vint(0x01), []byte{lg2}, salt, iv, check)
	extra := cat(vint(uint64(len(rec))), rec)
	return rar5File(rar5Part{
		file: f, data: cbcEncrypt(key, iv, f.data), unpSize: len(f.data),
		crc: crc32.ChecksumIEEE(f.data), extra: extra,
	})
}

// --- RAR 4 ---

func rar4Block(typ byte, flags uint16, body []byte) []byte {
	b := cat([]byte{typ}, le16(flags), le16(uint16(7+len(body))), body)
	return cat(le16(uint16(crc32.ChecksumIEEE(b))), b)
}

const fxDosTime = uint32((2026-1980)<<25 | 1<<21 | 1<<16)

func rar4File(name string, part []byte, unpSize int, crc uint32, before, after bool) []byte {
	flags := uint16(0x8000)
	if before {
		flags |= 0x01
	}
	if after {
		flags |= 0x02
	}
	body := cat(le32(uint32(len(part))), le32(uint32(unpSize)), []byte{3}, le32(crc), le32(fxDosTime),
		[]byte{29, 0x30}, le16(uint16(len(name))), le32(0o100644), []byte(name))
	return cat(rar4Block(0x74, flags, body), part)
}

func TestGenerateFixtures(t *testing.T) {
	dir := filepath.Join("..", "..", "testdata", "postprocess")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name string, b []byte) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	iv := []byte("adesc-fixture-iv")   // 16 bytes
	salt := []byte("adesc-fixture-sl") // 16 bytes

	basic := []fxFile{
		{name: "hello.txt", data: []byte("Hola desde AdminDescargas\n")},
		{name: "dir/nested.txt", data: []byte("archivo anidado\n")},
	}

	// 7z
	write("copy.7z", build7z(basic, "", 0, nil))
	write("aes.7z", build7z([]fxFile{{name: "secret.txt", data: []byte("contenido cifrado con 7zAES\n")}}, "secreto7z", 6, iv))
	write("traversal.7z", build7z([]fxFile{
		{name: "../evil.txt", data: []byte("evil\n")},
		{name: "ok.txt", data: []byte("ok\n")},
	}, "", 0, nil))
	big := build7z([]fxFile{{name: "split.txt", data: bytes.Repeat([]byte("0123456789abcdef"), 40)}}, "", 0, nil)
	write("split.7z.001", big[:400])
	write("split.7z.002", big[400:])

	// RAR5 stored
	sig5 := magicRar5
	write("stored.rar", cat(sig5, rar5Main(false, 0),
		rar5Stored(fxFile{name: "dir", dir: true}),
		rar5Stored(basic[0]), rar5Stored(basic[1]), rar5End(false)))

	// RAR5 encrypted file data (headers in clear), with password check.
	write("enc.rar", cat(sig5, rar5Main(false, 0),
		rar5Encrypted(fxFile{name: "secret.txt", data: []byte("contenido cifrado con RAR5\n")}, "secretorar", salt, iv, 15),
		rar5End(false)))

	// RAR5 traversal
	write("traversal.rar", cat(sig5, rar5Main(false, 0),
		rar5Stored(fxFile{name: "../evil.txt", data: []byte("evil\n")}),
		rar5Stored(fxFile{name: "ok.txt", data: []byte("ok\n")}),
		rar5End(false)))

	// RAR5 multi-volume, new naming: big.txt spans both volumes.
	bigData := bytes.Repeat([]byte("RAR5 multi-volume line\n"), 30)
	p1, p2 := bigData[:300], bigData[300:]
	small := fxFile{name: "small.txt", data: []byte("en el segundo volumen\n")}
	write("multi.part1.rar", cat(sig5, rar5Main(true, 0),
		rar5File(rar5Part{file: fxFile{name: "big.txt"}, data: p1, unpSize: len(bigData), crc: crc32.ChecksumIEEE(p1), splitAfter: true}),
		rar5End(true)))
	write("multi.part2.rar", cat(sig5, rar5Main(true, 1),
		rar5File(rar5Part{file: fxFile{name: "big.txt"}, data: p2, unpSize: len(bigData), crc: crc32.ChecksumIEEE(bigData), splitBefore: true}),
		rar5Stored(small),
		rar5End(false)))

	// RAR4 multi-volume, old naming (.rar, .r00).
	sig4 := magicRar4
	oldData := bytes.Repeat([]byte("RAR4 old naming\n"), 20)
	o1, o2 := oldData[:200], oldData[200:]
	write("old.rar", cat(sig4, rar4Block(0x73, 0x0001|0x0100, make([]byte, 6)),
		rar4File("old.txt", o1, len(oldData), crc32.ChecksumIEEE(o1), false, true),
		rar4Block(0x7b, 0x0001, nil)))
	write("old.r00", cat(sig4, rar4Block(0x73, 0x0001, make([]byte, 6)),
		rar4File("old.txt", o2, len(oldData), crc32.ChecksumIEEE(oldData), true, false),
		rar4Block(0x7b, 0x0000, nil)))
}
