// Package fsutil contains filesystem helpers shared by every component that
// writes files whose names come from untrusted sources (hosters, folder
// listings, Content-Disposition headers, user input).
//
// The rules are the strictest common denominator of Linux, macOS and Windows
// so that a download folder can be moved between systems:
//
//   - path separators ('/' and '\') never survive inside a component;
//   - "." and ".." components are dropped;
//   - characters invalid on Windows (<>:"|?* and control chars) become '_';
//   - trailing dots and spaces are removed (Windows silently strips them);
//   - reserved device names (CON, PRN, AUX, NUL, COM1-9, LPT1-9, also with an
//     extension such as "con.txt") are prefixed with '_';
//   - each component is limited to MaxComponentBytes bytes of UTF-8.
//
// SafeJoin additionally guarantees that the resulting path stays inside base.
package fsutil

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// MaxComponentBytes is the maximum length in bytes of a single sanitized path
// component (most filesystems limit names to 255 bytes).
const MaxComponentBytes = 255

// Replacement is the character used in place of invalid characters.
const Replacement = '_'

// ErrEmptyPath is returned by SafeJoin when relPath has no usable component.
var ErrEmptyPath = errors.New("fsutil: empty relative path")

// ErrOutsideBase is returned by SafeJoin if the joined path would escape base.
// With the sanitization rules this should be impossible; the check is a
// defense in depth.
var ErrOutsideBase = errors.New("fsutil: path escapes base directory")

// reserved holds Windows reserved device names (upper case).
var reserved = func() map[string]struct{} {
	m := map[string]struct{}{"CON": {}, "PRN": {}, "AUX": {}, "NUL": {}, "CONIN$": {}, "CONOUT$": {}}
	for _, p := range []string{"COM", "LPT"} {
		for i := 1; i <= 9; i++ {
			m[p+strconv.Itoa(i)] = struct{}{}
		}
		// Windows also treats superscript digits as reserved.
		for _, d := range []string{"¹", "²", "³"} {
			m[p+d] = struct{}{}
		}
	}
	return m
}()

// SanitizeName turns one untrusted path component into a safe file or
// directory name. It never returns an empty string, "." or "..": such inputs
// become "_". Separators inside the component are replaced, not interpreted;
// use SanitizeRelPath or SafeJoin for multi-component paths.
func SanitizeName(component string) string {
	if !utf8.ValidString(component) {
		component = strings.ToValidUTF8(component, string(Replacement))
	}
	component = norm.NFC.String(component)

	var b strings.Builder
	b.Grow(len(component))
	for _, r := range component {
		switch {
		case r < 0x20 || r == 0x7f:
			b.WriteRune(Replacement)
		case strings.ContainsRune(`<>:"/\|?*`, r):
			b.WriteRune(Replacement)
		default:
			b.WriteRune(r)
		}
	}
	s := strings.TrimLeft(b.String(), " ")
	s = strings.TrimRight(s, ". ")
	if s == "" {
		return string(Replacement)
	}

	s = truncateName(s, MaxComponentBytes)
	if isReserved(s) {
		s = truncateName(string(Replacement)+s, MaxComponentBytes)
	}
	return s
}

// isReserved reports whether name is a Windows reserved device name. Names are
// matched on the part before the first dot, ignoring trailing spaces
// ("CON .txt" and "con.tar.gz" are reserved too).
func isReserved(name string) bool {
	stem := name
	if i := strings.IndexByte(stem, '.'); i >= 0 {
		stem = stem[:i]
	}
	_, ok := reserved[strings.ToUpper(strings.TrimRight(stem, " "))]
	return ok
}

// truncateName shortens s to at most max bytes without splitting UTF-8
// sequences, keeping a short extension (<= 16 bytes) when possible.
func truncateName(s string, max int) string {
	if len(s) <= max {
		return s
	}
	ext := filepath.Ext(s)
	if len(ext) > 16 {
		ext = ""
	}
	stem := s[:len(s)-len(ext)]
	limit := max - len(ext)
	for limit > 0 && !utf8.RuneStart(stem[limit]) {
		limit--
	}
	stem = strings.TrimRight(stem[:limit], ". ")
	if stem == "" {
		stem = string(Replacement)
	}
	return stem + ext
}

// SplitRelPath splits an untrusted relative path on both '/' and '\',
// drops empty, "." and ".." components and sanitizes the rest. The result may
// be empty.
func SplitRelPath(relPath string) []string {
	parts := strings.FieldsFunc(relPath, func(r rune) bool { return r == '/' || r == '\\' })
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p == "." || p == ".." {
			continue
		}
		// Components made only of dots/spaces (e.g. "...", ". .") are
		// stripped to nothing by Windows; treat them like "..".
		if strings.Trim(p, ". ") == "" {
			continue
		}
		out = append(out, SanitizeName(p))
	}
	return out
}

// SanitizeRelPath returns the sanitized relative path using forward slashes
// (the canonical form stored in the database and shown in the UI). It returns
// "" if nothing usable remains.
func SanitizeRelPath(relPath string) string {
	return strings.Join(SplitRelPath(relPath), "/")
}

// SafeJoin joins base with the untrusted relPath. Every component of relPath
// is sanitized with SanitizeName, "." / ".." / empty components are dropped,
// absolute paths and drive letters are neutralized, and the result is
// verified to be strictly inside base. base itself is trusted and only
// cleaned (it is made absolute if it is not).
func SafeJoin(base, relPath string) (string, error) {
	parts := SplitRelPath(relPath)
	if len(parts) == 0 {
		return "", ErrEmptyPath
	}
	absBase, err := filepath.Abs(base)
	if err != nil {
		return "", fmt.Errorf("fsutil: resolve base %q: %w", base, err)
	}
	joined := filepath.Join(append([]string{absBase}, parts...)...)
	rel, err := filepath.Rel(absBase, joined)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("%w: %q", ErrOutsideBase, relPath)
	}
	return joined, nil
}

// UniquePath returns path if nothing exists there, otherwise the first free
// variant "name (1).ext", "name (2).ext", ... It is used by the "rename"
// exists-policy. There is an inherent race between the check and the
// creation; callers should create the file with O_EXCL and retry on
// os.ErrExist.
func UniquePath(path string) string {
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return path
	}
	dir, file := filepath.Split(path)
	ext := filepath.Ext(file)
	stem := strings.TrimSuffix(file, ext)
	for i := 1; ; i++ {
		cand := filepath.Join(dir, fmt.Sprintf("%s (%d)%s", stem, i, ext))
		if _, err := os.Lstat(cand); errors.Is(err, os.ErrNotExist) {
			return cand
		}
	}
}
