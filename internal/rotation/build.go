package rotation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"admindescargas/internal/accounts"
	"admindescargas/internal/config"
	"admindescargas/internal/hosters"
	"admindescargas/internal/netx"
	"admindescargas/internal/store"
)

// maxWGConfSize caps a wg-quick file.
const maxWGConfSize = 64 << 10

// NewFromConfig builds the Rotator from config.toml (direct, proxies from
// the inline list / files / URLs, Tor, WireGuard groups, router reconnect,
// system VPNs) plus the egresses imported through the API (store table
// egresses, secrets in the SecretStore), and starts the health checker.
//
// st and secrets may be nil (tests). hooks may be empty and set later with
// SetHooks once the engine exists. Problems with individual egresses are
// logged and skipped; only store failures are returned.
func NewFromConfig(ctx context.Context, cfg *config.Config, st *store.Store, secrets accounts.SecretStore, logger *slog.Logger, hooks Hooks) (*Rotator, error) {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	if secrets == nil {
		logger.Warn("rotation: no secret store; imported proxy passwords and WireGuard keys will not survive a restart")
	}
	m, err := New(ctx, SettingsFromConfig(cfg), Options{Logger: logger, Hooks: hooks, Store: st, Secrets: secrets})
	if err != nil {
		return nil, err
	}
	keep := map[string][]string{} // source -> ids
	add := func(e netx.Egress, o EgressOptions) {
		if err := m.Add(ctx, e, o); err != nil {
			logger.Warn("rotation: skip egress", "egress", e.ID(), "err", err)
			_ = e.Close()
			return
		}
		keep[o.Source] = append(keep[o.Source], e.ID())
	}

	ec := cfg.Egress
	hasGlobal := ec.Router.Enabled || len(ec.SystemVPN) > 0
	if ec.Direct || (hasGlobal && cfg.Rotation.GlobalRotation) {
		add(netx.NewDirect(), EgressOptions{Label: "Conexión directa"})
	}

	// Proxies: inline list and files now; URLs in the background.
	var lists []string
	if len(ec.Proxies.List) > 0 {
		lists = append(lists, strings.Join(ec.Proxies.List, "\n"))
	}
	for _, f := range ec.Proxies.Files {
		text, err := netx.ReadProxyFile(f)
		if err != nil {
			logger.Warn("rotation: proxy file", "err", err)
			continue
		}
		lists = append(lists, text)
	}
	for _, text := range lists {
		m.addProxyList(ctx, text, store.EgressSourceConfig, add)
	}

	// Tor (last resort).
	if ec.Tor.Enabled {
		t, err := netx.NewTor(ctx, netx.TorOptions{
			SocksAddr:       ec.Tor.SocksAddr,
			ControlAddr:     ec.Tor.ControlAddr,
			ControlPassword: ec.Tor.ControlPassword,
			TorPath:         ec.Tor.TorPath,
			DataDir:         filepath.Join(cfg.DataDir, "tor"),
			Circuits:        ec.Tor.Circuits,
			Logger:          logger,
		})
		if err != nil {
			logger.Warn("rotation: tor", "err", err)
		} else {
			m.AddCloser(t)
			for _, c := range t.Circuits() {
				add(c, EgressOptions{Source: store.EgressSourceAuto, Address: ec.Tor.SocksAddr, LastResort: true})
			}
		}
	}

	// WireGuard groups.
	for _, g := range ec.WireGuard {
		confs := loadWGGroup(g, logger)
		if len(confs) == 0 {
			continue
		}
		grp, err := netx.NewWireGuardGroup("wg:"+slug(g.Name), g.Name, confs, g.MaxSimultaneous, m.wgOpts)
		if err != nil {
			logger.Warn("rotation: wireguard group", "group", g.Name, "err", err)
			continue
		}
		for _, w := range grp.Egresses() {
			add(w, EgressOptions{Source: store.EgressSourceConfig, Address: w.Config().Endpoint()})
		}
	}

	// Egresses imported through the API.
	if st != nil {
		if err := m.loadImported(ctx); err != nil {
			m.Close()
			return nil, err
		}
	}

	// Global egresses.
	if ec.Router.Enabled {
		r, err := netx.NewRouter(netx.RouterOptions{
			Method: ec.Router.Method, Script: ec.Router.Script,
			WaitAfter: ec.Router.WaitAfter.Duration, Timeout: ec.Router.Timeout.Duration, Logger: logger,
		})
		if err != nil {
			logger.Warn("rotation: router", "err", err)
		} else {
			add(r, EgressOptions{})
		}
	}
	for _, v := range ec.SystemVPN {
		e, err := netx.NewSystemVPN(netx.SystemVPNOptions{
			Name: v.Name, Connect: v.Connect, Rotate: v.Rotate, Disconnect: v.Disconnect,
			Timeout: v.Timeout.Duration, WaitAfter: v.WaitAfter.Duration,
		})
		if err != nil {
			logger.Warn("rotation: system vpn", "err", err)
			continue
		}
		add(e, EgressOptions{})
	}

	if st != nil {
		for _, src := range []string{store.EgressSourceConfig, store.EgressSourceAuto} {
			if _, err := st.DeleteEgressesNotIn(ctx, src, keep[src]); err != nil {
				logger.Warn("rotation: prune egress rows", "err", err)
			}
		}
	}

	if urls := ec.Proxies.URLs; len(urls) > 0 {
		m.wg.Add(1)
		go func() {
			defer m.wg.Done()
			m.fetchProxyURLs(m.bg, urls)
		}()
	}
	m.Start(ctx)
	return m, nil
}

