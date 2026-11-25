package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"admindescargas/internal/accounts"
	"admindescargas/internal/api"
	"admindescargas/internal/config"
	"admindescargas/internal/core"
	"admindescargas/internal/hosters"
	"admindescargas/internal/hosters/mega"
	"admindescargas/internal/postprocess"
	"admindescargas/internal/rotation"
)

// This file adapts the packages to each other's interfaces (PLAN.md
// section 2): accounts.Manager -> core.AccountPicker / api.AccountService /
// api.SecretsService, postprocess.Processor -> core.PostProcessor,
// accounts.GoogleOAuth -> api.GoogleOAuth and rotation.Admin ->
// api.EgressService.

// --- accounts -> engine ---------------------------------------------------

// accountPicker implements core.AccountPicker and core.AccountLookup.
type accountPicker struct {
	m   *accounts.Manager
	log *slog.Logger
}

var (
	_ core.AccountPicker = accountPicker{}
	_ core.AccountLookup = accountPicker{}
)

// Pick returns (nil, nil) when no account is configured / usable (the
// engine downloads anonymously) and the *accounts.NoAccountError when every
// account is only waiting for its quota (the engine then waits if the
// hoster needs an account).
func (p accountPicker) Pick(ctx context.Context, hoster string) (*accounts.Account, error) {
	a, err := p.m.Pick(ctx, hoster)
	var nae *accounts.NoAccountError
	if errors.As(err, &nae) && nae.Until.IsZero() {
		return nil, nil
	}
	return a, err
}

func (p accountPicker) ReportQuota(ctx context.Context, id string, retryAfter time.Duration) {
	if err := p.m.ReportQuota(ctx, id, retryAfter); err != nil && ctx.Err() == nil {
		p.log.Warn("report account quota", "account", id, "err", err)
	}
}

func (p accountPicker) Get(ctx context.Context, id string) (*accounts.Account, error) {
	return p.m.Get(ctx, id)
}

// accountChecker implements accounts.Checker with the plugin's
// hosters.AccountChecker over a session on an egress from the rotation
// manager (pinned: checks never rotate the IP).
func accountChecker(reg *hosters.Registry, rot func() rotation.Manager, secrets accounts.SecretStore, log *slog.Logger) accounts.Checker {
	return func(ctx context.Context, acct *accounts.Account) (*accounts.CheckResult, error) {
		h := reg.Get(acct.Hoster)
		if h == nil {
			return nil, fmt.Errorf("el plugin %q no está disponible: %w", acct.Hoster, accounts.ErrNoChecker)
		}
		ac, ok := h.(hosters.AccountChecker)
		if !ok {
			return nil, accounts.ErrNoChecker
		}
		m := rot()
		if m == nil {
			return nil, errors.New("la red todavía no está lista")
		}
		eg, err := m.Acquire(ctx, rotation.Request{Hoster: acct.Hoster, AccountID: acct.ID, QuotaScope: hosters.ScopeAccount})
		if err != nil {
			return nil, fmt.Errorf("sin salida de red: %w", err)
		}
		defer m.Release(acct.Hoster, eg.ID())
		s := hosters.NewSession(eg, acct, nil)
		s.Hoster = acct.Hoster
		s.Secrets = secrets
		s.Log = log.With("hoster", acct.Hoster, "account", acct.ID, "check", true)
		defer s.Client.CloseIdleConnections()
		res, err := ac.CheckAccount(ctx, s)
		if err == nil {
			return res, nil
		}
		if errors.Is(err, accounts.ErrLocked) || errors.Is(err, accounts.ErrSecretNotFound) {
			return nil, err
		}
		switch hosters.KindOf(err) {
		case hosters.KindNeedsAuth:
			return &accounts.CheckResult{Status: accounts.StatusInvalid, Message: shortErr(err), QuotaTotal: -1, QuotaUsed: -1}, nil
		case hosters.KindQuota:
			return &accounts.CheckResult{Status: accounts.StatusQuota, Message: shortErr(err), QuotaTotal: -1, QuotaUsed: -1}, nil
		}
		return nil, err
	}
}

func shortErr(err error) string {
	msg := err.Error()
	if len(msg) > 300 {
		msg = msg[:300] + "…"
	}
	return msg
}

// cookieDomains are the cookie domains kept when importing cookies for a
// hoster (the hoster's [hosters.<name>].domains are added).
var cookieDomains = map[string][]string{
	"mega":       {"mega.nz", "mega.io", "mega.co.nz"},
	"mediafire":  {"mediafire.com"},
	"gdrive":     {"google.com", "drive.google.com", "googleusercontent.com"},
	"terabox":    config.DefaultTeraBoxDomains,
	"pixeldrain": {"pixeldrain.com"},
	"gofile":     {"gofile.io"},
	"dropbox":    {"dropbox.com"},
	"onedrive":   {"live.com", "onedrive.live.com", "1drv.ms", "sharepoint.com"},
	"onefichier": {"1fichier.com"},
	"yandex":     {"yandex.ru", "yandex.com", "disk.yandex.ru", "disk.yandex.com", "yadi.sk"},
}

