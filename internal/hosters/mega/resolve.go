package mega

import (
	"context"
	"crypto/cipher"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"admindescargas/internal/hosters"
)

// FileInfo.Meta keys.
const (
	metaKey  = "key"  // base64url node key (32 bytes)
	metaRoot = "root" // shared folder handle for files inside a folder link
)

// apiNode is one entry of an "f" listing.
type apiNode struct {
	H  string `json:"h"`
	P  string `json:"p"`
	T  int    `json:"t"`
	A  string `json:"a"`
	K  string `json:"k"`
	S  int64  `json:"s"`
	TS int64  `json:"ts"`
}

// gResponse is the answer to {"a":"g"}.
type gResponse struct {
	S  int64           `json:"s"`
	At string          `json:"at"`
	G  json.RawMessage `json:"g"`
}

// url returns the storage URL ("g" may be a string or a list of strings).
func (g gResponse) url() string {
	var s string
	if json.Unmarshal(g.G, &s) == nil {
		return s
	}
	var l []string
	if json.Unmarshal(g.G, &l) == nil && len(l) > 0 {
		return l[0]
	}
	return ""
}

// Resolve implements hosters.Hoster.
func (p *Plugin) Resolve(ctx context.Context, s *hosters.Session, link string) (*hosters.ResolveResult, error) {
	return p.ResolveStream(ctx, s, link, nil)
}

// ResolveStream implements hosters.StreamingResolver. MEGA returns a whole
// folder tree in one call, so files are emitted once the tree is decrypted.
func (p *Plugin) ResolveStream(ctx context.Context, s *hosters.Session, link string, emit func(hosters.FileInfo)) (*hosters.ResolveResult, error) {
	u, err := url.Parse(strings.TrimSpace(link))
	if err != nil || !p.Match(u) {
		return nil, hosters.NewFatal(errNotMEGA)
	}
	l, err := parseLink(u)
	if err != nil {
		return nil, hosters.NewFatal(err)
	}
	if l.Key == "" {
		return nil, hosters.NewFatal(errMissingKey)
	}
	var res *hosters.ResolveResult
	if l.Kind == linkFile {
		res, err = p.resolveFile(ctx, s, l)
	} else {
		res, err = p.resolveFolder(ctx, s, l)
	}
	if err != nil {
		return nil, err
	}
	if emit != nil {
		for _, f := range res.Files {
			emit(f)
		}
	}
	return res, nil
}

func (p *Plugin) resolveFile(ctx context.Context, s *hosters.Session, l megaLink) (*hosters.ResolveResult, error) {
	raw, err := b64Decode(l.Key)
	if err != nil || len(raw) != 32 {
		return nil, hosters.NewFatal(fmt.Errorf("%w: file links need a 43-character key", errBadKey))
	}
	k, _ := unpackFileKey(raw)
	res, err := p.call(ctx, s, nil, map[string]any{"a": "g", "p": l.Handle})
	if err != nil {
		return nil, err
	}
	var g gResponse
	if err := json.Unmarshal(res, &g); err != nil {
		return nil, hosters.NewTemporary(0, fmt.Errorf("mega: decoding file info: %w", err))
	}
	attrs, err := decryptAttrs(k.AES[:], g.At)
	if err != nil {
		return nil, hosters.NewFatal(fmt.Errorf("mega: wrong decryption key for this link: %w", err))
	}
	name := safeComponent(attrs.Name, l.Handle)
	key := b64Encode(raw)
	f := hosters.FileInfo{
		ID:       l.Handle,
		Name:     name,
		Size:     g.S,
		Checksum: &hosters.Checksum{Algo: hosters.AlgoMEGAMAC, Value: key},
		Link:     fileLink(l.Handle, key),
		Meta:     map[string]string{metaKey: key},
	}
	return &hosters.ResolveResult{Files: []hosters.FileInfo{f}}, nil
}

func (p *Plugin) resolveFolder(ctx context.Context, s *hosters.Session, l megaLink) (*hosters.ResolveResult, error) {
	folderKey, err := b64Decode(l.Key)
	if err != nil || len(folderKey) != 16 {
		return nil, hosters.NewFatal(fmt.Errorf("%w: folder links need a 22-character key", errBadKey))
	}
	res, err := p.call(ctx, s, url.Values{"n": {l.Handle}}, map[string]any{"a": "f", "c": 1, "r": 1, "ca": 1})
	if err != nil {
		return nil, err
	}
	var listing struct {
		F []apiNode `json:"f"`
	}
	if err := json.Unmarshal(res, &listing); err != nil {
		return nil, hosters.NewTemporary(0, fmt.Errorf("mega: decoding folder listing: %w", err))
	}
	t := buildTree(listing.F, folderKey, l.Handle)
	r, err := t.result(l)
	if t.skipped > 0 && s != nil && s.Log != nil {
		s.Log.Warn("mega: skipped undecryptable or orphan nodes", "count", t.skipped)
	}
	return r, err
}