// addProxyList parses text and adds every instance of every entry.
func (m *Rotator) addProxyList(ctx context.Context, text, source string, add func(netx.Egress, EgressOptions)) []string {
	specs, errs := netx.ParseProxyList(text)
	for _, le := range errs {
		m.log.Warn("rotation: bad proxy entry", "line", le.Line, "entry", le.Text, "err", le.Err)
	}
	var ids []string
	for _, sp := range specs {
		for i := range sp.Instances() {
			e, err := netx.NewProxyEgress(sp, i)
			if err != nil {
				m.log.Warn("rotation: proxy", "proxy", sp.Redacted(), "err", err)
				continue
			}
			add(e, EgressOptions{Source: source, Address: sp.Redacted()})
			ids = append(ids, e.ID())
		}
	}
	return ids
}

func (m *Rotator) fetchProxyURLs(ctx context.Context, urls []string) {
	client := netx.NewHTTPClient(netx.NewDirect(), nil)
	defer client.CloseIdleConnections()
	var added []string
	for i, u := range urls {
		fctx, cancel := context.WithTimeout(ctx, time.Minute)
		text, err := netx.FetchProxyList(fctx, client, u)
		cancel()
		if err != nil {
			m.log.Warn("rotation: proxy list URL", "index", i, "err", err)
			continue
		}
		added = append(added, m.addProxyList(ctx, text, store.EgressSourceConfig, func(e netx.Egress, o EgressOptions) {
			if err := m.Add(ctx, e, o); err != nil {
				m.log.Debug("rotation: skip proxy from URL", "egress", e.ID(), "err", err)
			}
		})...)
	}
	m.checkIDs(ctx, added)
}

// checkIDs health-checks the given egresses (used after imports).
func (m *Rotator) checkIDs(ctx context.Context, ids []string) {
	for _, id := range ids {
		select {
		case <-m.stop:
			return
		default:
		}
		m.mu.Lock()
		en, ok := m.entries[id]
		if !ok || en.checking {
			m.mu.Unlock()
			continue
		}
		en.checking = true
		m.mu.Unlock()
		m.checkEntry(ctx, en)
	}
}