func hosterDomains(cfg *config.Config) func(string) []string {
	return func(hoster string) []string {
		d := slices.Concat(cookieDomains[hoster], cfg.Hoster(hoster).Domains)
		if len(d) == 0 {
			return nil
		}
		slices.Sort(d)
		return slices.Compact(d)
	}
}

// --- accounts -> API --------------------------------------------------------

// accountService implements api.AccountService.
type accountService struct {
	m   *accounts.Manager
	reg *hosters.Registry
}

var _ api.AccountService = accountService{}

func (s accountService) List(ctx context.Context) ([]accounts.Account, error) {
	list, err := s.m.List(ctx, "")
	if err != nil {
		return nil, err
	}
	out := make([]accounts.Account, len(list))
	for i, a := range list {
		out[i] = *a
	}
	return out, nil
}

func secretFor(kind accounts.Kind, password, cookies, apiKey string) accounts.Secret {
	switch kind {
	case accounts.KindPassword:
		return accounts.Secret{Password: password}
	case accounts.KindCookie:
		return accounts.Secret{Cookies: cookies}
	case accounts.KindAPIKey:
		return accounts.Secret{APIKey: apiKey}
	}
	return accounts.Secret{}
}

func (s accountService) Create(ctx context.Context, req api.AccountCreateRequest) (accounts.Account, error) {
	if s.reg.Get(req.Hoster) == nil {
		return accounts.Account{}, fmt.Errorf("hoster %q desconocido: %w", req.Hoster, core.ErrInvalidArgument)
	}
	kind := accounts.Kind(req.Kind)
	a, err := s.m.Create(ctx, accounts.NewAccount{
		Hoster: req.Hoster, Label: req.Label, Kind: kind, Username: req.Username,
		Enabled: req.Enabled, AllowRotation: req.AllowRotation,
		Secret: secretFor(kind, req.Password, req.Cookies, req.APIKey),
	})
	if err != nil {
		return accounts.Account{}, err
	}
	return *a, nil
}

func (s accountService) Update(ctx context.Context, id string, p api.AccountPatch) (accounts.Account, error) {
	a, err := s.m.Update(ctx, id, accounts.AccountPatch{
		Label: p.Label, Username: p.Username, Enabled: p.Enabled, AllowRotation: p.AllowRotation,
		Password: p.Password, Cookies: p.Cookies, APIKey: p.APIKey,
	})
	if err != nil {
		return accounts.Account{}, err
	}
	return *a, nil
}

func (s accountService) Delete(ctx context.Context, id string) error { return s.m.Delete(ctx, id) }

func (s accountService) Check(ctx context.Context, id string) (accounts.Account, error) {
	a, err := s.m.Check(ctx, id)
	if a != nil {
		// The check ran: its failure is in the account status.
		return *a, nil
	}
	return accounts.Account{}, err
}

func (s accountService) Submit2FA(ctx context.Context, id, code string) (accounts.Account, error) {
	a, err := s.m.SubmitMFA(ctx, id, code)
	if a != nil {
		return *a, nil
	}
	return accounts.Account{}, err
}

// ImportCookies replaces the cookies of the cookie account with the same
// hoster and label, or creates one.
func (s accountService) ImportCookies(ctx context.Context, hoster, label, cookies string) (accounts.Account, error) {
	if s.reg.Get(hoster) == nil {
		return accounts.Account{}, fmt.Errorf("hoster %q desconocido: %w", hoster, core.ErrInvalidArgument)
	}
	list, err := s.m.List(ctx, hoster)
	if err != nil {
		return accounts.Account{}, err
	}
	for _, a := range list {
		if a.Kind == accounts.KindCookie && a.Label == label {
			if _, err := s.m.ImportCookies(ctx, a.ID, cookies, nil); err != nil {
				return accounts.Account{}, err
			}
			got, err := s.m.Get(ctx, a.ID)
			if err != nil {
				return accounts.Account{}, err
			}
			return *got, nil
		}
	}
	a, err := s.m.Create(ctx, accounts.NewAccount{Hoster: hoster, Label: label, Kind: accounts.KindCookie,
		Secret: accounts.Secret{Cookies: cookies}})
	if err != nil {
		return accounts.Account{}, err
	}
	return *a, nil
}

// secretsService implements api.SecretsService.
type secretsService struct{ m *accounts.Manager }

var _ api.SecretsService = secretsService{}

func (s secretsService) Status(context.Context) (accounts.StoreStatus, error) {
	return s.m.StoreStatus(), nil
}
func (s secretsService) Unlock(ctx context.Context, pw string) error { return s.m.Unlock(ctx, pw) }
func (s secretsService) Lock(context.Context) error                  { return s.m.Lock() }
func (s secretsService) ChangePassword(ctx context.Context, oldPW, newPW string) error {
	return s.m.ChangeMasterPassword(ctx, oldPW, newPW)
}

// googleOAuth implements api.GoogleOAuth.
type googleOAuth struct{ g *accounts.GoogleOAuth }

var _ api.GoogleOAuth = googleOAuth{}

func (o googleOAuth) Start(ctx context.Context, id, redirect string) (string, error) {
	return o.g.Start(ctx, id, redirect)
}

