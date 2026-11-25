package linkgrabber

import (
	"path"
	"slices"
	"strings"

	"admindescargas/internal/fsutil"
)

// Item is one file of a resolved entry, as seen by Select.
type Item struct {
	// Index is the stable index of the file in its entry.
	Index int
	// RelPath is the sanitized slash-separated path inside the folder ("" for
	// a loose file).
	RelPath string
	// Name is the file name (used when RelPath is empty).
	Name string
	Size int64
}

func (it Item) key() string {
	if it.RelPath != "" {
		return it.RelPath
	}
	return fsutil.SanitizeName(it.Name)
}

// Select returns the Index of every item chosen by a ConfirmItem-like
// selection, in input order:
//
//   - indexes, if non-empty, selects exactly those items (Include/Exclude
//     are ignored);
//   - otherwise include (empty = everything) then exclude are applied.
//
// A pattern matches an item when it is equal to its RelPath, when it names
// a folder prefix ("S1" or "S1/" match "S1/ep01.mkv"), or when it contains
// glob metacharacters (*, ?, [) and path.Match accepts it against the full
// RelPath or the base name ("*.mkv"). Patterns are compared on the
// sanitized form, so they may be given exactly as shown in the UI.
func Select(items []Item, include, exclude []string, indexes []int) []int {
	var out []int
	if len(indexes) > 0 {
		for _, it := range items {
			if slices.Contains(indexes, it.Index) {
				out = append(out, it.Index)
			}
		}
		return out
	}
	inc := compile(include)
	exc := compile(exclude)
	for _, it := range items {
		k := it.key()
		if len(inc) > 0 && !matchAny(inc, k) {
			continue
		}
		if matchAny(exc, k) {
			continue
		}
		out = append(out, it.Index)
	}
	return out
}

type pattern struct {
	glob   bool
	value  string
	folder bool // explicit trailing slash
}

func compile(ps []string) []pattern {
	var out []pattern
	for _, p := range ps {
		p = strings.TrimSpace(strings.ReplaceAll(p, `\`, "/"))
		if p == "" {
			continue
		}
		if strings.ContainsAny(p, "*?[") {
			out = append(out, pattern{glob: true, value: strings.TrimPrefix(p, "/")})
			continue
		}
		folder := strings.HasSuffix(p, "/")
		v := fsutil.SanitizeRelPath(p)
		if v == "" {
			continue
		}
		out = append(out, pattern{value: v, folder: folder})
	}
	return out
}

func matchAny(ps []pattern, key string) bool {
	for _, p := range ps {
		if p.glob {
			if ok, _ := path.Match(p.value, key); ok {
				return true
			}
			if ok, _ := path.Match(p.value, path.Base(key)); ok {
				return true
			}
			continue
		}
		if !p.folder && key == p.value {
			return true
		}
		if strings.HasPrefix(key, p.value+"/") {
			return true
		}
	}
	return false
}