func loadWGGroup(g config.WireGuardGroup, log *slog.Logger) []*netx.WGConfig {
	var paths []string
	for _, pat := range g.Confs {
		matches, err := filepath.Glob(pat)
		if err != nil || len(matches) == 0 {
			matches = []string{pat}
		}
		paths = append(paths, matches...)
	}
	slices.Sort(paths)
	paths = slices.Compact(paths)
	var out []*netx.WGConfig
	for _, p := range paths {
		text, err := readLimited(p, maxWGConfSize)
		if err != nil {
			log.Warn("rotation: wireguard conf", "group", g.Name, "err", err)
			continue
		}
		c, err := netx.ParseWGConfig(text)
		if err != nil {
			log.Warn("rotation: wireguard conf", "group", g.Name, "file", filepath.Base(p), "err", err)
			continue
		}
		c.Name = strings.TrimSuffix(filepath.Base(p), filepath.Ext(p))
		out = append(out, c)
	}
	return out
}

func readLimited(path string, limit int64) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return "", err
	}
	if int64(len(b)) > limit {
		return "", fmt.Errorf("%s: larger than %d bytes", filepath.Base(path), limit)
	}
	return string(b), nil
}

// slug turns a user name into an ID fragment: [a-z0-9-], at most 48 chars.
func slug(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		default:
			if !dash && b.Len() > 0 {
				b.WriteByte('-')
				dash = true
			}
		}
		if b.Len() >= 48 {
			break
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		out = "x"
	}
	return out
}

// --- Imported egresses -----------------------------------------------------

type proxySettings struct {
	Scheme   string `json:"scheme"`
	Host     string `json:"host"`
	Port     string `json:"port"`
	Username string `json:"username,omitempty"`
	Sessions int    `json:"sessions,omitempty"`
	Instance int    `json:"instance"`
}

type wgSettings struct {
	Name      string   `json:"name"`
	Endpoint  string   `json:"endpoint"`
	Addresses []string `json:"addresses,omitempty"`
}

const secretPrefix = "egress:"

// loadImported rebuilds the egresses of source "import" from the store.
func (m *Rotator) loadImported(ctx context.Context) error {
	rows, err := m.st.ListEgresses(ctx, store.EgressSourceImport)
	if err != nil {
		return err
	}
	for _, r := range rows {
		if !r.Enabled {
			continue
		}
		e, err := m.buildImported(ctx, r)
		if err != nil {
			m.log.Warn("rotation: imported egress", "egress", r.ID, "err", err)
			continue
		}
		if err := m.Add(ctx, e, EgressOptions{Label: r.Label, Source: store.EgressSourceImport}); err != nil {
			m.log.Warn("rotation: imported egress", "egress", r.ID, "err", err)
			_ = e.Close()
		}
	}
	return nil
}

func (m *Rotator) secret(ctx context.Context, ref string) (accounts.Secret, error) {
	if ref == "" {
		return accounts.Secret{}, nil
	}
	sec, err := m.secrets.Get(ctx, ref)
	if errors.Is(err, accounts.ErrSecretNotFound) {
		return accounts.Secret{}, nil
	}
	return sec, err
}

