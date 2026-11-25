package postprocess

import (
	"context"
	"crypto/md5"  //nolint:gosec // hoster-advertised checksum, not security
	"crypto/sha1" //nolint:gosec // hoster-advertised checksum, not security
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"strings"

	"admindescargas/internal/hosters"
)

// builtinHash returns the hash for the built-in algorithms.
func builtinHash(algo string) hash.Hash {
	switch algo {
	case hosters.AlgoSHA256:
		return sha256.New()
	case hosters.AlgoSHA1:
		return sha1.New() //nolint:gosec
	case hosters.AlgoMD5:
		return md5.New() //nolint:gosec
	case hosters.AlgoCRC32:
		return crc32.NewIEEE()
	}
	return nil
}

// verify checks it.Path against it.Checksum. It returns false (and no
// error) when the algorithm is unknown.
func (p *Processor) verify(ctx context.Context, it Item, algo string) (bool, error) {
	value := strings.TrimSpace(it.Checksum.Value)
	if fn := p.verifier(algo); fn != nil {
		if err := fn(ctx, it.Path, value); err != nil {
			return false, fmt.Errorf("postprocess: verify %s of %q: %w", algo, filepath.Base(it.Path), err)
		}
		return true, nil
	}
	h := builtinHash(algo)
	if h == nil {
		p.log.Warn("postprocess: unknown checksum algorithm, skipping verification", "algo", algo,
			"file", filepath.Base(it.Path))
		return false, nil
	}
	got, err := hashFile(ctx, it.Path, h, it.Progress)
	if err != nil {
		return false, err
	}
	want := strings.ToLower(value)
	if algo == hosters.AlgoCRC32 {
		want = strings.TrimPrefix(want, "0x")
		for len(want) < 8 {
			want = "0" + want
		}
	}
	if got != want {
		return false, fmt.Errorf("%w: %s of %q is %s, expected %s", ErrChecksumMismatch, algo,
			filepath.Base(it.Path), got, want)
	}
	p.log.Debug("postprocess: checksum ok", "algo", algo, "file", filepath.Base(it.Path))
	return true, nil
}

// hashFile streams path through h and returns the lowercase hex digest.
func hashFile(ctx context.Context, path string, h hash.Hash, progress func(Progress)) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("postprocess: open %q: %w", filepath.Base(path), err)
	}
	defer f.Close()
	total := int64(-1)
	if st, err := f.Stat(); err == nil {
		total = st.Size()
	}
	pr := &progressWriter{progress: progress, p: Progress{Stage: StageVerify, File: filepath.Base(path), Total: total}}
	pr.report()
	if _, err := io.Copy(io.MultiWriter(h, pr), &ctxReader{ctx: ctx, r: f}); err != nil {
		return "", fmt.Errorf("postprocess: hash %q: %w", filepath.Base(path), err)
	}
	pr.flush()
	return hex.EncodeToString(h.Sum(nil)), nil
}

// ctxReader fails reads once ctx is done.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c *ctxReader) Read(b []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(b)
}

// progressWriter counts bytes and calls progress at most every
// progressStep bytes.
type progressWriter struct {
	progress func(Progress)
	p        Progress
	last     int64
}

func (w *progressWriter) Write(b []byte) (int, error) {
	w.p.Done += int64(len(b))
	if w.p.Done-w.last >= progressStep {
		w.report()
	}
	return len(b), nil
}

func (w *progressWriter) report() {
	w.last = w.p.Done
	if w.progress != nil {
		w.progress(w.p)
	}
}

func (w *progressWriter) flush() {
	if w.p.Done != w.last {
		w.report()
	}
}
