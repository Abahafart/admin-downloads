// Package clipboard implements the desktop clipboard monitor (PLAN.md
// section 9.2): it watches the clipboard, extracts every URL of the copied
// text, keeps the ones a hoster plugin recognizes and hands them to the
// LinkGrabber according to config.Clipboard (ask / add / linkgrabber).
//
// The URL extractor, the hoster matcher and the "already queued" check are
// injected, so this package has no dependency on the engine or the
// linkgrabber implementation. Sources (golang.design/x/clipboard,
// `wl-paste --watch`) are pure Go and build with CGO_ENABLED=0.
//
// Privacy: the copied text is only processed in memory. It is never logged,
// stored or sent anywhere; only the matched links leave this package, and
// only through the Sink. The monitor remembers a hash of the last text and
// the recently seen links (bounded, in memory) to drop duplicates.
package clipboard

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"

	"admindescargas/internal/config"
)

// Clip is one clipboard change.
type Clip struct {
	Text string
	// Sensitive is set when a password manager marked the copy; such clips
	// are dropped without looking at them.
	Sensitive bool
}

// Source delivers clipboard changes until ctx is done, then closes the
// channel.
type Source interface {
	// Name identifies the backend ("native", "wl-paste", "fake") for logs
	// and the UI.
	Name() string
	Watch(ctx context.Context) (<-chan Clip, error)
}

// Extractor returns every URL found in free text (linkgrabber.ExtractURLs).
type Extractor func(text string) []string

// Matcher returns the name of the hoster plugin that handles link. Catch-all
// plugins (generic HTTP) must not match, or every copied URL would be
// offered; RegistryMatcher does that.
type Matcher func(link string) (hoster string, ok bool)

// KnownFilter returns the subset of links that are NOT already in the queue,
// the LinkGrabber or the history. It may be nil.
type KnownFilter func(ctx context.Context, links []string) ([]string, error)

// Sink receives the accepted links.
type Sink interface {
	// AddToQueue adds links straight to the download queue (action "add":
	// LinkGrabber with auto-confirm).
	AddToQueue(ctx context.Context, links []string) error
	// AddToGrabber leaves links in the LinkGrabber (action "linkgrabber",
	// and "ask" when the user accepts).
	AddToGrabber(ctx context.Context, links []string) error
}

// Prompter asks the user what to do with a detection (action "ask"), e.g.
// a notification with "Añadir" / "Ignorar". It returns true to add. It
// should give up (false) after a reasonable timeout.
type Prompter interface {
	Ask(ctx context.Context, d Detection) (bool, error)
}

// Detection is a batch of supported links found in one copy.
type Detection struct {
	// Links are the accepted links, in order of appearance, deduplicated.
	Links []string
	// Hosters are the distinct hoster names, in order of first appearance.
	Hosters []string
	// Counts is the number of links per hoster.
	Counts map[string]int
}

// Summary is the user-facing text of the detection, e.g.
// "3 enlaces de MEGA detectados" or "4 enlaces detectados (MEGA, MediaFire)".
func (d Detection) Summary() string {
	n := len(d.Links)
	noun := "enlaces"
	verb := "detectados"
	if n == 1 {
		noun, verb = "enlace", "detectado"
	}
	switch len(d.Hosters) {
	case 0:
		return fmt.Sprintf("%d %s %s", n, noun, verb)
	case 1:
		return fmt.Sprintf("%d %s de %s %s", n, noun, DisplayName(d.Hosters[0]), verb)
	default:
		names := make([]string, len(d.Hosters))
		for i, h := range d.Hosters {
			names[i] = DisplayName(h)
		}
		return fmt.Sprintf("%d %s %s (%s)", n, noun, verb, strings.Join(names, ", "))
	}
}

// MaxTextSize is the largest clipboard text that is inspected; bigger
// copies (a whole document, a log file) are ignored.
const MaxTextSize = 1 << 20

// defaultRemember bounds the set of recently seen links.
const defaultRemember = 2000

// Options configures a Monitor.
type Options struct {
	Source   Source    // required
	Extract  Extractor // required (linkgrabber.ExtractURLs; SimpleExtract as fallback)
	Match    Matcher   // required (RegistryMatcher)
	Known    KnownFilter
	Sink     Sink     // required
	Prompter Prompter // required when Config.Action is "ask"
	Config   config.ClipboardConfig
	Logger   *slog.Logger
	// Remember is the number of recently seen links kept to drop
	// duplicates (default 2000).
	Remember int
	// OnDetect, if set, is called (synchronously) for every detection
	// before it is delivered (tests, tray counters).
	OnDetect func(Detection)
}