func (m *Rotator) buildImported(ctx context.Context, r store.EgressRow) (netx.Egress, error) {
	sec, err := m.secret(ctx, r.SecretRef)
	if err != nil {
		return nil, fmt.Errorf("secret: %w", err)
	}
	switch netx.Kind(r.Kind) {
	case netx.KindHTTPProxy, netx.KindSOCKS5:
		var ps proxySettings
		if err := json.Unmarshal([]byte(r.Settings), &ps); err != nil {
			return nil, fmt.Errorf("settings: %w", err)
		}
		spec := netx.ProxySpec{Scheme: ps.Scheme, Host: ps.Host, Port: ps.Port, Username: ps.Username, Password: sec.Password, Sessions: max(1, ps.Sessions)}
		e, err := netx.NewProxyEgress(spec, ps.Instance)
		if err != nil {
			return nil, err
		}
		if e.ID() != r.ID {
			return nil, fmt.Errorf("id mismatch (%s)", e.ID())
		}
		return e, nil
	case netx.KindWireGuard:
		conf := sec.Extra["conf"]
		if conf == "" {
			return nil, errors.New("WireGuard configuration missing from the secret store")
		}
		c, err := netx.ParseWGConfig(conf)
		if err != nil {
			return nil, err
		}
		var ws wgSettings
		_ = json.Unmarshal([]byte(r.Settings), &ws)
		c.Name = ws.Name
		return netx.NewWireGuard(r.ID, c, m.wgOpts)
	}
	return nil, fmt.Errorf("unsupported imported kind %q", r.Kind)
}

// MaxImportEntries caps one ImportProxies call.
const MaxImportEntries = 5000

// ImportResult reports what ImportProxies did.
type ImportResult struct {
	Added      []string         `json:"added"`
	Duplicates []string         `json:"duplicates,omitempty"`
	Errors     []netx.LineError `json:"errors,omitempty"`
}

// ImportProxies parses a proxy list (see netx.ParseProxyList), adds the new
// entries as egresses of source "import" (persisted in the egresses table,
// passwords in the SecretStore) and health-checks them in the background.
func (m *Rotator) ImportProxies(ctx context.Context, text string) (ImportResult, error) {
	if len(text) > netx.MaxProxyListSize {
		return ImportResult{}, fmt.Errorf("rotation: proxy list larger than %d bytes", netx.MaxProxyListSize)
	}
	specs, lineErrs := netx.ParseProxyList(text)
	res := ImportResult{Errors: lineErrs}
	count := 0
	for _, sp := range specs {
		count += sp.Instances()
	}
	if count > MaxImportEntries {
		return res, fmt.Errorf("rotation: too many proxies in one import (%d > %d)", count, MaxImportEntries)
	}
	for _, sp := range specs {
		var fresh []int
		for i := range sp.Instances() {
			id := sp.InstanceID(i)
			if m.Get(id) != nil {
				res.Duplicates = append(res.Duplicates, id)
				continue
			}
			fresh = append(fresh, i)
		}
		if len(fresh) == 0 {
			continue
		}
		ref := ""
		if sp.Password != "" {
			ref = secretPrefix + sp.BaseID()
			if err := m.secrets.Set(ctx, ref, accounts.Secret{Password: sp.Password}); err != nil {
				return res, fmt.Errorf("rotation: store proxy password: %w", err)
			}
		}
		for _, i := range fresh {
			e, err := netx.NewProxyEgress(sp, i)
			if err != nil {
				res.Errors = append(res.Errors, netx.LineError{Text: sp.Redacted(), Err: err.Error()})
				continue
			}
			settings, _ := json.Marshal(proxySettings{Scheme: sp.Scheme, Host: sp.Host, Port: sp.Port, Username: sp.Username, Sessions: sp.Sessions, Instance: i})
			if m.st != nil {
				row := store.EgressRow{
					ID: e.ID(), Kind: string(e.Kind()), Label: sp.Redacted(), Source: store.EgressSourceImport,
					Address: sp.Redacted(), Settings: string(settings), SecretRef: ref, Enabled: true, Healthy: true,
				}
				if err := m.st.UpsertEgress(ctx, row); err != nil {
					return res, err
				}
			}
			if err := m.Add(ctx, e, EgressOptions{Source: store.EgressSourceImport, Address: sp.Redacted()}); err != nil {
				res.Duplicates = append(res.Duplicates, e.ID())
				continue
			}
			res.Added = append(res.Added, e.ID())
		}
	}
	m.checkInBackground(res.Added)
	return res, nil
}

