package linkgrabber

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"testing"

	"admindescargas/internal/hosters"
)

func TestSelect(t *testing.T) {
	items := []Item{
		{Index: 0, RelPath: "S1/ep01.mkv"},
		{Index: 1, RelPath: "S1/ep02.mkv"},
		{Index: 2, RelPath: "S1/subs/ep01.srt"},
		{Index: 3, RelPath: "S2/ep01.mkv"},
		{Index: 4, RelPath: "readme.txt"},
		{Index: 5, RelPath: "S10/x.mkv"},
	}
	for _, tc := range []struct {
		name             string
		include, exclude []string
		idx              []int
		want             []int
	}{
		{"all", nil, nil, nil, []int{0, 1, 2, 3, 4, 5}},
		{"folder slash", []string{"S1/"}, nil, nil, []int{0, 1, 2}},
		{"folder no slash is not S10", []string{"S1"}, nil, nil, []int{0, 1, 2}},
		{"exact file", []string{"readme.txt"}, nil, nil, []int{4}},
		{"exclude subfolder", []string{"S1/"}, []string{"S1/subs/"}, nil, []int{0, 1}},
		{"glob ext", []string{"*.mkv"}, nil, nil, []int{0, 1, 3, 5}},
		{"glob full path", []string{"S1/*.mkv"}, nil, nil, []int{0, 1}},
		{"exclude only", nil, []string{"*.srt", "readme.txt"}, nil, []int{0, 1, 3, 5}},
		{"indexes win", []string{"S2/"}, nil, []int{4, 1, 99}, []int{1, 4}},
		{"backslashes", []string{`S1\subs\`}, nil, nil, []int{2}},
		{"traversal pattern sanitized", []string{"../S2/"}, nil, nil, []int{3}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Select(items, tc.include, tc.exclude, tc.idx); !slices.Equal(got, tc.want) {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
	loose := []Item{{Index: 0, Name: "a.zip"}, {Index: 1, Name: "b.rar"}}
	if got := Select(loose, []string{"*.zip"}, nil, nil); !slices.Equal(got, []int{0}) {
		t.Fatalf("loose = %v", got)
	}
}

type plainHoster struct{ files []hosters.FileInfo }

func (plainHoster) Name() string        { return "plain" }
func (plainHoster) Match(*url.URL) bool { return true }
func (plainHoster) Caps() hosters.Caps  { return hosters.Caps{} }
func (h plainHoster) Resolve(context.Context, *hosters.Session, string) (*hosters.ResolveResult, error) {
	return &hosters.ResolveResult{Name: "Folder", IsFolder: true, Files: h.files}, nil
}
func (plainHoster) Prepare(context.Context, *hosters.Session, hosters.FileInfo) (*hosters.DownloadSpec, error) {
	return nil, nil
}
func (plainHoster) Classify(r *http.Response, b []byte, err error) error {
	return hosters.DefaultClassify(r, b, err)
}

type streamHoster struct{ plainHoster }

func (h streamHoster) ResolveStream(_ context.Context, _ *hosters.Session, _ string, emit func(hosters.FileInfo)) (*hosters.ResolveResult, error) {
	for _, f := range h.files {
		emit(f)
	}
	return &hosters.ResolveResult{Name: "Streamed", IsFolder: true, Files: h.files}, nil
}

func TestResolveEmits(t *testing.T) {
	files := []hosters.FileInfo{{Name: "a"}, {Name: "b"}}
	for _, h := range []hosters.Hoster{plainHoster{files}, streamHoster{plainHoster{files}}} {
		var got []string
		res, err := Resolve(context.Background(), h, nil, "x", func(f hosters.FileInfo) { got = append(got, f.Name) })
		if err != nil || len(res.Files) != 2 || !slices.Equal(got, []string{"a", "b"}) {
			t.Fatalf("%T: %v %v %v", h, res, got, err)
		}
	}
}

func TestPackageNameAndNormalize(t *testing.T) {
	if n := PackageName(&hosters.ResolveResult{Name: "Show"}, ""); n != "Show" {
		t.Fatal(n)
	}
	if n := PackageName(&hosters.ResolveResult{Files: []hosters.FileInfo{{Name: "f.zip"}}}, ""); n != "f.zip" {
		t.Fatal(n)
	}
	if n := PackageName(nil, "https://example.com/dir/my%20file.bin?x=1"); n != "my file.bin" {
		t.Fatal(n)
	}
	for in, want := range map[string]string{
		" HTTPS://Mega.NZ:443/file/AbC#KeY ": "https://mega.nz/file/AbC#KeY",
		"http://Example.com:80/a?b=C":        "http://example.com/a?b=C",
		"not a url":                          "not a url",
	} {
		if got := NormalizeURL(in); got != want {
			t.Errorf("NormalizeURL(%q) = %q want %q", in, got, want)
		}
	}
}