// tree is a decrypted folder listing.
type tree struct {
	root    string
	nodes   map[string]*treeNode
	order   []string
	skipped int
}

type treeNode struct {
	handle, parent string
	dir            bool
	name           string
	decrypted      bool // attributes decrypted with the node key
	size           int64
	key            []byte // raw node key (16 bytes dirs, 32 files); nil if unknown
}

// buildTree decrypts node keys and names with the folder key. It never
// fails: nodes whose key cannot be decrypted keep their handle as name
// (files without a key are dropped later since they cannot be decrypted).
func buildTree(list []apiNode, folderKey []byte, rootHandle string) *tree {
	t := &tree{root: rootHandle, nodes: make(map[string]*treeNode, len(list))}
	for _, n := range list {
		if n.H == "" || t.nodes[n.H] != nil {
			continue
		}
		tn := &treeNode{handle: n.H, parent: n.P, dir: n.T == 1, size: n.S}
		switch n.T {
		case 0, 1:
		default:
			continue // root/inbox/trash pseudo-nodes never appear in folder links
		}
		want := 32
		if tn.dir {
			want = 16
		}
		tn.key = decryptNodeKey(n.K, folderKey, rootHandle, want)
		name := ""
		if tn.key != nil {
			if ak, err := attrKey(tn.key); err == nil {
				if a, err := decryptAttrs(ak, n.A); err == nil {
					name, tn.decrypted = a.Name, true
				}
			}
		}
		tn.name = safeComponent(name, n.H)
		t.nodes[n.H] = tn
		t.order = append(t.order, n.H)
	}
	if t.nodes[rootHandle] == nil {
		// Fall back to the only folder whose parent is not in the listing.
		var cands []string
		for _, h := range t.order {
			n := t.nodes[h]
			if n.dir && t.nodes[n.parent] == nil {
				cands = append(cands, h)
			}
		}
		if len(cands) == 1 {
			t.root = cands[0]
		}
	}
	return t
}

// decryptNodeKey picks the "<handle>:<key>" entry for the share (or any
// entry that decrypts to the expected length) and decrypts it with the
// folder key.
func decryptNodeKey(field string, folderKey []byte, rootHandle string, want int) []byte {
	var fallback []byte
	for _, part := range strings.Split(field, "/") {
		owner, enc, ok := strings.Cut(part, ":")
		if !ok {
			enc, owner = part, ""
		}
		raw, err := b64Decode(enc)
		if err != nil || len(raw) != want {
			continue
		}
		key, err := ecbDecrypt(folderKey, raw)
		if err != nil {
			continue
		}
		if owner == rootHandle {
			return key
		}
		if fallback == nil {
			fallback = key
		}
	}
	return fallback
}

// relPath returns the slash path of h below base (excluding base itself),
// or ok=false if h is not inside base.
func (t *tree) relPath(h, base string) (string, bool) {
	var parts []string
	cur := h
	for depth := 0; depth <= len(t.nodes); depth++ {
		if cur == base {
			for i, j := 0, len(parts)-1; i < j; i, j = i+1, j-1 {
				parts[i], parts[j] = parts[j], parts[i]
			}
			return strings.Join(parts, "/"), true
		}
		n := t.nodes[cur]
		if n == nil {
			return "", false
		}
		parts = append(parts, n.name)
		cur = n.parent
	}
	return "", false // cycle
}

// result selects the part of the tree the link points to.
func (t *tree) result(l megaLink) (*hosters.ResolveResult, error) {
	folderKey := l.Key
	root := t.nodes[t.root]
	if root == nil || !root.dir {
		return nil, hosters.NewNotFound(errors.New("mega: shared folder root not found in listing"))
	}
	if !root.decrypted {
		return nil, hosters.NewFatal(errors.New("mega: wrong decryption key for this folder"))
	}
	fileInfo := func(n *treeNode, rel string) hosters.FileInfo {
		key := b64Encode(n.key)
		return hosters.FileInfo{
			ID:       n.handle,
			Name:     n.name,
			RelPath:  rel,
			Size:     n.size,
			Checksum: &hosters.Checksum{Algo: hosters.AlgoMEGAMAC, Value: key},
			Link:     folderFileLink(l.Handle, folderKey, n.handle),
			Meta:     map[string]string{metaKey: key, metaRoot: l.Handle},
		}
	}
	if l.File != "" {
		n := t.nodes[l.File]
		if n == nil || n.dir {
			return nil, hosters.NewNotFound(errors.New("mega: file not found in shared folder"))
		}
		if _, ok := t.relPath(n.handle, t.root); !ok || n.key == nil {
			return nil, hosters.NewNotFound(errors.New("mega: file not found in shared folder"))
		}
		return &hosters.ResolveResult{Files: []hosters.FileInfo{fileInfo(n, "")}}, nil
	}
	base := root
	if l.SubFolder != "" {
		base = t.nodes[l.SubFolder]
		if base == nil || !base.dir {
			return nil, hosters.NewNotFound(errors.New("mega: subfolder not found in shared folder"))
		}
		if _, ok := t.relPath(base.handle, t.root); !ok {
			return nil, hosters.NewNotFound(errors.New("mega: subfolder not found in shared folder"))
		}
	}
	res := &hosters.ResolveResult{Name: base.name, IsFolder: true, Files: []hosters.FileInfo{}}
	for _, h := range t.order {
		n := t.nodes[h]
		if n.dir {
			continue
		}
		rel, ok := t.relPath(h, base.handle)
		if !ok {
			if _, inRoot := t.relPath(h, t.root); !inRoot {
				t.skipped++ // orphan
			}
			continue
		}
		if n.key == nil {
			t.skipped++
			continue
		}
		res.Files = append(res.Files, fileInfo(n, rel))
	}
	sort.SliceStable(res.Files, func(i, j int) bool { return res.Files[i].RelPath < res.Files[j].RelPath })
	return res, nil
}

