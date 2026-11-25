package postprocess

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"admindescargas/internal/fsutil"
)

// Archive formats.
const (
	formatZip = "zip"
	format7z  = "7z"
	formatRar = "rar"
)

var (
	magicZip      = []byte("PK\x03\x04")
	magicZipEmpty = []byte("PK\x05\x06")
	magic7z       = []byte{'7', 'z', 0xbc, 0xaf, 0x27, 0x1c}
	magicRar4     = []byte("Rar!\x1a\x07\x00")
	magicRar5     = []byte("Rar!\x1a\x07\x01\x00")
)

var (
	rePartRar  = regexp.MustCompile(`(?i)^(.*\.part)(\d+)(\.rar)$`)
	reRar      = regexp.MustCompile(`(?i)^(.*)\.(rar)$`)
	reOldRarNN = regexp.MustCompile(`(?i)^(.*)\.([r-y])(\d\d)$`)
	reNumVol   = regexp.MustCompile(`(?i)^(.*\.(7z|zip))\.(\d{3,})$`)
	reSpanZ    = regexp.MustCompile(`(?i)^(.*)\.z(\d\d)$`)
	reSingle   = regexp.MustCompile(`(?i)^(.*)\.(7z|zip)$`)
	reStripExt = regexp.MustCompile(`(?i)(\.part\d+)?\.(rar|7z|zip)(\.\d{3,})?$`)
)

// archiveSet is an archive split in one or more volumes.
type archiveSet struct {
	format string
	first  string
	// volumes are the volumes present on disk, in order, starting with
	// first (empty if first is missing).
	volumes []string
	// oldNaming is true for .rar/.r00/.r01 sets.
	oldNaming bool
	// multi is true when the naming says the archive is split.
	multi bool
	// unsupported, if set, is returned instead of extracting.
	unsupported error

	aesOnce sync.Once
	aes     bool
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func pad(n, width int) string {
	s := strconv.Itoa(n)
	for len(s) < width {
		s = "0" + s
	}
	return s
}

// detectSet recognizes archives by name and confirms them by magic bytes.
// It returns nil (no error) for files that are not archives.
func detectSet(path string) (*archiveSet, error) {
	dir, base := filepath.Split(path)
	set := &archiveSet{}
	switch {
	case rePartRar.MatchString(base):
		m := rePartRar.FindStringSubmatch(base)
		width := len(m[2])
		set.format, set.multi = formatRar, true
		set.first = filepath.Join(dir, m[1]+pad(1, width)+m[3])
		for i := 1; ; i++ {
			v := filepath.Join(dir, m[1]+pad(i, width)+m[3])
			if !exists(v) {
				break
			}
			set.volumes = append(set.volumes, v)
		}
	case reSpanZ.MatchString(base):
		set.format, set.first, set.volumes = formatZip, path, []string{path}
		set.unsupported = fmt.Errorf("%w: %q is part of a spanned zip (.z01...); join it with a zip tool", ErrUnsupported, base)
		return set, nil
	case reRar.MatchString(base) || reOldRarNN.MatchString(base):
		var stem, ext string
		if m := reRar.FindStringSubmatch(base); m != nil {
			stem, ext = m[1], m[2]
		} else {
			m := reOldRarNN.FindStringSubmatch(base)
			stem = m[1]
			ext = "rar"
			if m[2] == strings.ToUpper(m[2]) {
				ext = "RAR"
			}
			if !exists(filepath.Join(dir, stem+"."+ext)) && exists(filepath.Join(dir, stem+"."+strings.ToLower(ext))) {
				ext = strings.ToLower(ext)
			}
			if !exists(filepath.Join(dir, stem+"."+ext)) && !strings.EqualFold(m[2], "r") {
				return nil, nil // .s01 & co. without a .rar: not ours
			}
			set.multi = true
		}
		set.format, set.oldNaming = formatRar, true
		set.first = filepath.Join(dir, stem+"."+ext)
		if exists(set.first) {
			set.volumes = append(set.volumes, set.first)
			upper := ext == "RAR"
			for i := 0; ; i++ {
				letter := byte('r' + i/100)
				if letter > 'y' {
					break
				}
				if upper {
					letter -= 'a' - 'A'
				}
				v := filepath.Join(dir, stem+"."+string(letter)+pad(i%100, 2))
				if !exists(v) {
					break
				}
				set.volumes = append(set.volumes, v)
				set.multi = true
			}
		}
	case reNumVol.MatchString(base):
		m := reNumVol.FindStringSubmatch(base)
		width := len(m[3])
		set.format, set.multi = strings.ToLower(m[2]), true
		set.first = filepath.Join(dir, m[1]+"."+pad(1, width))
		for i := 1; ; i++ {
			v := filepath.Join(dir, m[1]+"."+pad(i, width))
			if !exists(v) {
				break
			}
			set.volumes = append(set.volumes, v)
		}
	case reSingle.MatchString(base):
		m := reSingle.FindStringSubmatch(base)
		set.format, set.first, set.volumes = strings.ToLower(m[2]), path, []string{path}
		if set.format == formatZip && (exists(filepath.Join(dir, m[1]+".z01")) || exists(filepath.Join(dir, m[1]+".Z01"))) {
			set.unsupported = fmt.Errorf("%w: %q is a spanned zip (.z01...); join it with a zip tool", ErrUnsupported, base)
			return set, nil
		}
	default:
		return nil, nil
	}

	if len(set.volumes) == 0 {
		return set, nil // first volume not downloaded yet
	}
	format, err := sniff(set.first)
	if err != nil {
		return nil, err
	}
	switch {
	case format == set.format:
	case format == "":
		return nil, fmt.Errorf("%w: %q is not a valid %s archive", ErrCorrupt, filepath.Base(set.first), set.format)
	case set.multi:
		return nil, fmt.Errorf("%w: %q is a %s archive, not %s", ErrCorrupt, filepath.Base(set.first), format, set.format)
	default:
		set.format = format // misnamed single archive: trust the content
	}
	return set, nil
}

// sniff returns the format given by the magic bytes, or "".
func sniff(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("postprocess: %w", err)
	}
	defer f.Close()
	buf := make([]byte, 8)
	n, err := io.ReadFull(f, buf)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("postprocess: read %q: %w", filepath.Base(path), err)
	}
	buf = buf[:n]
	switch {
	case bytes.HasPrefix(buf, magicZip), bytes.HasPrefix(buf, magicZipEmpty):
		return formatZip, nil
	case bytes.HasPrefix(buf, magic7z):
		return format7z, nil
	case bytes.HasPrefix(buf, magicRar4), bytes.HasPrefix(buf, magicRar5):
		return formatRar, nil
	}
	return "", nil
}