func (m *Rotator) checkInBackground(ids []string) {
	if len(ids) == 0 {
		return
	}
	m.mu.Lock()
	closed := m.closed
	if !closed {
		m.wg.Add(1)
	}
	m.mu.Unlock()
	if closed {
		return
	}
	go func() {
		defer m.wg.Done()
		m.checkIDs(m.bg, ids)
	}()
}

// AddWireGuard imports a wg-quick configuration as a new egress (ID
// "wireguard:<slug of name>"). The whole file is kept in the SecretStore
// (it holds the private key); the egresses row only has non-secret fields.
// The tunnel is health-checked in the background.
func (m *Rotator) AddWireGuard(ctx context.Context, name, conf string) (EgressStatus, error) {
	if len(conf) > maxWGConfSize {
		return EgressStatus{}, fmt.Errorf("rotation: WireGuard configuration larger than %d bytes", maxWGConfSize)
	}
	c, err := netx.ParseWGConfig(conf)
	if err != nil {
		return EgressStatus{}, err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = c.Endpoint()
	}
	c.Name = name
	id := "wireguard:" + slug(name)
	if m.Get(id) != nil {
		return EgressStatus{}, fmt.Errorf("%w: %s", ErrExists, id)
	}
	w, err := netx.NewWireGuard(id, c, m.wgOpts)
	if err != nil {
		return EgressStatus{}, err
	}
	ref := secretPrefix + id
	if err := m.secrets.Set(ctx, ref, accounts.Secret{Extra: map[string]string{"conf": conf}}); err != nil {
		return EgressStatus{}, fmt.Errorf("rotation: store WireGuard configuration: %w", err)
	}
	if m.st != nil {
		var addrs []string
		for _, a := range c.Addresses {
			addrs = append(addrs, a.String())
		}
		settings, _ := json.Marshal(wgSettings{Name: name, Endpoint: c.Endpoint(), Addresses: addrs})
		row := store.EgressRow{
			ID: id, Kind: string(netx.KindWireGuard), Label: name, Source: store.EgressSourceImport,
			Address: c.Endpoint(), Settings: string(settings), SecretRef: ref, Enabled: true, Healthy: true,
		}
		if err := m.st.UpsertEgress(ctx, row); err != nil {
			_ = m.secrets.Delete(ctx, ref)
			return EgressStatus{}, err
		}
	}
	if err := m.Add(ctx, w, EgressOptions{Label: name, Source: store.EgressSourceImport}); err != nil {
		return EgressStatus{}, err
	}
	m.checkInBackground([]string{id})
	return m.Status(id)
}

// RemoveEgress deletes an imported egress (row, secret, quota marks).
// Egresses defined in config.toml cannot be removed from the API.
func (m *Rotator) RemoveEgress(ctx context.Context, id string) error {
	m.mu.Lock()
	en, ok := m.entries[id]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("%w: %s", ErrUnknownEgress, id)
	}
	if en.source != store.EgressSourceImport {
		m.mu.Unlock()
		return ErrNotImported
	}
	m.mu.Unlock()
	m.remove(id)
	if err := m.book.ClearSubject(ctx, hosters.ScopeIP, id); err != nil {
		m.log.Warn("rotation: clear quota marks", "egress", id, "err", err)
	}
	if m.st == nil {
		return nil
	}
	row, err := m.st.GetEgress(ctx, id)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	if err := m.st.DeleteEgress(ctx, id); err != nil {
		return err
	}
	if row.SecretRef != "" {
		rows, err := m.st.ListEgresses(ctx, store.EgressSourceImport)
		if err != nil {
			return err
		}
		shared := slices.ContainsFunc(rows, func(r store.EgressRow) bool { return r.SecretRef == row.SecretRef })
		if !shared {
			if err := m.secrets.Delete(ctx, row.SecretRef); err != nil {
				return fmt.Errorf("rotation: delete secret: %w", err)
			}
		}
	}
	m.emit(id)
	return nil
}
