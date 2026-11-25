package linkgrabber

import (
	"math/rand"
	"net/url"
	"slices"
	"strings"
	"testing"
	"unicode"
)

func TestExtractURLs(t *testing.T) {
	tests := []struct {
		name string
		text string
		want []string
	}{
		{
			name: "spanish prose",
			text: "Descarga aquí: https://mega.nz/file/abc#def, y también https://www.mediafire.com/file/xyz/archivo.zip/file.",
			want: []string{"https://mega.nz/file/abc#def", "https://www.mediafire.com/file/xyz/archivo.zip/file"},
		},
		{
			name: "several hosters, mixed separators",
			text: "https://mega.nz/file/AbCdEf12#KeyKeyKey-_123 https://pixeldrain.com/u/x1,https://gofile.io/d/y2;https://1fichier.com/?abc3|https://example.com/a\n" +
				"https://drive.google.com/file/d/ID/view?usp=sharing\r\n\thttps://www.terabox.com/s/1abc",
			want: []string{
				"https://mega.nz/file/AbCdEf12#KeyKeyKey-_123",
				"https://pixeldrain.com/u/x1",
				"https://gofile.io/d/y2",
				"https://1fichier.com/?abc3",
				"https://example.com/a",
				"https://drive.google.com/file/d/ID/view?usp=sharing",
				"https://www.terabox.com/s/1abc",
			},
		},
		{
			name: "comma and space list",
			text: "https://a.example/1, https://b.example/2 , https://c.example/3",
			want: []string{"https://a.example/1", "https://b.example/2", "https://c.example/3"},
		},
		{
			name: "mega formats",
			text: "Nuevo: https://mega.nz/folder/XyZ#key/folder/sub\nViejo: https://mega.nz/#!handle!key\n" +
				"Carpeta vieja: https://mega.co.nz/#F!handle!key y mega.nz/#F!h!k!subhandle!",
			want: []string{
				"https://mega.nz/folder/XyZ#key/folder/sub",
				"https://mega.nz/#!handle!key",
				"https://mega.co.nz/#F!handle!key",
				"https://mega.nz/#F!h!k!subhandle",
			},
		},
		{
			name: "bare hosts",
			text: "drive.google.com/drive/folders/ID mediafire.com/file/abc/x.rar MEGA.NZ/file/q#w terabox.com/s/1xyz www.example.com/path",
			want: []string{
				"https://drive.google.com/drive/folders/ID",
				"https://mediafire.com/file/abc/x.rar",
				"https://MEGA.NZ/file/q#w",
				"https://terabox.com/s/1xyz",
				"https://www.example.com/path",
			},
		},
		{
			name: "terabox domains",
			text: "https://www.terabox.com/s/1abc https://1024terabox.com/s/1abc!",
			want: []string{"https://www.terabox.com/s/1abc", "https://1024terabox.com/s/1abc"},
		},
		{
			name: "markdown",
			text: "- [descarga](https://mega.nz/folder/XyZ#key) y [wiki](https://en.wikipedia.org/wiki/Foo_(bar)).\n" +
				"- [https://example.com/x](https://example.com/x \"title\")\n- **https://example.com/bold**",
			want: []string{
				"https://mega.nz/folder/XyZ#key",
				"https://en.wikipedia.org/wiki/Foo_(bar)",
				"https://example.com/x",
				"https://example.com/bold",
			},
		},
		{
			name: "html",
			text: `<p><a href="https://example.com/dl?a=1&amp;b=2">https://example.com/dl?a=1&amp;b=2</a>` +
				`<img src='https://cdn.example.com/i.png'></p>`,
			want: []string{"https://example.com/dl?a=1&b=2", "https://cdn.example.com/i.png"},
		},
		{
			name: "plain text keeps &amp;",
			text: "raw https://example.com/q?a=1&amp;b=2 text",
			want: []string{"https://example.com/q?a=1&amp;b=2"},
		},
		{
			name: "bbcode",
			text: "[url=https://mega.nz/#!handle!key]MEGA[/url] [url]mega.nz/#F!h!k!subhandle[/url] [URL]https://www.mediafire.com/file/a/b.zip[/URL]",
			want: []string{
				"https://mega.nz/#!handle!key",
				"https://mega.nz/#F!h!k!subhandle",
				"https://www.mediafire.com/file/a/b.zip",
			},
		},
		{
			name: "parentheses and brackets",
			text: "(ver https://mega.co.nz/#F!handle!key) [https://example.com/a] {https://example.com/b}",
			want: []string{"https://mega.co.nz/#F!handle!key", "https://example.com/a", "https://example.com/b"},
		},
		{
			name: "angle brackets and quotes",
			text: `<https://www.terabox.com/s/1abc> "https://example.com/q" 'https://example.com/s' «https://example.com/g» “https://example.com/c”`,
			want: []string{
				"https://www.terabox.com/s/1abc",
				"https://example.com/q",
				"https://example.com/s",
				"https://example.com/g",
				"https://example.com/c",
			},
		},
		{
			name: "trailing punctuation",
			text: "¿Viste https://example.com/file.zip? ¡Sí! https://example.com/a!!! https://example.com/b...; https://example.com/c:",
			want: []string{"https://example.com/file.zip", "https://example.com/a", "https://example.com/b", "https://example.com/c"},
		},
		{
			name: "duplicates and scheme case",
			text: "https://mega.nz/file/abc#def\nhttps://mega.nz/file/abc#def HTTPS://mega.nz/file/abc#def https://mega.nz/file/abc#other",
			want: []string{"https://mega.nz/file/abc#def", "https://mega.nz/file/abc#other"},
		},
		{
			name: "unicode path and ports",
			text: "Ver https://es.wikipedia.org/wiki/España. o http://[::1]:8080/x y http://127.0.0.1:9000/f.bin",
			want: []string{"https://es.wikipedia.org/wiki/España", "http://[::1]:8080/x", "http://127.0.0.1:9000/f.bin"},
		},
		{
			name: "url inside query is not split",
			text: "https://example.com/redirect?to=https://mega.nz/file/a#b",
			want: []string{"https://example.com/redirect?to=https://mega.nz/file/a#b"},
		},
		{
			name: "not urls",
			text: "Hola, esto no tiene enlaces. soporte@www.example.com user@mega.nz/x http:// https://. www. mega.nz ftp://www.example.com/a //www.example.com mailto:a@b.c",
			want: nil,
		},
		{name: "empty", text: "", want: nil},
		{name: "no links", text: "Solo texto normal, sin nada raro.\nOtra línea.", want: nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ExtractURLs(tc.text)
			if !slices.Equal(got, tc.want) {
				t.Errorf("ExtractURLs mismatch\n got: %q\nwant: %q", got, tc.want)
			}
		})
	}
}