// subdirName is the archive name without its archive extensions.
func subdirName(first string) string {
	name := reStripExt.ReplaceAllString(filepath.Base(first), "")
	return fsutil.SanitizeName(name)
}

func (s *archiveSet) totalSize() (int64, error) {
	var total int64
	for _, v := range s.volumes {
		st, err := os.Stat(v)
		if err != nil {
			return 0, fmt.Errorf("postprocess: %w", err)
		}
		total += st.Size()
	}
	return total, nil
}

// complete reports whether every volume of the set is on disk.
func (s *archiveSet) complete() (bool, error) {
	if len(s.volumes) == 0 {
		return false, nil
	}
	if !s.multi && s.format != formatRar {
		return true, nil
	}
	switch s.format {
	case format7z:
		want, err := sevenZipSize(s.first)
		if err != nil {
			return false, err
		}
		have, err := s.totalSize()
		if err != nil {
			return false, err
		}
		return have >= want, nil
	case formatZip:
		return s.zipComplete()
	case formatRar:
		last := s.volumes[len(s.volumes)-1]
		more, known, err := rarMoreVolumes(last)
		if err != nil {
			return false, err
		}
		if known {
			return !more, nil
		}
		// Headers are encrypted (or the format is too old to say): volumes
		// have the same size except the last one.
		if len(s.volumes) == 1 {
			return true, nil
		}
		firstSt, err1 := os.Stat(s.first)
		lastSt, err2 := os.Stat(last)
		if err := errors.Join(err1, err2); err != nil {
			return false, fmt.Errorf("postprocess: %w", err)
		}
		return lastSt.Size() < firstSt.Size(), nil
	}
	return true, nil
}

