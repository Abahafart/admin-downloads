package mediafire

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"

	"admindescargas/internal/fsutil"
	"admindescargas/internal/hosters"
)

// flexInt decodes numbers MediaFire sends either as JSON numbers or strings.
type flexInt int64

func (n *flexInt) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(bytes.TrimSpace(b)), `"`)
	if s == "" || s == "null" {
		*n = 0
		return nil
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return fmt.Errorf("mediafire: bad number %q", s)
	}
	*n = flexInt(v)
	return nil
}

// apiEnvelope is the common shape of every api/1.5 reply.
type apiEnvelope struct {
	Response struct {
		Result        string         `json:"result"`
		Error         flexInt        `json:"error"`
		Message       string         `json:"message"`
		FileInfo      *apiFile       `json:"file_info"`
		FolderInfo    *apiFolder     `json:"folder_info"`
		FolderContent *folderContent `json:"folder_content"`
		UserInfo      *apiUser       `json:"user_info"`
		SessionToken  string         `json:"session_token"`
	} `json:"response"`
}

type apiFile struct {
	QuickKey          string  `json:"quickkey"`
	Filename          string  `json:"filename"`
	Size              flexInt `json:"size"`
	Hash              string  `json:"hash"`
	PasswordProtected string  `json:"password_protected"`
	Links             struct {
		NormalDownload string `json:"normal_download"`
	} `json:"links"`
}

type apiFolder struct {
	FolderKey string `json:"folderkey"`
	Name      string `json:"name"`
}

type folderContent struct {
	ChunkNumber flexInt     `json:"chunk_number"`
	MoreChunks  string      `json:"more_chunks"`
	Files       []apiFile   `json:"files"`
	Folders     []apiFolder `json:"folders"`
}

type apiUser struct {
	Email       string `json:"email"`
	DisplayName string `json:"display_name"`
	Premium     string `json:"premium"`
	UsedStorage string `json:"used_storage_size"`
	StorageLim  string `json:"storage_limit"`
}

// toFileInfo converts an API file to a FileInfo. For files listed inside
// a folder (inFolder) RelPath is prefix (the sanitized folder path, "" at
// the root) joined with the sanitized name.
func (f apiFile) toFileInfo(webBase, prefix string, inFolder bool) hosters.FileInfo {
	name := f.Filename
	if name == "" {
		name = f.QuickKey
	}
	fi := hosters.FileInfo{
		ID:   f.QuickKey,
		Name: name,
		Size: int64(f.Size),
		Link: webBase + "/file/" + f.QuickKey,
	}
	if f.Links.NormalDownload != "" {
		if u, err := url.Parse(f.Links.NormalDownload); err == nil && u.Scheme == "https" {
			fi.Link = f.Links.NormalDownload
		}
	}
	if inFolder {
		fi.RelPath = path.Join(prefix, fsutil.SanitizeName(name))
	}
	if h := strings.ToLower(strings.TrimSpace(f.Hash)); len(h) == 64 && isHex(h) {
		fi.Checksum = &hosters.Checksum{Algo: hosters.AlgoSHA256, Value: h}
	}
	if strings.EqualFold(f.PasswordProtected, "yes") {
		fi.Meta = map[string]string{"password_protected": "yes"}
	}
	return fi
}

