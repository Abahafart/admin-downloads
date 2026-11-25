package postprocess

import (
	"archive/zip"
	"bytes"
	"compress/flate"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/sha1" //nolint:gosec // WinZip AES uses HMAC-SHA1 / PBKDF2-SHA1
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
)

const (
	zipFlagEncrypted  = 0x1
	zipFlagDescriptor = 0x8
	zipMethodAES      = 99
	zipExtraAES       = 0x9901
)

func (x *extraction) extractZip(pw string) error {
	var (
		ra   io.ReaderAt
		size int64
	)
	if len(x.set.volumes) == 1 {
		f, err := os.Open(x.set.first)
		if err != nil {
			return fmt.Errorf("postprocess: %w", err)
		}
		defer f.Close()
		st, err := f.Stat()
		if err != nil {
			return fmt.Errorf("postprocess: %w", err)
		}
		ra, size = f, st.Size()
	} else {
		mr, err := openMulti(x.set.volumes)
		if err != nil {
			return err
		}
		defer mr.Close()
		ra, size = mr, mr.Size()
	}
	zr, err := zip.NewReader(ra, size)
	if err != nil && !errors.Is(err, zip.ErrInsecurePath) {
		return fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	var total int64
	for _, f := range zr.File {
		total += int64(f.UncompressedSize64) //nolint:gosec // only for progress
	}
	x.pw.p.Total = total

	for _, f := range zr.File {
		if err := x.ctx.Err(); err != nil {
			return err
		}
		isDir := f.FileInfo().IsDir() || (len(f.Name) > 0 && (f.Name[len(f.Name)-1] == '/' || f.Name[len(f.Name)-1] == '\\'))
		switch x.entryKind(f.Name, f.Mode(), isDir) {
		case "dir":
			if err := x.writeDir(f.Name); err != nil {
				return err
			}
		case "file":
			if isUnsafeName(f.Name) {
				x.reject(f.Name, "unsafe path")
				continue
			}
			rc, err := openZipEntry(f, pw)
			if err != nil {
				return err
			}
			err = x.writeFile(f.Name, rc)
			rc.Close()
			if err != nil {
				var ce flate.CorruptInputError
				if errors.Is(err, zip.ErrChecksum) || errors.Is(err, zip.ErrFormat) || errors.As(err, &ce) || errors.Is(err, io.ErrUnexpectedEOF) {
					return fmt.Errorf("%w: %q: %v", ErrCorrupt, f.Name, err)
				}
				return err
			}
		}
	}
	return nil
}

// openZipEntry opens f, decrypting ZipCrypto and WinZip AES entries.
func openZipEntry(f *zip.File, pw string) (io.ReadCloser, error) {
	encrypted := f.Flags&zipFlagEncrypted != 0 || f.Method == zipMethodAES
	if !encrypted {
		rc, err := f.Open()
		if err != nil {
			if errors.Is(err, zip.ErrAlgorithm) {
				return nil, fmt.Errorf("%w: %q uses compression method %d", ErrUnsupported, f.Name, f.Method)
			}
			return nil, fmt.Errorf("%w: %q: %v", ErrCorrupt, f.Name, err)
		}
		return rc, nil
	}
	if pw == "" {
		return nil, fmt.Errorf("%w: %q is encrypted", errBadPassword, f.Name)
	}
	raw, err := f.OpenRaw()
	if err != nil {
		return nil, fmt.Errorf("%w: %q: %v", ErrCorrupt, f.Name, err)
	}
	if f.Method == zipMethodAES {
		return openWinZipAES(f, raw, pw)
	}
	return openZipCrypto(f, raw, pw)
}

func decompressor(f *zip.File, method uint16, r io.Reader) (io.ReadCloser, error) {
	switch method {
	case zip.Store:
		return io.NopCloser(r), nil
	case zip.Deflate:
		return flate.NewReader(r), nil
	}
	return nil, fmt.Errorf("%w: %q uses compression method %d", ErrUnsupported, f.Name, method)
}

// --- traditional PKWARE encryption (ZipCrypto) ---

type zipCryptoKeys [3]uint32

func newZipCryptoKeys(pw string) *zipCryptoKeys {
	k := &zipCryptoKeys{0x12345678, 0x23456789, 0x34567890}
	for i := 0; i < len(pw); i++ {
		k.update(pw[i])
	}
	return k
}

func crc32Update(crc uint32, b byte) uint32 {
	return crc32.IEEETable[byte(crc)^b] ^ (crc >> 8) //nolint:gosec // G115: ZipCrypto takes the low byte on purpose
}

func (k *zipCryptoKeys) update(b byte) {
	k[0] = crc32Update(k[0], b)
	k[1] = (k[1]+(k[0]&0xff))*134775813 + 1
	k[2] = crc32Update(k[2], byte(k[1]>>24))
}

func (k *zipCryptoKeys) stream() byte {
	t := k[2] | 2
	return byte((t * (t ^ 1)) >> 8) //nolint:gosec // G115: ZipCrypto takes the low byte on purpose
}

func (k *zipCryptoKeys) decrypt(b []byte) {
	for i := range b {
		b[i] ^= k.stream()
		k.update(b[i])
	}
}

func (k *zipCryptoKeys) encrypt(b []byte) {
	for i := range b {
		c := b[i] ^ k.stream()
		k.update(b[i])
		b[i] = c
	}
}

type zipCryptoReader struct {
	r    io.Reader
	keys *zipCryptoKeys
}

func (z *zipCryptoReader) Read(b []byte) (int, error) {
	n, err := z.r.Read(b)
	z.keys.decrypt(b[:n])
	return n, err
}

// zipCryptoCheckByte is the last byte of the decrypted 12-byte header.
func zipCryptoCheckByte(f *zip.File) byte {
	if f.Flags&zipFlagDescriptor != 0 {
		return byte(f.ModifiedTime >> 8)
	}
	return byte(f.CRC32 >> 24)
}

func openZipCrypto(f *zip.File, raw io.Reader, pw string) (io.ReadCloser, error) {
	if f.CompressedSize64 < 12 {
		return nil, fmt.Errorf("%w: %q: short encrypted entry", ErrCorrupt, f.Name)
	}
	keys := newZipCryptoKeys(pw)
	var hdr [12]byte
	if _, err := io.ReadFull(raw, hdr[:]); err != nil {
		return nil, fmt.Errorf("%w: %q: %v", ErrCorrupt, f.Name, err)
	}
	keys.decrypt(hdr[:])
	if hdr[11] != zipCryptoCheckByte(f) {
		return nil, fmt.Errorf("%w: %q", errBadPassword, f.Name)
	}
	data := &zipCryptoReader{r: io.LimitReader(raw, int64(f.CompressedSize64-12)), keys: keys} //nolint:gosec
	dc, err := decompressor(f, f.Method, data)
	if err != nil {
		return nil, err
	}
	// The check byte has a 1/256 false-positive rate: a CRC mismatch on an
	// encrypted entry most likely means a wrong password.
	return verifyZipData(f, dc, true, errBadPassword, nil), nil
}

// verifyZipData checks size and (optionally) CRC at EOF; final runs last.
func verifyZipData(f *zip.File, rc io.ReadCloser, checkCRC bool, crcErr error, final func() error) io.ReadCloser {
	h := crc32.NewIEEE()
	var n int64
	cr := &checkedReader{
		r: io.TeeReader(rc, writerFunc(func(b []byte) (int, error) {
			n += int64(len(b))
			return h.Write(b)
		})),
		check: func() error {
			if uint64(n) != f.UncompressedSize64 { //nolint:gosec
				return fmt.Errorf("%w: %q: size mismatch", crcErr, f.Name)
			}
			if checkCRC && h.Sum32() != f.CRC32 {
				return fmt.Errorf("%w: %q: CRC mismatch", crcErr, f.Name)
			}
			if final != nil {
				return final()
			}
			return nil
		},
	}
	return struct {
		io.Reader
		io.Closer
	}{cr, rc}
}

type writerFunc func([]byte) (int, error)

func (w writerFunc) Write(b []byte) (int, error) { return w(b) }

// --- WinZip AES (method 99, AE-1 / AE-2) ---

type winzipAESExtra struct {
	version  uint16
	strength byte
	method   uint16
}

func parseAESExtra(extra []byte) (winzipAESExtra, bool) {
	for len(extra) >= 4 {
		id := binary.LittleEndian.Uint16(extra[0:2])
		size := int(binary.LittleEndian.Uint16(extra[2:4]))
		extra = extra[4:]
		if size > len(extra) {
			break
		}
		if id == zipExtraAES && size >= 7 {
			d := extra[:size]
			return winzipAESExtra{
				version:  binary.LittleEndian.Uint16(d[0:2]),
				strength: d[4],
				method:   binary.LittleEndian.Uint16(d[5:7]),
			}, true
		}
		extra = extra[size:]
	}
	return winzipAESExtra{}, false
}

func aesKeyLen(strength byte) int {
	switch strength {
	case 1:
		return 16
	case 2:
		return 24
	case 3:
		return 32
	}
	return 0
}

// winzipKeys derives the AES key, HMAC key and 2-byte password verifier.
func winzipKeys(pw string, salt []byte, keyLen int) (aesKey, macKey, verifier []byte, err error) {
	dk, err := pbkdf2.Key(sha1.New, pw, salt, 1000, 2*keyLen+2)
	if err != nil {
		return nil, nil, nil, err
	}
	return dk[:keyLen], dk[keyLen : 2*keyLen], dk[2*keyLen:], nil
}

// winzipCTR is AES in CTR mode with a little-endian counter starting at 1.
type winzipCTR struct {
	block cipher.Block
	ctr   [aes.BlockSize]byte
	ks    [aes.BlockSize]byte
	pos   int
}

func newWinzipCTR(key []byte) (*winzipCTR, error) {
	b, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return &winzipCTR{block: b, pos: aes.BlockSize}, nil
}

func (c *winzipCTR) XORKeyStream(dst, src []byte) {
	for i := range src {
		if c.pos == aes.BlockSize {
			for j := range c.ctr {
				c.ctr[j]++
				if c.ctr[j] != 0 {
					break
				}
			}
			c.block.Encrypt(c.ks[:], c.ctr[:])
			c.pos = 0
		}
		dst[i] = src[i] ^ c.ks[c.pos]
		c.pos++
	}
}

func openWinZipAES(f *zip.File, raw io.Reader, pw string) (io.ReadCloser, error) {
	ex, ok := parseAESExtra(f.Extra)
	keyLen := aesKeyLen(ex.strength)
	if !ok || keyLen == 0 {
		return nil, fmt.Errorf("%w: %q: bad WinZip AES header", ErrCorrupt, f.Name)
	}
	saltLen := keyLen / 2
	overhead := uint64(saltLen + 2 + 10) //nolint:gosec
	if f.CompressedSize64 < overhead {
		return nil, fmt.Errorf("%w: %q: short encrypted entry", ErrCorrupt, f.Name)
	}
	head := make([]byte, saltLen+2)
	if _, err := io.ReadFull(raw, head); err != nil {
		return nil, fmt.Errorf("%w: %q: %v", ErrCorrupt, f.Name, err)
	}
	aesKey, macKey, verifier, err := winzipKeys(pw, head[:saltLen], keyLen)
	if err != nil {
		return nil, fmt.Errorf("postprocess: derive key: %w", err)
	}
	if !bytes.Equal(verifier, head[saltLen:]) {
		return nil, fmt.Errorf("%w: %q", errBadPassword, f.Name)
	}
	ctr, err := newWinzipCTR(aesKey)
	if err != nil {
		return nil, fmt.Errorf("postprocess: %w", err)
	}
	mac := hmac.New(sha1.New, macKey)
	enc := io.TeeReader(io.LimitReader(raw, int64(f.CompressedSize64-overhead)), mac) //nolint:gosec
	plain := cipher.StreamReader{S: ctr, R: enc}
	dc, err := decompressor(f, ex.method, plain)
	if err != nil {
		return nil, err
	}
	checkMAC := func() error {
		// Drain what the decompressor did not consume, then compare.
		if _, err := io.Copy(io.Discard, plain); err != nil {
			return fmt.Errorf("%w: %q: %v", ErrCorrupt, f.Name, err)
		}
		var code [10]byte
		if _, err := io.ReadFull(raw, code[:]); err != nil {
			return fmt.Errorf("%w: %q: %v", ErrCorrupt, f.Name, err)
		}
		if !hmac.Equal(mac.Sum(nil)[:10], code[:]) {
			return fmt.Errorf("%w: %q: authentication code mismatch", ErrCorrupt, f.Name)
		}
		return nil
	}
	// AE-2 stores CRC 0; AE-1 has a real CRC.
	checkCRC := ex.version == 1 || f.CRC32 != 0
	return verifyZipData(f, dc, checkCRC, ErrCorrupt, checkMAC), nil
}
