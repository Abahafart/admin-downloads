package postprocess

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"strings"

	"github.com/bodgit/sevenzip"
)

// aes7zID is the 7zAES coder id.
var aes7zID = []byte{0x06, 0xf1, 0x07, 0x01}

// hasAES reports whether the (unencoded or encrypted) 7z header mentions
// the 7zAES coder. Headers compressed with LZMA hide it; read errors with
// ReadError.Encrypted cover that case.
func (s *archiveSet) hasAES() bool {
	s.aesOnce.Do(func() {
		mr, err := openMulti(s.volumes)
		if err != nil {
			return
		}
		defer mr.Close()
		var hdr [32]byte
		if _, err := mr.ReadAt(hdr[:], 0); err != nil {
			return
		}
		off := binary.LittleEndian.Uint64(hdr[12:20])
		size := binary.LittleEndian.Uint64(hdr[20:28])
		if size == 0 || size > 16<<20 || off > uint64(mr.Size()) { //nolint:gosec
			return
		}
		buf := make([]byte, size)
		if _, err := mr.ReadAt(buf, int64(32+off)); err != nil && !errors.Is(err, io.EOF) { //nolint:gosec
			return
		}
		s.aes = bytes.Contains(buf, aes7zID)
	})
	return s.aes
}

func isEncryptedReadErr(err error) bool {
	var re *sevenzip.ReadError
	if errors.As(err, &re) && re.Encrypted {
		return true
	}
	var rv sevenzip.ReadError
	if errors.As(err, &rv) && rv.Encrypted {
		return true
	}
	return strings.Contains(err.Error(), "aes7z")
}

func (x *extraction) extract7z(pw string) error {
	mr, err := openMulti(x.set.volumes)
	if err != nil {
		return err
	}
	defer mr.Close()
	encrypted := x.set.hasAES()
	z, err := sevenzip.NewReaderWithPassword(mr, mr.Size(), pw)
	if err != nil {
		if encrypted || isEncryptedReadErr(err) {
			return fmt.Errorf("%w: %v", errBadPassword, err)
		}
		return fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	var total int64
	for _, f := range z.File {
		total += int64(f.UncompressedSize) //nolint:gosec // only for progress
	}
	x.pw.p.Total = total

	classify := func(name string, err error) error {
		if ctxErr := x.ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if isEncryptedReadErr(err) || encrypted {
			return fmt.Errorf("%w: %q: %v", errBadPassword, name, err)
		}
		return fmt.Errorf("%w: %q: %v", ErrCorrupt, name, err)
	}

	for _, f := range z.File {
		if err := x.ctx.Err(); err != nil {
			return err
		}
		switch x.entryKind(f.Name, f.Mode(), f.FileInfo().IsDir()) {
		case "dir":
			if err := x.writeDir(f.Name); err != nil {
				return err
			}
		case "file":
			if isUnsafeName(f.Name) {
				x.reject(f.Name, "unsafe path")
				continue
			}
			rc, err := f.Open()
			if err != nil {
				return classify(f.Name, err)
			}
			h := crc32.NewIEEE()
			want := f.CRC32
			r := &checkedReader{
				r: io.TeeReader(rc, h),
				check: func() error {
					if want != 0 && h.Sum32() != want {
						return errors.New("CRC mismatch")
					}
					return nil
				},
			}
			err = x.writeFile(f.Name, r)
			rc.Close()
			if err != nil {
				if isWriteError(err) {
					return err
				}
				return classify(f.Name, err)
			}
		}
	}
	return nil
}
