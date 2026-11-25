package hosters

import (
	"fmt"
	"net/url"
	"slices"
	"strings"
	"sync"

	"admindescargas/internal/config"
)

// Factory builds a plugin from its [hosters.<name>] settings.
type Factory func(cfg config.HosterConfig) (Hoster, error)

var (
	factoriesMu sync.Mutex
	factories   = map[string]Factory{}
)

// RegisterFactory registers a plugin factory; call it from the plugin's init.
// It panics on duplicate names (a programming error).
func RegisterFactory(name string, f Factory) {
	factoriesMu.Lock()
	defer factoriesMu.Unlock()
	if _, dup := factories[name]; dup {
		panic("hosters: duplicate factory " + name)
	}
	factories[name] = f
}

// FactoryNames returns the registered factory names, sorted.
func FactoryNames() []string {
	factoriesMu.Lock()
	defer factoriesMu.Unlock()
	names := make([]string, 0, len(factories))
	for n := range factories {
		names = append(names, n)
	}
	slices.Sort(names)
	return names
}

// NewRegistryFromFactories instantiates every registered factory whose
// config is not disabled. cfgs may be nil.
func NewRegistryFromFactories(cfgs map[string]config.HosterConfig) (*Registry, error) {
	r := NewRegistry()
	for _, name := range FactoryNames() {
		cfg := cfgs[name]
		if cfg.Disabled {
			continue
		}
		factoriesMu.Lock()
		f := factories[name]
		factoriesMu.Unlock()
		h, err := f(cfg)
		if err != nil {
			return nil, fmt.Errorf("hosters: init %s: %w", name, err)
		}
		if err := r.Register(h); err != nil {
			return nil, err
		}
	}
	return r, nil
}

// Registry finds the plugin for a link. It is safe for concurrent use.
type Registry struct {
	mu       sync.RWMutex
	specific []Hoster
	fallback []Hoster
	byName   map[string]Hoster
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry { return &Registry{byName: map[string]Hoster{}} }

// Register adds h. Plugins with Caps().Fallback are matched last.
func (r *Registry) Register(h Hoster) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.byName[h.Name()]; dup {
		return fmt.Errorf("hosters: %q already registered", h.Name())
	}
	r.byName[h.Name()] = h
	if h.Caps().Fallback {
		r.fallback = append(r.fallback, h)
	} else {
		r.specific = append(r.specific, h)
	}
	return nil
}

// Match returns the plugin for u (specific plugins first, then fallbacks),
// or nil if none matches.
func (r *Registry) Match(u *url.URL) Hoster {
	if u == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, list := range [][]Hoster{r.specific, r.fallback} {
		for _, h := range list {
			if h.Match(u) {
				return h
			}
		}
	}
	return nil
}

// MatchString parses raw and calls Match. It returns nil for unparsable or
// non-absolute URLs.
func (r *Registry) MatchString(raw string) Hoster {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil
	}
	return r.Match(u)
}

// Get returns the plugin named name, or nil.
func (r *Registry) Get(name string) Hoster {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.byName[name]
}

// All returns every registered plugin (specific first, then fallbacks).
func (r *Registry) All() []Hoster {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append(slices.Clone(r.specific), r.fallback...)
}
