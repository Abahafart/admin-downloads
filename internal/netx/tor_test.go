package netx

import (
	"bufio"
	"context"
	"encoding/hex"
	"net"
	"strings"
	"sync"
	"testing"
)

// fakeControl is a tiny tor control port: PROTOCOLINFO announces
// HASHEDPASSWORD, AUTHENTICATE checks the password, SIGNAL is recorded.
type fakeControl struct {
	addr     string
	password string
	mu       sync.Mutex
	signals  []string
	authFail int
}

func startFakeControl(t *testing.T, password string) *fakeControl {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	fc := &fakeControl{addr: ln.Addr().String(), password: password}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go fc.serve(c)
		}
	}()
	return fc
}

func (fc *fakeControl) serve(c net.Conn) {
	defer c.Close()
	r := bufio.NewReader(c)
	authed := false
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		cmd, arg, _ := strings.Cut(line, " ")
		switch strings.ToUpper(cmd) {
		case "PROTOCOLINFO":
			c.Write([]byte("250-PROTOCOLINFO 1\r\n250-AUTH METHODS=HASHEDPASSWORD\r\n250-VERSION Tor=\"0.4.8.0\"\r\n250 OK\r\n"))
		case "AUTHENTICATE":
			pw, _ := hex.DecodeString(strings.TrimSpace(arg))
			if string(pw) != fc.password {
				fc.mu.Lock()
				fc.authFail++
				fc.mu.Unlock()
				c.Write([]byte("515 Authentication failed: Password did not match HashedControlPassword value from configuration\r\n"))
				return
			}
			authed = true
			c.Write([]byte("250 OK\r\n"))
		case "SIGNAL":
			if !authed {
				c.Write([]byte("514 Authentication required.\r\n"))
				return
			}
			fc.mu.Lock()
			fc.signals = append(fc.signals, arg)
			fc.mu.Unlock()
			c.Write([]byte("250 OK\r\n"))
		case "QUIT":
			c.Write([]byte("250 closing connection\r\n"))
			return
		default:
			c.Write([]byte("510 Unrecognized command\r\n"))
		}
	}
}

func (fc *fakeControl) count() (signals []string, authFail int) {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	return append([]string(nil), fc.signals...), fc.authFail
}

func TestTorCircuitsIsolationAndNewnym(t *testing.T) {
	var users []string
	var mu sync.Mutex
	socksAddr := startSOCKS(t, anyCreds{}, nil, &users, &mu)
	ctrl := startFakeControl(t, "ctrl-pw")
	_, port := hostServer(t)

	tor, err := NewTor(context.Background(), TorOptions{SocksAddr: socksAddr, ControlAddr: ctrl.addr, ControlPassword: "ctrl-pw", Circuits: 3})
	if err != nil {
		t.Fatal(err)
	}
	defer tor.Close()
	cs := tor.Circuits()
	if len(cs) != 3 || cs[0].ID() != "tor:0" || cs[2].ID() != "tor:2" || cs[0].Kind() != KindTor {
		t.Fatalf("circuits = %v", cs)
	}
	for _, c := range cs {
		get(t, c, "http://127.0.0.1:"+port+"/")
	}
	mu.Lock()
	if len(users) != 3 || users[0] == users[1] || users[1] == users[2] || users[0] == users[2] {
		t.Fatalf("SOCKS users not isolated: %v", users)
	}
	mu.Unlock()

	before := cs[1].SOCKSUser()
	if err := cs[1].Rotate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if cs[1].SOCKSUser() == before {
		t.Fatal("Rotate kept the isolation credentials")
	}
	if sig, fails := ctrl.count(); len(sig) != 1 || sig[0] != "NEWNYM" || fails != 0 {
		t.Fatalf("signals = %v, auth failures = %d", sig, fails)
	}
	// Coalesced within TorNewnymInterval.
	if err := tor.NewIdentity(context.Background()); err != nil {
		t.Fatal(err)
	}
	if sig, _ := ctrl.count(); len(sig) != 1 {
		t.Fatalf("NEWNYM not rate limited: %v", sig)
	}
}

func TestTorNewIdentityBadPassword(t *testing.T) {
	ctrl := startFakeControl(t, "right")
	tor, err := NewTor(context.Background(), TorOptions{SocksAddr: "127.0.0.1:9", ControlAddr: ctrl.addr, ControlPassword: "wrong"})
	if err != nil {
		t.Fatal(err)
	}
	err = tor.NewIdentity(context.Background())
	if err == nil {
		t.Fatal("NewIdentity succeeded with a wrong password")
	}
	if strings.Contains(err.Error(), "wrong") {
		t.Fatalf("error leaks password: %v", err)
	}
	// Rotate still works (new credentials) and does not fail.
	if err := tor.Circuits()[0].Rotate(context.Background()); err != nil {
		t.Fatal(err)
	}
}
