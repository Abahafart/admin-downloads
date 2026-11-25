package postprocess

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"admindescargas/internal/fsutil"
)

// errBadPassword is returned by the format extractors when the archive
// needs a (different) password; run then tries the next candidate.
var errBadPassword = errors.New("postprocess: password needed or wrong")

// errMissingVolume is returned when a volume disappears or a set turns
// out to be incomplete during extraction.
var errMissingVolume = errors.New("postprocess: missing archive volume")

// extraction holds the state of one extraction of an archive set.
type extraction struct {
	ctx       context.Context
	log       *slog.Logger
	set       *archiveSet
	dest      string
	overwrite bool
	progress  func(Progress)

	// per attempt
	staging string
	staged  []string // rel paths of files in staging, in order
	isStage map[string]bool
	dirs    []string
	pw      *progressWriter

	// results of the successful attempt
	extracted []string
	rejected  []string
}

func (x *extraction) run(passwords []string) error {
	if err := os.MkdirAll(x.dest, 0o755); err != nil { //nolint:gosec // G301: extracted files are user files, same perms as downloads
		return fmt.Errorf("postprocess: create %q: %w", x.dest, err)
	}
	name := filepath.Base(x.set.first)
	err := x.attempt("")
	if err == nil || !errors.Is(err, errBadPassword) {
		return err
	}
	if len(passwords) == 0 {
		return fmt.Errorf("%w: %q", ErrPasswordRequired, name)
	}
	for _, pw := range passwords {
		err = x.attempt(pw)
		if err == nil || !errors.Is(err, errBadPassword) {
			return err
		}
	}
	return fmt.Errorf("%w: %q: tried %d password(s), none worked", ErrWrongPassword, name, len(passwords))
}

// attempt extracts the whole set with pw into a fresh staging directory
// and moves the result into dest on success.
func (x *extraction) attempt(pw string) error {
	staging, err := os.MkdirTemp(x.dest, ".adesc-extract-*")
	if err != nil {
		return fmt.Errorf("postprocess: staging dir: %w", err)
	}
	defer os.RemoveAll(staging)
	x.staging = staging
	x.staged, x.dirs, x.rejected = nil, nil, nil
	x.isStage = map[string]bool{}
	x.pw = &progressWriter{progress: x.progress, p: Progress{Stage: StageExtract, Total: -1}}

	switch x.set.format {
	case formatZip:
		err = x.extractZip(pw)
	case format7z:
		err = x.extract7z(pw)
	case formatRar:
		err = x.extractRar(pw)
	default:
		err = fmt.Errorf("%w: format %q", ErrUnsupported, x.set.format)
	}
	if err != nil {
		if ctxErr := x.ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return err
	}
	x.pw.flush()
	return x.commit()
}

