package postprocess

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"admindescargas/internal/config"
	"admindescargas/internal/hosters"
)

// Sentinel errors (wrapped with context by Process / Run).
var (
	// ErrChecksumMismatch means the downloaded file does not match the
	// checksum advertised by the hoster.
	ErrChecksumMismatch = errors.New("postprocess: checksum mismatch")
	// ErrWrongPassword means none of the configured passwords opened the
	// archive.
	ErrWrongPassword = errors.New("postprocess: wrong archive password")
	// ErrPasswordRequired means the archive is encrypted and no password is
	// configured.
	ErrPasswordRequired = errors.New("postprocess: archive is encrypted and no password is configured")
	// ErrUnsupported is returned for archive variants that cannot be
	// extracted (e.g. PKWARE spanned .z01 zips).
	ErrUnsupported = errors.New("postprocess: unsupported archive")
	// ErrCorrupt means the archive is damaged or not what its extension
	// says.
	ErrCorrupt = errors.New("postprocess: corrupt archive")
)

// Progress stages.
const (
	StageVerify  = "verify"
	StageExtract = "extract"
)

// progressStep is the minimum number of bytes between two progress calls.
const progressStep = 1 << 20

// Progress reports the advance of a step.
type Progress struct {
	// Stage is StageVerify or StageExtract.
	Stage string
	// File is the file being hashed or the archive entry being extracted.
	File string
	// Done and Total are bytes (Total is -1 if unknown).
	Done, Total int64
}

// Options override the [postprocess] config for one item.
type Options struct {
	// Verify, Extract and DeleteArchives override the config when non-nil.
	Verify         *bool
	Extract        *bool
	DeleteArchives *bool
	// ExtractSubdir extracts into <PackageDir>/<archive base name>.
	ExtractSubdir bool
	// Overwrite replaces existing files; by default a conflicting name is
	// saved as "name (1).ext".
	Overwrite bool
}

// Item is one finished download handed to Process.
type Item struct {
	// Path is the final path of the downloaded file.
	Path string
	// Checksum to verify (nil = nothing to verify).
	Checksum *hosters.Checksum
	// PackageDir is the extraction destination (default: dir of Path).
	PackageDir string
	// Passwords are tried before the configured ones.
	Passwords []string
	Options   Options
	// Progress, if set, is called from the calling goroutine.
	Progress func(Progress)
}

// Result describes what Run did.
type Result struct {
	// Verified is true when a checksum was checked and matched.
	Verified bool
	// Algo is the checksum algorithm verified.
	Algo string
	// Archive is the first volume of the archive set ("" if Path is not an
	// archive or extraction is disabled).
	Archive string
	// Waiting is true when Path belongs to a multi-volume set that is not
	// complete yet; call Process again when more volumes arrive.
	Waiting bool
	// Extracted lists the paths written.
	Extracted []string
	// Rejected lists entry names skipped as unsafe (traversal, links...).
	Rejected []string
	// Deleted lists the archive volumes removed after extraction.
	Deleted []string
}

// VerifierFunc verifies path against value (as advertised by the hoster);
// it returns an error wrapping ErrChecksumMismatch on mismatch.
type VerifierFunc func(ctx context.Context, path, value string) error

// Processor runs checksum verification and extraction. It is safe for
// concurrent use.
type Processor struct {
	cfg config.PostProcessConfig
	log *slog.Logger

	mu        sync.Mutex
	verifiers map[string]VerifierFunc
	setLocks  map[string]*sync.Mutex
	// done maps every volume of an extracted set to its first volume.
	done map[string]string
}

// New returns a Processor using cfg; a nil logger means slog.Default().
func New(cfg config.PostProcessConfig, log *slog.Logger) *Processor {
	if log == nil {
		log = slog.Default()
	}
	return &Processor{
		cfg:       cfg,
		log:       log,
		verifiers: map[string]VerifierFunc{},
		setLocks:  map[string]*sync.Mutex{},
		done:      map[string]string{},
	}
}

// RegisterVerifier installs a checksum verifier for algo (case-insensitive),
// overriding the built-in one if any. The MEGA plugin's MAC verifier is
// registered this way by internal/app.
func (p *Processor) RegisterVerifier(algo string, fn func(ctx context.Context, path, value string) error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	algo = strings.ToLower(strings.TrimSpace(algo))
	if fn == nil {
		delete(p.verifiers, algo)
		return
	}
	p.verifiers[algo] = fn
}

func (p *Processor) verifier(algo string) VerifierFunc {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.verifiers[algo]
}

// Process runs Run and drops the Result.
func (p *Processor) Process(ctx context.Context, it Item) error {
	_, err := p.Run(ctx, it)
	return err
}

