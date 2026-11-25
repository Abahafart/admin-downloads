package netx

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"
)

func TestParseProxyLine(t *testing.T) {
	tests := []struct {
		line    string
		want    ProxySpec
		wantErr string
	}{
		{line: "http://1.2.3.4:8080", want: ProxySpec{Scheme: "http", Host: "1.2.3.4", Port: "8080", Sessions: 1}},
		{line: "https://u:p@proxy.example:443", want: ProxySpec{Scheme: "https", Host: "proxy.example", Port: "443", Username: "u", Password: "p", Sessions: 1}},
		{line: "socks5://user:p%40ss@10.0.0.1:1080", want: ProxySpec{Scheme: "socks5", Host: "10.0.0.1", Port: "1080", Username: "user", Password: "p@ss", Sessions: 1}},
		{line: "SOCKS5H://h:1", want: ProxySpec{Scheme: "socks5", Host: "h", Port: "1", Sessions: 1}},
		{line: "socks://h:1", want: ProxySpec{Scheme: "socks5", Host: "h", Port: "1", Sessions: 1}},
		{line: "1.2.3.4:3128", want: ProxySpec{Scheme: "http", Host: "1.2.3.4", Port: "3128", Sessions: 1}},
		{line: "user:pass@1.2.3.4:3128", want: ProxySpec{Scheme: "http", Host: "1.2.3.4", Port: "3128", Username: "user", Password: "pass", Sessions: 1}},
		{line: "gw.example:7777:cust:se#cret", want: ProxySpec{Scheme: "http", Host: "gw.example", Port: "7777", Username: "cust", Password: "se#cret", Sessions: 1}},
		{line: "[2001:db8::1]:8080", want: ProxySpec{Scheme: "http", Host: "2001:db8::1", Port: "8080", Sessions: 1}},
		{line: "http://[2001:db8::1]:8080", want: ProxySpec{Scheme: "http", Host: "2001:db8::1", Port: "8080", Sessions: 1}},
		{line: "socks5://cust-session-{session}:pw@gw.example:9000#sessions=3", want: ProxySpec{Scheme: "socks5", Host: "gw.example", Port: "9000", Username: "cust-session-{session}", Password: "pw", Sessions: 3}},
		{line: "http://cust-{session}:pw@gw.example:9000", want: ProxySpec{Scheme: "http", Host: "gw.example", Port: "9000", Username: "cust-{session}", Password: "pw", Sessions: 1}},
		{line: "http://u:pw@gw.example:9000#sessions=4", want: ProxySpec{Scheme: "http", Host: "gw.example", Port: "9000", Username: "u", Password: "pw", Sessions: 1}},
		{line: "socks4://h:1", wantErr: "unsupported scheme"},
		{line: "http://h", wantErr: "port"},
		{line: "h:0", wantErr: "port"},
		{line: "h:99999", wantErr: "port"},
		{line: "a:b:c", wantErr: "want host:port"},
		{line: "http://h:1/path", wantErr: "path"},
		{line: "http://u:p@h:1#sessions=0", wantErr: "sessions"},
		{line: "http://u:p@h:1#sessions=x", wantErr: "sessions"},
	}
	for _, tt := range tests {
		t.Run(tt.line, func(t *testing.T) {
			got, err := ParseProxyLine(tt.line)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("got %#v\nwant %#v", got, tt.want)
			}
		})
	}
}

func TestParseProxyListCommentsAndDedupe(t *testing.T) {
	text := "\ufeff# my proxies\n" +
		"; another comment\n" +
		"// and another\n" +
		"\n" +
		"socks5://a:secretpw@10.0.0.1:1080   # inline comment\n" +
		"10.0.0.2:3128\t; inline\n" +
		"socks5://a:secretpw@10.0.0.1:1080\n" + // duplicate
		"ftp://x:hunter2@h:21\n" +
		"bad line here\n" +
		"10.0.0.3:3128:user:topsecret\n"
	specs, errs := ParseProxyList(text)
	if len(specs) != 3 {
		t.Fatalf("specs = %v", specs)
	}
	if specs[0].Addr() != "10.0.0.1:1080" || specs[1].Addr() != "10.0.0.2:3128" || specs[2].Username != "user" {
		t.Fatalf("specs = %v", specs)
	}
	if len(errs) != 2 || errs[0].Line != 8 || errs[1].Line != 9 {
		t.Fatalf("errs = %+v", errs)
	}
	for _, e := range errs {
		if strings.Contains(e.Text, "hunter2") || strings.Contains(e.Err, "hunter2") {
			t.Fatalf("password leaked in %+v", e)
		}
	}
}

func TestProxySpecNeverPrintsPassword(t *testing.T) {
	sp, err := ParseProxyLine("socks5://cust-{session}:topsecret@gw.example:9000#sessions=2")
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{sp.Redacted(), sp.String(), fmt.Sprint(sp), fmt.Sprintf("%v %+v %#v %s", sp, sp, sp, sp)} {
		if strings.Contains(s, "topsecret") {
			t.Fatalf("password leaked: %s", s)
		}
	}
	if got := sp.Redacted(); got != "socks5://cust-{session}@gw.example:9000" {
		t.Fatalf("Redacted = %s", got)
	}
	if sp.Instances() != 2 || sp.InstanceID(0) == sp.InstanceID(1) || !strings.HasPrefix(sp.InstanceID(1), sp.BaseID()+"#") {
		t.Fatalf("ids %s %s", sp.InstanceID(0), sp.InstanceID(1))
	}
	if strings.Contains(sp.BaseID(), "cust") {
		t.Fatalf("BaseID contains the username: %s", sp.BaseID())
	}
	plain, _ := ParseProxyLine("http://1.2.3.4:80")
	if plain.InstanceID(0) != "http:1.2.3.4:80" {
		t.Fatalf("InstanceID = %s", plain.InstanceID(0))
	}
}

func TestSanitizeError(t *testing.T) {
	err := fmt.Errorf("x: %w", &url.Error{Op: "Get", URL: "https://h/file?sig=abc", Err: errors.New("connection refused")})
	got := SanitizeError(err)
	if strings.Contains(got, "sig=") || strings.Contains(got, "pw") {
		t.Fatalf("SanitizeError = %q", got)
	}
	got = SanitizeError(fmt.Errorf("proxyconnect tcp: socks5://user:pw@h:1 refused"))
	if strings.Contains(got, "pw") {
		t.Fatalf("SanitizeError = %q", got)
	}
}