// Monitor watches a Source and delivers detections. Use New.
type Monitor struct {
	src      Source
	extract  Extractor
	match    Matcher
	known    KnownFilter
	sink     Sink
	prompter Prompter
	log      *slog.Logger
	onDetect func(Detection)

	mu       sync.Mutex
	cfg      config.ClipboardConfig
	enabled  bool
	lastHash [32]byte
	hasLast  bool
	seen     *lru

	wg sync.WaitGroup
}

// New validates opts and returns a Monitor. The monitor starts enabled when
// opts.Config.Enabled is set.
func New(opts Options) (*Monitor, error) {
	var errs []error
	if opts.Source == nil {
		errs = append(errs, errors.New("nil Source"))
	}
	if opts.Extract == nil {
		errs = append(errs, errors.New("nil Extract"))
	}
	if opts.Match == nil {
		errs = append(errs, errors.New("nil Match"))
	}
	if opts.Sink == nil {
		errs = append(errs, errors.New("nil Sink"))
	}
	if err := errors.Join(errs...); err != nil {
		return nil, fmt.Errorf("clipboard: %w", err)
	}
	log := opts.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	remember := opts.Remember
	if remember <= 0 {
		remember = defaultRemember
	}
	return &Monitor{
		src:      opts.Source,
		extract:  opts.Extract,
		match:    opts.Match,
		known:    opts.Known,
		sink:     opts.Sink,
		prompter: opts.Prompter,
		log:      log.With("component", "clipboard", "source", opts.Source.Name()),
		onDetect: opts.OnDetect,
		cfg:      opts.Config,
		enabled:  opts.Config.Enabled,
		seen:     newLRU(remember),
	}, nil
}

// SourceName returns the backend name.
func (m *Monitor) SourceName() string { return m.src.Name() }

// Enabled reports whether the monitor processes clipboard changes.
func (m *Monitor) Enabled() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.enabled
}

// SetEnabled pauses (false) or resumes (true) the monitor. Changes made
// while paused are ignored, also after resuming.
func (m *Monitor) SetEnabled(on bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.enabled = on
}

// Config returns the current configuration.
func (m *Monitor) Config() config.ClipboardConfig {
	m.mu.Lock()
	defer m.mu.Unlock()
	c := m.cfg
	c.Hosters = slices.Clone(c.Hosters)
	return c
}

// SetConfig replaces the configuration (action, hoster list...). Enabled
// is applied too.
func (m *Monitor) SetConfig(c config.ClipboardConfig) {
	c.Hosters = slices.Clone(c.Hosters)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cfg = c
	m.enabled = c.Enabled
}

// Run watches the source until ctx is done. Detections in "ask" mode are
// delivered in the background (the prompt may take a while); Run waits for
// them before returning.
func (m *Monitor) Run(ctx context.Context) error {
	ch, err := m.src.Watch(ctx)
	if err != nil {
		return fmt.Errorf("clipboard: watch: %w", err)
	}
	m.log.Info("clipboard monitor running")
	defer m.wg.Wait()
	for {
		select {
		case <-ctx.Done():
			return nil
		case clip, ok := <-ch:
			if !ok {
				if ctx.Err() != nil {
					return nil
				}
				return errors.New("clipboard: source closed")
			}
			d, ok := m.Detect(ctx, clip)
			if !ok {
				continue
			}
			if m.action() == config.ClipboardAsk {
				m.wg.Add(1)
				go func() {
					defer m.wg.Done()
					m.deliver(ctx, d)
				}()
				continue
			}
			m.deliver(ctx, d)
		}
	}
}

func (m *Monitor) action() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cfg.Action
}