// checkExtracted asserts the invariants every result must satisfy.
func checkExtracted(t *testing.T, input string, got []string) {
	t.Helper()
	seen := map[string]bool{}
	for _, u := range got {
		if seen[u] {
			t.Fatalf("duplicate %q", u)
		}
		seen[u] = true
		if strings.IndexFunc(u, unicode.IsSpace) >= 0 {
			t.Fatalf("result %q contains whitespace", u)
		}
		p, err := url.Parse(u)
		if err != nil || (p.Scheme != "http" && p.Scheme != "https") || p.Hostname() == "" {
			t.Fatalf("result %q is not an http(s) URL (input %q)", u, input)
		}
	}
}

func TestExtractURLsRandomInput(t *testing.T) {
	pieces := []string{
		"https://", "http://", "www.", "mega.nz/", "mega.nz", "#", "!", "F!", "(", ")", "[", "]", "{", "}",
		"<", ">", "\"", "'", "=\"", "&amp;", ",", ";", "|", ".", "?", ":", "/", "@", " ", "\n", "\t",
		"a", "Z", "9", "-", "_", "ñ", "日本", "€", "«", "»", "“", "\u200b", "\x00", "\xff", "\xc3", "[::1]",
		"[url=", "[/url]", "](", "example.com", "drive.google.com/", "%", "%2F", "é",
	}
	rng := rand.New(rand.NewSource(42))
	for n := 0; n < 20000; n++ {
		var b strings.Builder
		for k := rng.Intn(24); k >= 0; k-- {
			if rng.Intn(5) == 0 {
				b.WriteRune(rune(rng.Intn(0x2FFFF)))
			} else {
				b.WriteString(pieces[rng.Intn(len(pieces))])
			}
		}
		in := b.String()
		checkExtracted(t, in, ExtractURLs(in))
	}
}

func FuzzExtractURLs(f *testing.F) {
	for _, s := range []string{
		"", "https://mega.nz/file/a#b", "mega.nz/#F!h!k!s!", "[x](https://e.com/(a))", `<a href="http://e.com/?a&amp;b">`,
		"http://[", "http://[::1", "www.é", "\xff\xfe", "https://e.com/,https://f.com/",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, in string) {
		checkExtracted(t, in, ExtractURLs(in))
	})
}