// isUnsafeName reports whether an entry name tries to escape the
// destination: absolute, drive letter / UNC, or a ".." component.
func isUnsafeName(raw string) bool {
	n := strings.ReplaceAll(raw, `\`, "/")
	if strings.ContainsRune(n, 0) || strings.HasPrefix(n, "/") {
		return true
	}
	if len(n) >= 2 && n[1] == ':' && ((n[0] >= 'a' && n[0] <= 'z') || (n[0] >= 'A' && n[0] <= 'Z')) {
		return true
	}
	for _, c := range strings.Split(n, "/") {
		if strings.TrimRight(c, " ") == ".." {
			return true
		}
	}
	return false
}

func (x *extraction) reject(name, reason string) {
	x.rejected = append(x.rejected, name)
	x.log.Warn("postprocess: archive entry rejected", "archive", filepath.Base(x.set.first),
		"entry", name, "reason", reason)
}

// target validates an entry name and returns its staging path and the
// sanitized relative path ("" = skip).
func (x *extraction) target(name string) (string, string) {
	if isUnsafeName(name) {
		x.reject(name, "unsafe path")
		return "", ""
	}
	p, err := fsutil.SafeJoin(x.staging, name)
	if err != nil {
		if !errors.Is(err, fsutil.ErrEmptyPath) {
			x.reject(name, "unsafe path")
		}
		return "", ""
	}
	rel, err := filepath.Rel(x.staging, p)
	if err != nil {
		x.reject(name, "unsafe path")
		return "", ""
	}
	return p, rel
}

// entryMode decides how to handle an entry from its mode: "dir", "file"
// or "" (rejected).
func (x *extraction) entryKind(name string, mode fs.FileMode, isDir bool) string {
	switch {
	case mode&fs.ModeSymlink != 0:
		x.reject(name, "symbolic link")
		return ""
	case isDir:
		return "dir"
	case mode.Type() != 0:
		x.reject(name, "special file")
		return ""
	}
	return "file"
}

func (x *extraction) writeDir(name string) error {
	p, rel := x.target(name)
	if p == "" {
		return nil
	}
	if err := os.MkdirAll(p, 0o755); err != nil { //nolint:gosec // G301: extracted files are user files, same perms as downloads
		return &writeError{err}
	}
	x.dirs = append(x.dirs, rel)
	return nil
}

// writeError marks failures writing to disk (as opposed to reading the
// archive), so extractors do not mistake them for bad passwords.
type writeError struct{ err error }

func (e *writeError) Error() string { return "postprocess: write: " + e.err.Error() }
func (e *writeError) Unwrap() error { return e.err }

func isWriteError(err error) bool {
	var we *writeError
	return errors.As(err, &we)
}

type fileWriter struct{ f *os.File }

func (w fileWriter) Write(b []byte) (int, error) {
	n, err := w.f.Write(b)
	if err != nil {
		err = &writeError{err}
	}
	return n, err
}

// writeFile copies r into the staging path of name. Errors writing to disk
// are *writeError; errors reading r are returned as they are.
func (x *extraction) writeFile(name string, r io.Reader) error {
	if err := x.ctx.Err(); err != nil {
		return err
	}
	p, rel := x.target(name)
	if p == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil { //nolint:gosec // G301: extracted files are user files, same perms as downloads
		return &writeError{err}
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644) //nolint:gosec // G302: extracted files are user files, same perms as downloads
	if err != nil {
		return &writeError{err}
	}
	x.pw.p.File = name
	x.pw.report()
	_, err = io.Copy(io.MultiWriter(fileWriter{f}, x.pw), &ctxReader{ctx: x.ctx, r: r})
	if cerr := f.Close(); err == nil && cerr != nil {
		err = &writeError{cerr}
	}
	if err != nil {
		return err
	}
	if !x.isStage[rel] {
		x.isStage[rel] = true
		x.staged = append(x.staged, rel)
	}
	return nil
}

// commit moves the staged tree into dest.
func (x *extraction) commit() error {
	x.extracted = nil
	for _, rel := range x.dirs {
		dst, err := fsutil.SafeJoin(x.dest, rel)
		if err != nil {
			continue
		}
		if err := os.MkdirAll(dst, 0o755); err != nil { //nolint:gosec // G301: extracted files are user files, same perms as downloads
			return fmt.Errorf("postprocess: create dir: %w", err)
		}
	}
	for _, rel := range x.staged {
		if err := x.ctx.Err(); err != nil {
			return err
		}
		dst, err := fsutil.SafeJoin(x.dest, rel)
		if err != nil {
			return fmt.Errorf("postprocess: %w", err)
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil { //nolint:gosec // G301: extracted files are user files, same perms as downloads
			return fmt.Errorf("postprocess: create dir: %w", err)
		}
		if st, err := os.Lstat(dst); err == nil {
			if !x.overwrite || st.IsDir() {
				dst = fsutil.UniquePath(dst)
			}
		}
		if err := os.Rename(filepath.Join(x.staging, rel), dst); err != nil {
			return fmt.Errorf("postprocess: move extracted file: %w", err)
		}
		x.extracted = append(x.extracted, dst)
	}
	return nil
}

// checkedReader runs check once the underlying reader reaches EOF; check
// may turn the EOF into an error (bad CRC, bad MAC, short data).
type checkedReader struct {
	r     io.Reader
	check func() error
	done  bool
}

func (c *checkedReader) Read(b []byte) (int, error) {
	n, err := c.r.Read(b)
	if errors.Is(err, io.EOF) && !c.done {
		c.done = true
		if cerr := c.check(); cerr != nil {
			return n, cerr
		}
	}
	return n, err
}
