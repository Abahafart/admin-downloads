package netx

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestHelperProcess is run as a child process by the command tests:
// argv = [test binary, -test.run=TestHelperProcess, --, action, args...].
func TestHelperProcess(t *testing.T) {
	if os.Getenv("ADESC_HELPER_PROCESS") != "1" {
		return
	}
	args := os.Args
	for i, a := range args {
		if a == "--" {
			args = args[i+1:]
			break
		}
	}
	switch args[0] {
	case "append": // append <file> <text>
		f, err := os.OpenFile(args[1], os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			os.Exit(3)
		}
		fmt.Fprintln(f, args[2])
		f.Close()
		os.Exit(0)
	case "fail":
		fmt.Fprintln(os.Stderr, "vpn: not logged in")
		os.Exit(2)
	case "sleep":
		time.Sleep(time.Minute)
		os.Exit(0)
	}
	os.Exit(4)
}

func helperArgv(t *testing.T, args ...string) []string {
	t.Helper()
	t.Setenv("ADESC_HELPER_PROCESS", "1")
	return append([]string{os.Args[0], "-test.run=^TestHelperProcess$", "--"}, args...)
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return strings.Fields(string(b))
}

func TestRunArgv(t *testing.T) {
	log := filepath.Join(t.TempDir(), "log")
	if err := RunArgv(context.Background(), helperArgv(t, "append", log, "x"), 30*time.Second); err != nil {
		t.Fatal(err)
	}
	if got := readLines(t, log); len(got) != 1 || got[0] != "x" {
		t.Fatalf("log = %v", got)
	}
	err := RunArgv(context.Background(), helperArgv(t, "fail"), 30*time.Second)
	if err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Fatalf("err = %v", err)
	}
	start := time.Now()
	err = RunArgv(context.Background(), helperArgv(t, "sleep"), 300*time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 20*time.Second {
		t.Fatalf("err = %v after %s", err, time.Since(start))
	}
	if err := RunArgv(context.Background(), nil, 0); err == nil {
		t.Fatal("empty argv accepted")
	}
}

func TestSystemVPN(t *testing.T) {
	log := filepath.Join(t.TempDir(), "log")
	v, err := NewSystemVPN(SystemVPNOptions{
		Name:       "mullvad",
		Connect:    helperArgv(t, "append", log, "connect"),
		Disconnect: helperArgv(t, "append", log, "disconnect"),
		WaitAfter:  time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if v.ID() != "systemvpn:mullvad" || v.Scope() != ScopeGlobal || v.Kind() != KindSystemVPN {
		t.Fatal("metadata")
	}
	if err := v.Rotate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := readLines(t, log); strings.Join(got, ",") != "disconnect,connect" {
		t.Fatalf("without rotate argv: %v", got)
	}

	log2 := filepath.Join(t.TempDir(), "log")
	v2, _ := NewSystemVPN(SystemVPNOptions{Name: "n", Rotate: helperArgv(t, "append", log2, "rotate"), WaitAfter: time.Millisecond})
	if err := v2.Rotate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := readLines(t, log2); strings.Join(got, ",") != "rotate" {
		t.Fatalf("with rotate argv: %v", got)
	}

	v3, _ := NewSystemVPN(SystemVPNOptions{Name: "f", Rotate: helperArgv(t, "fail"), WaitAfter: time.Millisecond})
	if err := v3.Rotate(context.Background()); err == nil || v3.Healthy() {
		t.Fatalf("failing VPN: err=%v healthy=%v", err, v3.Healthy())
	}
	if _, err := NewSystemVPN(SystemVPNOptions{Name: "x"}); err == nil {
		t.Fatal("VPN without commands accepted")
	}
}

type fakeIGD struct {
	mu    sync.Mutex
	calls []string
	ip    string
	fail  error
}

func (f *fakeIGD) record(s string) {
	f.mu.Lock()
	f.calls = append(f.calls, s)
	f.mu.Unlock()
}

func (f *fakeIGD) ForceTerminationCtx(context.Context) error {
	f.record("terminate")
	if f.fail != nil {
		return f.fail
	}
	f.mu.Lock()
	f.ip = ""
	f.mu.Unlock()
	return nil
}

func (f *fakeIGD) RequestConnectionCtx(context.Context) error {
	f.record("request")
	go func() {
		time.Sleep(20 * time.Millisecond)
		f.mu.Lock()
		f.ip = "203.0.113.50"
		f.mu.Unlock()
	}()
	return nil
}

func (f *fakeIGD) GetExternalIPAddressCtx(context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ip == "" {
		return "0.0.0.0", nil
	}
	return f.ip, nil
}

func TestRouterUPnP(t *testing.T) {
	broken := &fakeIGD{ip: "203.0.113.1", fail: errors.New("action not authorized")}
	igd := &fakeIGD{ip: "203.0.113.1"}
	r, err := NewRouter(RouterOptions{
		WaitAfter: 10 * time.Millisecond,
		Timeout:   5 * time.Second,
		Discover: func(context.Context) ([]IGDConnection, error) {
			return []IGDConnection{broken, igd}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if r.ID() != RouterID || r.Scope() != ScopeGlobal {
		t.Fatal("metadata")
	}
	if err := r.Rotate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if strings.Join(igd.calls, ",") != "terminate,request" {
		t.Fatalf("calls = %v", igd.calls)
	}
	if ip, _ := igd.GetExternalIPAddressCtx(context.Background()); ip != "203.0.113.50" {
		t.Fatalf("ip = %s", ip)
	}

	none, _ := NewRouter(RouterOptions{Discover: func(context.Context) ([]IGDConnection, error) { return nil, nil }})
	if err := none.Rotate(context.Background()); err == nil || !strings.Contains(err.Error(), "UPnP") {
		t.Fatalf("err = %v", err)
	}

	// The link never comes back: bounded by Timeout.
	stuck := &fakeIGD{ip: "203.0.113.1"}
	sr, _ := NewRouter(RouterOptions{WaitAfter: 10 * time.Millisecond, Timeout: 200 * time.Millisecond,
		Discover: func(context.Context) ([]IGDConnection, error) { return []IGDConnection{stuckIGD{stuck}}, nil }})
	if err := sr.Rotate(context.Background()); err == nil {
		t.Fatal("stuck link reported success")
	}
}

type stuckIGD struct{ *fakeIGD }

func (s stuckIGD) RequestConnectionCtx(context.Context) error { return errors.New("refused") }

func TestRouterScript(t *testing.T) {
	log := filepath.Join(t.TempDir(), "log")
	r, err := NewRouter(RouterOptions{Method: RouterScript, Script: helperArgv(t, "append", log, "reconnect"), WaitAfter: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Rotate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := readLines(t, log); len(got) != 1 || got[0] != "reconnect" {
		t.Fatalf("log = %v", got)
	}
	if _, err := NewRouter(RouterOptions{Method: RouterScript}); err == nil {
		t.Fatal("script method without argv accepted")
	}
}
