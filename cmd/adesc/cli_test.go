package main

import (
	"bytes"
	"context"
	"fmt"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"admindescargas/internal/accounts"
	"admindescargas/internal/api"
	"admindescargas/internal/config"
	"admindescargas/internal/core"
	"admindescargas/internal/netx"
	"admindescargas/internal/rotation"
)

// cliEngine implements what the CLI tests need; other methods panic.
type cliEngine struct {
	core.Engine
	mu    sync.Mutex
	calls []string
	add   core.AddLinksRequest
}

func (e *cliEngine) rec(s string) { e.mu.Lock(); e.calls = append(e.calls, s); e.mu.Unlock() }
func (e *cliEngine) last() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.calls) == 0 {
		return ""
	}
	return e.calls[len(e.calls)-1]
}

func (e *cliEngine) AddLinks(_ context.Context, r core.AddLinksRequest) ([]core.GrabberEntry, error) {
	e.mu.Lock()
	e.add = r
	e.mu.Unlock()
	e.rec("AddLinks")
	return []core.GrabberEntry{{ID: 1, URL: "https://mega.nz/file/x", Hoster: "mega", State: core.GrabberReady, TotalSize: 2048}}, nil
}
func (e *cliEngine) ListLinkGrabber(context.Context) ([]core.GrabberEntry, error) {
	return []core.GrabberEntry{{ID: 1, State: core.GrabberReady}, {ID: 2, State: core.GrabberResolving}}, nil
}
func (e *cliEngine) ConfirmLinkGrabber(_ context.Context, r core.ConfirmRequest) ([]core.Package, error) {
	e.rec(fmt.Sprintf("Confirm%d", len(r.Items)))
	return []core.Package{{ID: 3, Name: "pkg", FilesTotal: 1}}, nil
}
func (e *cliEngine) ListDownloads(context.Context, core.DownloadFilter) ([]core.Download, error) {
	e.rec("ListDownloads")
	return []core.Download{{ID: 1, PackageID: 3, Name: "big.iso", State: core.StateDownloading, Size: 1 << 30, Done: 1 << 29, Speed: 5 << 20, ETA: 100}}, nil
}
func (e *cliEngine) PauseAll(context.Context) error       { e.rec("PauseAll"); return nil }
func (e *cliEngine) RetryAllFailed(context.Context) error { e.rec("RetryAllFailed"); return nil }
func (e *cliEngine) PauseDownloads(_ context.Context, ids []int64) error {
	e.rec(fmt.Sprintf("PauseDownloads%v", ids))
	return nil
}
func (e *cliEngine) RestartPackages(_ context.Context, ids []int64) error {
	e.rec(fmt.Sprintf("RestartPackages%v", ids))
	return nil
}
func (e *cliEngine) DeleteDownloads(_ context.Context, ids []int64, files bool) error {
	e.rec(fmt.Sprintf("DeleteDownloads%v %v", ids, files))
	return nil
}
func (e *cliEngine) Subscribe(ctx context.Context) <-chan core.Event {
	return core.NewBus(1).Subscribe(ctx)
}

type cliAccounts struct {
	api.AccountService
	got api.AccountCreateRequest
}

func (a *cliAccounts) Create(_ context.Context, r api.AccountCreateRequest) (accounts.Account, error) {
	a.got = r
	return accounts.Account{ID: "acc-1", Hoster: r.Hoster, Kind: accounts.Kind(r.Kind)}, nil
}
func (a *cliAccounts) List(context.Context) ([]accounts.Account, error) {
	return []accounts.Account{{ID: "acc-1", Hoster: "mega", Kind: accounts.KindPassword, Status: accounts.StatusOK, QuotaTotal: -1, QuotaUsed: -1}}, nil
}

type cliEnv struct {
	url    string
	engine *cliEngine
	accts  *cliAccounts
}

func newCLIEnv(t *testing.T) *cliEnv {
	t.Helper()
	env := &cliEnv{engine: &cliEngine{}, accts: &cliAccounts{}}
	srv := httptest.NewServer(api.New(api.Deps{
		Engine:   env.engine,
		Rotation: rotation.NewStatic(nil),
		Accounts: env.accts,
		PublicIP: func(context.Context, netx.Egress) (netip.Addr, error) { return netip.MustParseAddr("192.0.2.10"), nil },
		Server:   config.ServerConfig{Listen: "127.0.0.1:7979", Token: "tok"},
	}))
	t.Cleanup(srv.Close)
	env.url = srv.URL
	return env
}

func resetFlags(cmd *cobra.Command) {
	reset := func(f *pflag.Flag) {
		if sv, ok := f.Value.(pflag.SliceValue); ok {
			_ = sv.Replace(nil)
		} else {
			_ = f.Value.Set(f.DefValue)
		}
		f.Changed = false
	}
	for c := cmd; c != nil; c = c.Parent() {
		c.Flags().VisitAll(reset)
		c.PersistentFlags().VisitAll(reset)
	}
}

// run executes the CLI with args against env and returns stdout.
func (env *cliEnv) run(t *testing.T, stdin string, args ...string) (string, error) {
	t.Helper()
	full := append([]string{"--url", env.url, "--token", "tok"}, args...)
	if cmd, _, err := rootCmd.Find(args); err == nil {
		resetFlags(cmd)
	}
	var out, errOut bytes.Buffer
	rootCmd.SetArgs(full)
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&errOut)
	rootCmd.SetIn(strings.NewReader(stdin))
	err := rootCmd.ExecuteContext(context.Background())
	return out.String(), err
}

