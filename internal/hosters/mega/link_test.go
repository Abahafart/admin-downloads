package mega

import (
	"errors"
	"net/url"
	"testing"

	"admindescargas/internal/config"
)

const (
	tFileKey   = "MekSHe11vu1zC4h7VRG3W2SPjhk6BsMH4SoBU8IpESg"
	tFolderKey = "oypUROBVM8E8dUecOWOpaQ"
)

func TestParseLink(t *testing.T) {
	tests := []struct {
		in   string
		want megaLink
		err  error
	}{
		{in: "https://mega.nz/file/AbCd1234#" + tFileKey, want: megaLink{Kind: linkFile, Handle: "AbCd1234", Key: tFileKey}},
		{in: "https://www.mega.nz/file/AbCd1234#" + tFileKey, want: megaLink{Kind: linkFile, Handle: "AbCd1234", Key: tFileKey}},
		{in: "https://mega.co.nz/embed/AbCd1234#" + tFileKey, want: megaLink{Kind: linkFile, Handle: "AbCd1234", Key: tFileKey}},
		{in: "https://mega.nz/file/AbCd1234", want: megaLink{Kind: linkFile, Handle: "AbCd1234"}},
		{in: "https://mega.nz/folder/Fold_-12#" + tFolderKey, want: megaLink{Kind: linkFolder, Handle: "Fold_-12", Key: tFolderKey}},
		{in: "https://mega.nz/folder/Fold_-12#" + tFolderKey + "/file/File5678", want: megaLink{Kind: linkFolder, Handle: "Fold_-12", Key: tFolderKey, File: "File5678"}},
		{in: "https://mega.nz/folder/Fold_-12#" + tFolderKey + "/folder/Sub45678", want: megaLink{Kind: linkFolder, Handle: "Fold_-12", Key: tFolderKey, SubFolder: "Sub45678"}},
		{in: "https://mega.io/folder/Fold_-12#" + tFolderKey, want: megaLink{Kind: linkFolder, Handle: "Fold_-12", Key: tFolderKey}},
		{in: "https://mega.nz/#!AbCd1234!" + tFileKey, want: megaLink{Kind: linkFile, Handle: "AbCd1234", Key: tFileKey}},
		{in: "https://mega.co.nz/#!AbCd1234!" + tFileKey, want: megaLink{Kind: linkFile, Handle: "AbCd1234", Key: tFileKey}},
		{in: "https://mega.nz/#F!Fold_-12!" + tFolderKey, want: megaLink{Kind: linkFolder, Handle: "Fold_-12", Key: tFolderKey}},
		{in: "https://mega.nz/#F!Fold_-12!" + tFolderKey + "!Sub45678", want: megaLink{Kind: linkFolder, Handle: "Fold_-12", Key: tFolderKey, SubFolder: "Sub45678"}},
		{in: "https://mega.nz/file/AbCd1234#" + tFileKey + "?ref=x", want: megaLink{Kind: linkFile, Handle: "AbCd1234", Key: tFileKey}},
		{in: "https://mega.nz/#P!AgAAAAAAAAAA", err: errPasswordLink},
		{in: "https://mega.nz/", err: errNotMEGA},
		{in: "https://mega.nz/pro", err: errNotMEGA},
		{in: "https://mega.nz/aff=123", err: errNotMEGA},
		{in: "https://mega.nz/file/Ab<d#x", err: errMalformedLink},
		{in: "https://mega.nz/file/AbCd1234#ke.y", err: errMalformedLink},
	}
	for _, tt := range tests {
		u, err := url.Parse(tt.in)
		if err != nil {
			t.Fatalf("%s: %v", tt.in, err)
		}
		got, err := parseLink(u)
		if tt.err != nil {
			if !errors.Is(err, tt.err) {
				t.Errorf("%s: err = %v, want %v", tt.in, err, tt.err)
			}
			continue
		}
		if err != nil || got != tt.want {
			t.Errorf("%s: got %+v, %v; want %+v", tt.in, got, err, tt.want)
		}
	}
}

func TestMatch(t *testing.T) {
	h, err := New(config.HosterConfig{Domains: []string{"mega.example"}})
	if err != nil {
		t.Fatal(err)
	}
	for in, want := range map[string]bool{
		"https://mega.nz/file/AbCd1234#" + tFileKey:          true,
		"https://mega.nz/#F!Fold_-12!" + tFolderKey:          true,
		"https://mega.nz/#P!AgAAAAAAAAAA":                    true, // reported as unsupported by Resolve
		"https://mega.example/file/AbCd1234#" + tFileKey:     true,
		"https://notmega.nz/file/AbCd1234#" + tFileKey:       false,
		"https://mega.nz.evil.com/file/AbCd1234#" + tFileKey: false,
		"https://mega.nz/":                                   false,
		"https://mega.nz/help":                               false,
		"ftp://mega.nz/file/AbCd1234#" + tFileKey:            false,
	} {
		u, _ := url.Parse(in)
		if got := h.Match(u); got != want {
			t.Errorf("Match(%s) = %v, want %v", in, got, want)
		}
	}
	if h.Match(nil) {
		t.Error("Match(nil)")
	}
}

func TestSafeComponent(t *testing.T) {
	for in, want := range map[string]string{
		"ok.txt":           "ok.txt",
		"../../etc/passwd": ".._.._etc_passwd",
		`a\b`:              "a_b",
		"nul\x00byte":      "nul_byte",
		"":                 "H",
		".":                "H",
		"..":               "H",
		"   ":              "H",
	} {
		if got := safeComponent(in, "H"); got != want {
			t.Errorf("safeComponent(%q) = %q, want %q", in, got, want)
		}
	}
}