// safeComponent keeps an untrusted name as a single path component: path
// separators and NULs are replaced and empty/dot names fall back to
// fallback. Full sanitizing happens downstream (fsutil).
func safeComponent(name, fallback string) string {
	name = strings.Map(func(r rune) rune {
		switch r {
		case '/', '\\', 0:
			return '_'
		}
		return r
	}, name)
	if strings.TrimSpace(name) == "" || name == "." || name == ".." {
		return fallback
	}
	return name
}

// Prepare implements hosters.Hoster: asks the API for a storage URL and
// returns a spec whose Decryptor yields the AES-CTR keystream at any offset.
func (p *Plugin) Prepare(ctx context.Context, s *hosters.Session, f hosters.FileInfo) (*hosters.DownloadSpec, error) {
	key, handle, root := f.Meta[metaKey], f.ID, f.Meta[metaRoot]
	if key == "" || handle == "" {
		// Reloaded without Meta: fall back to the link itself.
		u, err := url.Parse(f.Link)
		if err != nil {
			return nil, hosters.NewFatal(errNotMEGA)
		}
		l, err := parseLink(u)
		if err != nil {
			return nil, hosters.NewFatal(err)
		}
		switch {
		case l.Kind == linkFile:
			key, handle, root = l.Key, l.Handle, ""
		case l.File != "":
			// The node key must come from the folder listing.
			r, err := p.resolveFolder(ctx, s, l)
			if err != nil {
				return nil, err
			}
			if len(r.Files) != 1 {
				return nil, hosters.NewNotFound(errors.New("mega: file not found in shared folder"))
			}
			key, handle, root = r.Files[0].Meta[metaKey], r.Files[0].ID, l.Handle
		default:
			return nil, hosters.NewFatal(errors.New("mega: Prepare needs a file, not a folder"))
		}
	}
	k, err := parseFileKey(key)
	if err != nil {
		return nil, hosters.NewFatal(err)
	}
	cmd := map[string]any{"a": "g", "g": 1}
	if p.ssl {
		cmd["ssl"] = 2
	}
	var q url.Values
	if root != "" {
		cmd["n"] = handle
		q = url.Values{"n": {root}}
	} else {
		cmd["p"] = handle
	}
	res, err := p.call(ctx, s, q, cmd)
	if err != nil {
		return nil, err
	}
	var g gResponse
	if err := json.Unmarshal(res, &g); err != nil {
		return nil, hosters.NewTemporary(0, fmt.Errorf("mega: decoding download info: %w", err))
	}
	if g.At != "" {
		if _, err := decryptAttrs(k.AES[:], g.At); err != nil {
			return nil, hosters.NewFatal(fmt.Errorf("mega: wrong decryption key for this file: %w", err))
		}
	}
	dl := g.url()
	if dl == "" {
		return nil, hosters.NewTemporary(0, errors.New("mega: API returned no download URL"))
	}
	size := g.S
	if size <= 0 && f.Size > 0 {
		size = f.Size
	}
	spec := &hosters.DownloadSpec{
		URL:       dl,
		Size:      size,
		Resumable: true,
		MaxConns:  p.maxConns,
		Decryptor: func(offset int64) (cipher.Stream, error) { return newCTRAt(k, offset) },
		Checksum:  &hosters.Checksum{Algo: hosters.AlgoMEGAMAC, Value: key},
	}
	if f.Name == "" && g.At != "" {
		if a, err := decryptAttrs(k.AES[:], g.At); err == nil {
			spec.Filename = safeComponent(a.Name, handle)
		}
	}
	return spec, nil
}