// Run verifies the checksum of it.Path (if any and enabled) and extracts it
// when it is an archive (if enabled).
func (p *Processor) Run(ctx context.Context, it Item) (*Result, error) {
	res := &Result{}
	if it.Path == "" {
		return res, errors.New("postprocess: empty path")
	}
	path, err := filepath.Abs(it.Path)
	if err != nil {
		return res, fmt.Errorf("postprocess: %w", err)
	}
	it.Path = path

	// A volume of a set we already extracted (and maybe deleted).
	if first, ok := p.extractedSet(path); ok {
		res.Archive = first
		return res, nil
	}

	if err := ctx.Err(); err != nil {
		return res, err
	}

	if boolOr(it.Options.Verify, p.cfg.VerifyChecksums) && it.Checksum != nil && it.Checksum.Value != "" {
		algo := strings.ToLower(strings.TrimSpace(it.Checksum.Algo))
		ok, err := p.verify(ctx, it, algo)
		if err != nil {
			return res, err
		}
		if ok {
			res.Verified, res.Algo = true, algo
		}
	}

	if !boolOr(it.Options.Extract, p.cfg.Extract) {
		return res, nil
	}
	set, err := detectSet(path)
	if err != nil {
		return res, err
	}
	if set == nil {
		return res, nil // not an archive
	}
	res.Archive = set.first
	return res, p.extractSet(ctx, it, set, res)
}

func (p *Processor) extractedSet(path string) (string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	first, ok := p.done[path]
	return first, ok
}

func (p *Processor) lockSet(first string) func() {
	p.mu.Lock()
	l, ok := p.setLocks[first]
	if !ok {
		l = &sync.Mutex{}
		p.setLocks[first] = l
	}
	p.mu.Unlock()
	l.Lock()
	return l.Unlock
}

func (p *Processor) markDone(set *archiveSet) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, v := range set.volumes {
		p.done[v] = set.first
	}
	p.done[set.first] = set.first
}

func (p *Processor) extractSet(ctx context.Context, it Item, set *archiveSet, res *Result) error {
	unlock := p.lockSet(set.first)
	defer unlock()
	if _, ok := p.extractedSet(set.first); ok {
		return nil
	}
	if set.unsupported != nil {
		return set.unsupported
	}

	complete, err := set.complete()
	if err != nil {
		return err
	}
	if !complete {
		res.Waiting = true
		p.log.Info("postprocess: archive set incomplete, waiting for more volumes",
			"archive", filepath.Base(set.first), "volumes", len(set.volumes))
		return nil
	}

	dest := it.PackageDir
	if dest == "" {
		dest = filepath.Dir(set.first)
	}
	if it.Options.ExtractSubdir {
		dest = filepath.Join(dest, subdirName(set.first))
	}
	dest, err = filepath.Abs(dest)
	if err != nil {
		return fmt.Errorf("postprocess: %w", err)
	}

	passwords := candidatePasswords(it.Passwords, p.cfg.Passwords)
	x := &extraction{
		ctx:       ctx,
		log:       p.log,
		set:       set,
		dest:      dest,
		overwrite: it.Options.Overwrite,
		progress:  it.Progress,
	}
	if err := x.run(passwords); err != nil {
		if errors.Is(err, errMissingVolume) {
			res.Waiting = true
			p.log.Info("postprocess: archive volume missing, waiting for more volumes",
				"archive", filepath.Base(set.first))
			return nil
		}
		return err
	}
	res.Extracted = x.extracted
	res.Rejected = x.rejected
	p.markDone(set)
	p.log.Info("postprocess: archive extracted", "archive", filepath.Base(set.first),
		"format", set.format, "files", len(x.extracted), "rejected", len(x.rejected))

	if boolOr(it.Options.DeleteArchives, p.cfg.DeleteArchives) {
		for _, v := range set.volumes {
			if err := os.Remove(v); err != nil && !errors.Is(err, os.ErrNotExist) {
				p.log.Warn("postprocess: cannot delete archive volume", "volume", filepath.Base(v), "err", err)
				continue
			}
			res.Deleted = append(res.Deleted, v)
		}
	}
	return nil
}

// candidatePasswords merges item and config passwords, dropping empty and
// duplicate values while keeping the order.
func candidatePasswords(lists ...[]string) []string {
	seen := map[string]bool{}
	var out []string
	for _, l := range lists {
		for _, pw := range l {
			if pw == "" || seen[pw] {
				continue
			}
			seen[pw] = true
			out = append(out, pw)
		}
	}
	return out
}

func boolOr(v *bool, def bool) bool {
	if v != nil {
		return *v
	}
	return def
}