// Detect inspects one clipboard change and returns the new supported links
// in it. It applies, in order: paused monitor, sensitive clips, size limit,
// same text as last time, URL extraction, hoster matching and the hoster
// allow-list, links seen recently and, if configured, links already known
// to the core. Accepted links are remembered as seen.
func (m *Monitor) Detect(ctx context.Context, clip Clip) (Detection, bool) {
	m.mu.Lock()
	if !m.enabled || clip.Sensitive || clip.Text == "" || len(clip.Text) > MaxTextSize {
		m.mu.Unlock()
		return Detection{}, false
	}
	h := sha256.Sum256([]byte(clip.Text))
	if m.hasLast && h == m.lastHash {
		m.mu.Unlock()
		return Detection{}, false
	}
	m.lastHash, m.hasLast = h, true
	allowed := m.cfg.Hosters
	ignoreKnown := m.cfg.IgnoreKnown
	m.mu.Unlock()

	type hit struct{ link, hoster string }
	var hits []hit
	dup := map[string]bool{}
	for _, raw := range m.extract(clip.Text) {
		link := strings.TrimSpace(raw)
		if link == "" || dup[link] {
			continue
		}
		dup[link] = true
		name, ok := m.match(link)
		if !ok || len(allowed) > 0 && !slices.Contains(allowed, name) {
			continue
		}
		hits = append(hits, hit{link, name})
	}
	if len(hits) == 0 {
		return Detection{}, false
	}

	m.mu.Lock()
	fresh := hits[:0]
	for _, x := range hits {
		if !m.seen.contains(x.link) {
			fresh = append(fresh, x)
		}
	}
	m.mu.Unlock()
	if len(fresh) == 0 {
		return Detection{}, false
	}

	if ignoreKnown && m.known != nil {
		links := make([]string, len(fresh))
		for i, x := range fresh {
			links[i] = x.link
		}
		unknown, err := m.known(ctx, links)
		if err != nil {
			// Better to offer a duplicate than to lose links.
			m.log.Warn("clipboard: known-links check failed", "err", err)
		} else {
			keep := map[string]bool{}
			for _, l := range unknown {
				keep[l] = true
			}
			kept := fresh[:0]
			for _, x := range fresh {
				if keep[x.link] {
					kept = append(kept, x)
				} else {
					m.remember(x.link) // known: do not ask again
				}
			}
			fresh = kept
		}
		if len(fresh) == 0 {
			return Detection{}, false
		}
	}

	d := Detection{Counts: map[string]int{}}
	for _, x := range fresh {
		d.Links = append(d.Links, x.link)
		if d.Counts[x.hoster] == 0 {
			d.Hosters = append(d.Hosters, x.hoster)
		}
		d.Counts[x.hoster]++
		m.remember(x.link)
	}
	m.log.Debug("clipboard: links detected", "links", len(d.Links), "hosters", d.Hosters)
	if m.onDetect != nil {
		m.onDetect(d)
	}
	return d, true
}

func (m *Monitor) remember(link string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.seen.add(link)
}

func (m *Monitor) forget(links []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, l := range links {
		m.seen.remove(l)
	}
	m.hasLast = false // copying the same text again retries
}

// Handle delivers d according to the configured action. On a delivery
// error the links are forgotten so copying them again retries.
func (m *Monitor) Handle(ctx context.Context, d Detection) error {
	var err error
	switch m.action() {
	case config.ClipboardAdd:
		err = m.sink.AddToQueue(ctx, d.Links)
	case config.ClipboardLinkGrabber:
		err = m.sink.AddToGrabber(ctx, d.Links)
	default: // ask
		if m.prompter == nil {
			err = errors.New("no prompter configured for action ask")
			break
		}
		var yes bool
		yes, err = m.prompter.Ask(ctx, d)
		if err == nil && yes {
			err = m.sink.AddToGrabber(ctx, d.Links)
		}
	}
	if err != nil {
		m.forget(d.Links)
		return fmt.Errorf("clipboard: deliver: %w", err)
	}
	return nil
}

func (m *Monitor) deliver(ctx context.Context, d Detection) {
	if err := m.Handle(ctx, d); err != nil && ctx.Err() == nil {
		m.log.Warn("clipboard: could not deliver links", "links", len(d.Links), "err", err)
	}
}

// lru is a small bounded set with least-recently-added eviction.
type lru struct {
	max   int
	order []string
	set   map[string]struct{}
}

func newLRU(max int) *lru { return &lru{max: max, set: map[string]struct{}{}} }

func (l *lru) contains(k string) bool { _, ok := l.set[k]; return ok }

func (l *lru) add(k string) {
	if l.contains(k) {
		return
	}
	l.set[k] = struct{}{}
	l.order = append(l.order, k)
	if len(l.order) > l.max {
		old := l.order[0]
		l.order = l.order[1:]
		delete(l.set, old)
	}
}

func (l *lru) remove(k string) {
	if !l.contains(k) {
		return
	}
	delete(l.set, k)
	if i := slices.Index(l.order, k); i >= 0 {
		l.order = slices.Delete(l.order, i, i+1)
	}
}