// sevenZipSize returns the total archive size from the 7z start header.
func sevenZipSize(first string) (int64, error) {
	f, err := os.Open(first)
	if err != nil {
		return 0, fmt.Errorf("postprocess: %w", err)
	}
	defer f.Close()
	var hdr [32]byte
	if _, err := io.ReadFull(f, hdr[:]); err != nil || !bytes.HasPrefix(hdr[:], magic7z) {
		return 0, fmt.Errorf("%w: bad 7z header in %q", ErrCorrupt, filepath.Base(first))
	}
	off := binary.LittleEndian.Uint64(hdr[12:20])
	size := binary.LittleEndian.Uint64(hdr[20:28])
	if off > 1<<62 || size > 1<<62 {
		return 0, fmt.Errorf("%w: bad 7z header in %q", ErrCorrupt, filepath.Base(first))
	}
	return int64(32 + off + size), nil //nolint:gosec // bounded above
}

// zipComplete looks for the end-of-central-directory record at the end of
// the concatenated volumes.
func (s *archiveSet) zipComplete() (bool, error) {
	mr, err := openMulti(s.volumes)
	if err != nil {
		return false, err
	}
	defer mr.Close()
	size := mr.Size()
	n := int64(22 + 65535)
	if n > size {
		n = size
	}
	tail := make([]byte, n)
	if _, err := mr.ReadAt(tail, size-n); err != nil && !errors.Is(err, io.EOF) {
		return false, fmt.Errorf("postprocess: %w", err)
	}
	for i := bytes.LastIndex(tail, magicZipEmpty); i >= 0; i = bytes.LastIndex(tail[:i], magicZipEmpty) {
		if i+22 <= len(tail) {
			comment := int(binary.LittleEndian.Uint16(tail[i+20 : i+22]))
			if i+22+comment == len(tail) {
				return true, nil
			}
		}
	}
	return false, nil
}

// rarMoreVolumes walks the block headers of a RAR volume and reports
// whether more volumes follow. known is false when that cannot be told
// (encrypted headers, no end-of-archive block).
func rarMoreVolumes(path string) (more, known bool, err error) {
	f, err := os.Open(path)
	if err != nil {
		return false, false, fmt.Errorf("postprocess: %w", err)
	}
	defer f.Close()
	sig := make([]byte, 8)
	n, _ := io.ReadFull(f, sig)
	sig = sig[:n]
	switch {
	case bytes.HasPrefix(sig, magicRar5):
		return rar5Walk(f)
	case bytes.HasPrefix(sig, magicRar4):
		return rar4Walk(f)
	}
	return false, false, fmt.Errorf("%w: %q is not a RAR volume", ErrCorrupt, filepath.Base(path))
}

func readVint(b []byte) (uint64, []byte, bool) {
	var v uint64
	for i := 0; i < len(b) && i < 10; i++ {
		v |= uint64(b[i]&0x7f) << (7 * i)
		if b[i]&0x80 == 0 {
			return v, b[i+1:], true
		}
	}
	return 0, nil, false
}