func isHex(s string) bool {
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// apiCall performs GET <apiBase><p>?<params>&response_format=json.
func (h *Hoster) apiCall(ctx context.Context, s *hosters.Session, p string, params url.Values) (*apiEnvelope, error) {
	if err := h.limiter.Wait(ctx); err != nil {
		return nil, hosters.NewFatal(err)
	}
	params.Set("response_format", "json")
	if tok, ok := s.Value(valSessionToken); ok {
		if t, _ := tok.(string); t != "" {
			params.Set("session_token", t)
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.apiBase+p+"?"+params.Encode(), nil)
	if err != nil {
		return nil, hosters.NewFatal(fmt.Errorf("mediafire: build request: %w", err))
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json")
	resp, err := s.Client.Do(req)
	if err != nil {
		return nil, h.Classify(nil, nil, fmt.Errorf("mediafire api %s: %w", p, redactURLError(err)))
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxPageBytes))
	if err != nil {
		return nil, hosters.NewTemporary(0, fmt.Errorf("mediafire api %s: read: %w", p, err))
	}
	var env apiEnvelope
	jsonErr := json.Unmarshal(body, &env)
	if jsonErr == nil && strings.EqualFold(env.Response.Result, "error") {
		return nil, apiError(p, int(env.Response.Error), env.Response.Message)
	}
	if resp.StatusCode >= 400 {
		if err := h.Classify(resp, head(body), nil); err != nil {
			return nil, fmt.Errorf("mediafire api %s: %w", p, err)
		}
	}
	if jsonErr != nil {
		if isCaptcha(body) {
			return nil, captchaError()
		}
		return nil, hosters.NewTemporary(0, fmt.Errorf("mediafire api %s: unexpected reply: %w", p, jsonErr))
	}
	if !strings.EqualFold(env.Response.Result, "success") {
		return nil, hosters.NewTemporary(0, fmt.Errorf("mediafire api %s: result %q", p, env.Response.Result))
	}
	return &env, nil
}

// apiError maps an api/1.5 error code to a typed error.
func apiError(p string, code int, msg string) error {
	err := fmt.Errorf("mediafire api %s: error %d: %s", p, code, msg)
	switch {
	case notFoundCodes[code]:
		return hosters.NewNotFound(err)
	case rateLimitCodes[code]:
		return hosters.NewQuota(hosters.ScopeIP, captchaRetryAfter, err)
	case code == 105 || code == 107: // session token invalid / expired
		return hosters.NewNeedsAuth(err)
	default:
		return hosters.NewFatal(err)
	}
}

func (h *Hoster) fileInfo(ctx context.Context, s *hosters.Session, key string) (*apiFile, error) {
	env, err := h.apiCall(ctx, s, pathFileInfo, url.Values{"quick_key": {key}})
	if err != nil {
		return nil, err
	}
	fi := env.Response.FileInfo
	if fi == nil || fi.QuickKey == "" {
		return nil, hosters.NewNotFound(fmt.Errorf("mediafire: file %s not found", key))
	}
	return fi, nil
}

func (h *Hoster) folderInfo(ctx context.Context, s *hosters.Session, key string) (*apiFolder, error) {
	env, err := h.apiCall(ctx, s, pathFolderInfo, url.Values{"folder_key": {key}})
	if err != nil {
		return nil, err
	}
	fi := env.Response.FolderInfo
	if fi == nil {
		return nil, hosters.NewNotFound(fmt.Errorf("mediafire: folder %s not found", key))
	}
	return fi, nil
}

// folderContent lists every chunk of one content type of a folder.
func (h *Hoster) folderContent(ctx context.Context, s *hosters.Session, key, contentType string) (*folderContent, error) {
	all := &folderContent{}
	for chunk := 1; ; chunk++ {
		env, err := h.apiCall(ctx, s, pathFolderList, url.Values{
			"folder_key":   {key},
			"content_type": {contentType},
			"chunk":        {strconv.Itoa(chunk)},
		})
		if err != nil {
			return nil, err
		}
		fc := env.Response.FolderContent
		if fc == nil {
			return nil, hosters.NewTemporary(0, errors.New("mediafire: folder content missing in reply"))
		}
		all.Files = append(all.Files, fc.Files...)
		all.Folders = append(all.Folders, fc.Folders...)
		if !strings.EqualFold(fc.MoreChunks, "yes") || (len(fc.Files) == 0 && len(fc.Folders) == 0) {
			return all, nil
		}
		if chunk > 100000 {
			return nil, hosters.NewFatal(errors.New("mediafire: folder pagination does not end"))
		}
	}
}

func head(b []byte) []byte {
	if len(b) > 8<<10 {
		return b[:8<<10]
	}
	return b
}