func (o googleOAuth) Callback(ctx context.Context, q url.Values) error {
	_, err := o.g.Complete(ctx, q)
	return err
}

func (o googleOAuth) CompleteURL(ctx context.Context, pasted string) error {
	_, err := o.g.CompleteURL(ctx, pasted)
	return err
}

// --- rotation -> API --------------------------------------------------------

// egressService implements api.EgressService over rotation.Admin.
type egressService struct{ a rotation.Admin }

var _ api.EgressService = egressService{}

func (e egressService) ImportProxies(ctx context.Context, text string) (api.ImportResult, error) {
	res, err := e.a.ImportProxies(ctx, text)
	if err != nil {
		return api.ImportResult{}, fmt.Errorf("%w: %w", core.ErrInvalidArgument, err)
	}
	out := api.ImportResult{Added: len(res.Added), Skipped: len(res.Duplicates), IDs: res.Added}
	for _, le := range res.Errors {
		out.Errors = append(out.Errors, api.ImportLineError{Line: le.Line, Message: le.Err})
	}
	return out, nil
}

func (e egressService) ImportWireGuard(ctx context.Context, name, conf string) (api.ImportResult, error) {
	st, err := e.a.AddWireGuard(ctx, name, conf)
	if err != nil {
		if errors.Is(err, rotation.ErrExists) || errors.Is(err, rotation.ErrClosed) {
			return api.ImportResult{}, err
		}
		return api.ImportResult{}, fmt.Errorf("%w: %w", core.ErrInvalidArgument, err)
	}
	return api.ImportResult{Added: 1, IDs: []string{st.ID}}, nil
}

func (e egressService) Test(ctx context.Context, id string) (rotation.EgressStatus, error) {
	return e.a.TestEgress(ctx, id)
}

// --- postprocess -> engine ----------------------------------------------------

// postProcessor implements core.PostProcessor with internal/postprocess.
type postProcessor struct {
	p   *postprocess.Processor
	log *slog.Logger
}

var _ core.PostProcessor = postProcessor{}

func (pp postProcessor) Process(ctx context.Context, info core.PostProcessInfo, path string) error {
	res, err := pp.p.Run(ctx, postprocess.Item{
		Path:     path,
		Checksum: info.Checksum,
		// Archives are extracted next to themselves, which keeps the
		// folder structure of folder packages.
		PackageDir: filepath.Dir(path),
	})
	switch {
	case err == nil:
	case ctx.Err() != nil:
		return err
	case errors.Is(err, postprocess.ErrChecksumMismatch):
		return fmt.Errorf("%w: %w", core.ErrChecksumMismatch, err)
	case errors.Is(err, postprocess.ErrWrongPassword), errors.Is(err, postprocess.ErrPasswordRequired):
		return errors.New("el archivo comprimido tiene contraseña y ninguna de las configuradas sirve: " +
			"añádela en Ajustes › Postproceso › Contraseñas y pulsa Reintentar")
	case errors.Is(err, postprocess.ErrUnsupported):
		// Not something we can extract: the download itself is fine.
		pp.log.Info("archive not extracted", "download", info.Download.ID, "err", err)
		return nil
	case errors.Is(err, postprocess.ErrCorrupt):
		return errors.New("el archivo comprimido está dañado: reinicia la descarga para bajarlo de nuevo")
	default:
		return err
	}
	if res != nil {
		if res.Waiting {
			pp.log.Debug("archive waits for more volumes", "download", info.Download.ID)
		}
		if len(res.Rejected) > 0 {
			pp.log.Warn("unsafe archive entries skipped", "download", info.Download.ID, "count", len(res.Rejected))
		}
	}
	return nil
}

// megaMACVerifier adapts mega.VerifyMAC to postprocess.VerifierFunc.
func megaMACVerifier(ctx context.Context, path, value string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := mega.VerifyMAC(contextReader{ctx, f}, value); err != nil {
		if errors.Is(err, mega.ErrMACMismatch) {
			return fmt.Errorf("%w: %w", postprocess.ErrChecksumMismatch, err)
		}
		return err
	}
	return nil
}

// contextReader stops a long hash when ctx is canceled.
type contextReader struct {
	ctx context.Context
	r   *os.File
}

func (c contextReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

// --- events ---------------------------------------------------------------

// accountEvents publishes account changes on the engine bus (never
// blocking: Bus.Publish drops events for slow subscribers).
func accountEvents(bus *core.Bus) func(accounts.ChangeEvent) {
	return func(ev accounts.ChangeEvent) {
		switch ev.Type {
		case accounts.ChangeDeleted:
			bus.Publish(core.Event{Type: core.EventAccountRemoved, AccountID: ev.AccountID})
		case accounts.ChangeLocked, accounts.ChangeUnlocked:
			bus.Publish(core.Event{Type: core.EventSecretsUpdated, Message: strings.TrimPrefix(string(ev.Type), "secrets_")})
		default:
			bus.Publish(core.Event{Type: core.EventAccountUpdated, AccountID: ev.AccountID, Account: ev.Account})
		}
	}
}