func rar5Walk(f *os.File) (more, known bool, err error) {
	off := int64(len(magicRar5))
	head := make([]byte, 4+10)
	for {
		n, _ := f.ReadAt(head, off)
		if n < 5 {
			return false, false, nil
		}
		size, rest, ok := readVint(head[4:n])
		if !ok || size == 0 || size > 2<<20 {
			return false, false, nil
		}
		vlen := int64(n-4) - int64(len(rest))
		hdr := make([]byte, size)
		if _, err := f.ReadAt(hdr, off+4+vlen); err != nil {
			return false, false, nil //nolint:nilerr // a short read means unknown (known=false), not an error
		}
		typ, b, ok1 := readVint(hdr)
		flags, b, ok2 := readVint(b)
		if !ok1 || !ok2 {
			return false, false, nil
		}
		var dataSize uint64
		if flags&0x01 != 0 {
			if _, b, ok1 = readVint(b); !ok1 {
				return false, false, nil
			}
		}
		if flags&0x02 != 0 {
			if dataSize, b, ok1 = readVint(b); !ok1 {
				return false, false, nil
			}
		}
		switch typ {
		case 1: // main archive header
			af, _, ok := readVint(b)
			if !ok {
				return false, false, nil
			}
			if af&0x01 == 0 {
				return false, true, nil // not a volume
			}
		case 4: // archive encryption header: the rest is encrypted
			return false, false, nil
		case 5: // end of archive
			ef, _, ok := readVint(b)
			if !ok {
				return false, false, nil
			}
			return ef&0x01 != 0, true, nil
		}
		if dataSize > 1<<62 {
			return false, false, nil
		}
		off += 4 + vlen + int64(size) + int64(dataSize) //nolint:gosec // bounded above
	}
}

func rar4Walk(f *os.File) (more, known bool, err error) {
	off := int64(len(magicRar4))
	var h [36]byte
	for {
		n, _ := f.ReadAt(h[:], off)
		if n < 7 {
			return false, false, nil
		}
		typ := h[2]
		flags := binary.LittleEndian.Uint16(h[3:5])
		size := int64(binary.LittleEndian.Uint16(h[5:7]))
		if size < 7 {
			return false, false, nil
		}
		var add int64
		switch {
		case typ == 0x74 || typ == 0x7a:
			if n < 11 {
				return false, false, nil
			}
			add = int64(binary.LittleEndian.Uint32(h[7:11]))
			if flags&0x100 != 0 {
				if n < 36 {
					return false, false, nil
				}
				add |= int64(binary.LittleEndian.Uint32(h[32:36])) << 32
			}
		case flags&0x8000 != 0:
			if n < 11 {
				return false, false, nil
			}
			add = int64(binary.LittleEndian.Uint32(h[7:11]))
		}
		switch typ {
		case 0x73: // main header
			if flags&0x0001 == 0 {
				return false, true, nil
			}
			if flags&0x0080 != 0 {
				return false, false, nil
			}
		case 0x7b: // end of archive
			return flags&0x0001 != 0, true, nil
		}
		if add < 0 {
			return false, false, nil
		}
		off += size + add
	}
}

// multiReaderAt concatenates volumes.
type multiReaderAt struct {
	files []*os.File
	offs  []int64 // start offset of each file
	size  int64
}

func openMulti(paths []string) (*multiReaderAt, error) {
	m := &multiReaderAt{}
	for _, p := range paths {
		f, err := os.Open(p)
		if err != nil {
			m.Close()
			return nil, fmt.Errorf("postprocess: %w", err)
		}
		st, err := f.Stat()
		if err != nil {
			f.Close()
			m.Close()
			return nil, fmt.Errorf("postprocess: %w", err)
		}
		m.files = append(m.files, f)
		m.offs = append(m.offs, m.size)
		m.size += st.Size()
	}
	return m, nil
}

func (m *multiReaderAt) Size() int64 { return m.size }

func (m *multiReaderAt) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, errors.New("postprocess: negative offset")
	}
	total := 0
	for len(p) > 0 {
		if off >= m.size {
			return total, io.EOF
		}
		i := len(m.offs) - 1
		for i > 0 && m.offs[i] > off {
			i--
		}
		end := m.size
		if i+1 < len(m.offs) {
			end = m.offs[i+1]
		}
		chunk := p
		if int64(len(chunk)) > end-off {
			chunk = chunk[:end-off]
		}
		n, err := m.files[i].ReadAt(chunk, off-m.offs[i])
		total += n
		off += int64(n)
		p = p[n:]
		if err != nil && !(errors.Is(err, io.EOF) && n == len(chunk)) {
			if errors.Is(err, io.EOF) {
				return total, io.ErrUnexpectedEOF
			}
			return total, err
		}
	}
	return total, nil
}

func (m *multiReaderAt) Close() error {
	var errs []error
	for _, f := range m.files {
		errs = append(errs, f.Close())
	}
	return errors.Join(errs...)
}