func TestCLICommands(t *testing.T) {
	env := newCLIEnv(t)

	out, err := env.run(t, "mira esto https://mega.nz/file/x#k y nada más\n", "add")
	if err != nil || !strings.Contains(out, "mega") || !strings.Contains(out, "listo") {
		t.Fatalf("add stdin: %v\n%s", err, out)
	}
	if env.engine.add.Source != "cli" || !strings.Contains(env.engine.add.Text, "mega.nz") {
		t.Fatalf("add req = %+v", env.engine.add)
	}
	if _, err := env.run(t, "", "add", "--start", "--dest", "/tmp/x", "https://a.example/1"); err != nil {
		t.Fatal(err)
	}
	if !env.engine.add.AutoConfirm || env.engine.add.DestDir != "/tmp/x" || len(env.engine.add.URLs) != 1 {
		t.Fatalf("add --start req = %+v", env.engine.add)
	}

	out, err = env.run(t, "", "ls")
	if err != nil || !strings.Contains(out, "big.iso") || !strings.Contains(out, "50.0%") || !strings.Contains(out, "descargando") {
		t.Fatalf("ls: %v\n%s", err, out)
	}
	out, err = env.run(t, "", "--json", "ls")
	if err != nil || !strings.Contains(out, `"name": "big.iso"`) {
		t.Fatalf("ls --json: %v\n%s", err, out)
	}

	for _, c := range []struct {
		args []string
		call string
	}{
		{[]string{"pause", "--all"}, "PauseAll"},
		{[]string{"pause", "1", "2,3"}, "PauseDownloads[1 2 3]"},
		{[]string{"retry", "--failed"}, "RetryAllFailed"},
		{[]string{"restart", "-p", "3"}, "RestartPackages[3]"},
		{[]string{"rm", "--files", "4"}, "DeleteDownloads[4] true"},
		{[]string{"rm", "5"}, "DeleteDownloads[5] false"},
		{[]string{"grabber", "confirm", "--all"}, "Confirm1"},
	} {
		if _, err := env.run(t, "", c.args...); err != nil {
			t.Errorf("%v: %v", c.args, err)
			continue
		}
		if got := env.engine.last(); got != c.call {
			t.Errorf("%v called %q, want %q", c.args, got, c.call)
		}
	}
	if _, err := env.run(t, "", "pause"); err == nil {
		t.Error("pause without ids accepted")
	}
	if _, err := env.run(t, "", "pause", "abc"); err == nil {
		t.Error("pause abc accepted")
	}

	out, err = env.run(t, "hunter2-password\n", "accounts", "add", "mega", "--user", "me@example.com")
	if err != nil || !strings.Contains(out, "acc-1") || env.accts.got.Password != "hunter2-password" || env.accts.got.Kind != "password" {
		t.Fatalf("accounts add: %v %+v\n%s", err, env.accts.got, out)
	}
	if strings.Contains(out, "hunter2") {
		t.Fatal("password echoed")
	}
	out, err = env.run(t, "", "accounts", "ls")
	if err != nil || !strings.Contains(out, "acc-1") || !strings.Contains(out, "ok") {
		t.Fatalf("accounts ls: %v\n%s", err, out)
	}
	cookies := filepath.Join(t.TempDir(), "cookies.txt")
	_ = os.WriteFile(cookies, []byte(".terabox.com\tTRUE\t/\tTRUE\t0\tndus\tX\n"), 0o600)
	if _, err := env.run(t, "", "accounts", "add", "terabox", "--cookies-file", cookies); err != nil || !strings.Contains(env.accts.got.Cookies, "ndus") {
		t.Fatalf("accounts add cookie: %v", err)
	}

	out, err = env.run(t, "", "ip")
	if err != nil || !strings.Contains(out, "192.0.2.10") || !strings.Contains(out, "direct") {
		t.Fatalf("ip: %v\n%s", err, out)
	}
	out, err = env.run(t, "", "egress", "ls")
	if err != nil || !strings.Contains(out, "direct") {
		t.Fatalf("egress ls: %v\n%s", err, out)
	}
	// Endpoints without a service give a clear error.
	if _, err := env.run(t, "", "selftest", "mega"); err == nil || !strings.Contains(err.Error(), "mega") {
		t.Fatalf("selftest without runner: %v", err)
	}
	if _, err := env.run(t, "socks5://a:b@1.2.3.4:1080\n", "egress", "import", "-"); err == nil || !strings.Contains(err.Error(), "no tiene esta función") {
		t.Fatalf("egress import without service: %v", err)
	}
}

func TestCLIDaemonDown(t *testing.T) {
	env := &cliEnv{url: "http://127.0.0.1:1"}
	_, err := env.run(t, "", "ls")
	if err == nil || !strings.Contains(err.Error(), "adesc daemon") {
		t.Fatalf("err = %v", err)
	}
}

func TestPasswordCommand(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	cfg := config.Default()
	cfg.DataDir, cfg.DownloadDir = dir, dir
	if err := config.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	env := &cliEnv{url: "http://127.0.0.1:1"}
	if _, err := env.run(t, "corto\n", "--config", path, "password"); err == nil {
		t.Fatal("short password accepted")
	}
	if _, err := env.run(t, "una contraseña larga\n", "--config", path, "password"); err != nil {
		t.Fatal(err)
	}
	got, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := api.VerifyPassword(got.Server.PasswordHash, "una contraseña larga"); !ok || err != nil {
		t.Fatalf("hash = %q %v %v", got.Server.PasswordHash, ok, err)
	}
	if _, err := env.run(t, "", "--config", path, "password", "--clear"); err != nil {
		t.Fatal(err)
	}
	got, _ = config.Load(path)
	if got.Server.PasswordHash != "" {
		t.Fatal("not cleared")
	}
	globalFlags.configPath = ""
}
