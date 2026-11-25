package api

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"admindescargas/internal/core"
	"admindescargas/internal/rotation"
)

// userinfoRe matches credentials embedded in URLs ("scheme://user:pass@").
var userinfoRe = regexp.MustCompile(`([A-Za-z][A-Za-z0-9+.-]*://[^\s:/@]*):[^\s@/]*@`)

// redactCreds hides passwords of URLs embedded in free text (labels and
// errors of proxies may contain them).
func redactCreds(s string) string {
	if !strings.Contains(s, "@") {
		return s
	}
	if u, err := url.Parse(s); err == nil && u.User != nil {
		if _, has := u.User.Password(); has {
			return u.Redacted()
		}
	}
	return userinfoRe.ReplaceAllString(s, "$1:xxxxx@")
}

func sanitizeEgress(st rotation.EgressStatus) rotation.EgressStatus {
	st.Label = redactCreds(st.Label)
	st.LastError = redactCreds(st.LastError)
	return st
}

func (s *server) rot(w http.ResponseWriter) rotation.Manager {
	if s.d.Rotation == nil {
		notImplemented(w)
	}
	return s.d.Rotation
}

func (s *server) listEgress(w http.ResponseWriter, r *http.Request) {
	m := s.rot(w)
	if m == nil {
		return
	}
	list := m.Egresses()
	out := make([]rotation.EgressStatus, 0, len(list))
	for _, st := range list {
		out = append(out, sanitizeEgress(st))
	}
	writeJSON(w, http.StatusOK, out)
}

func sanitizeImport(res ImportResult) ImportResult {
	for i := range res.Errors {
		res.Errors[i].Message = redactCreds(res.Errors[i].Message)
	}
	return res
}

func (s *server) importProxies(w http.ResponseWriter, r *http.Request) {
	if s.d.Egress == nil {
		notImplemented(w)
		return
	}
	var req ImportTextRequest
	if err := decode(w, r, maxBigBody, &req, false); err != nil {
		s.fail(w, r, err)
		return
	}
	if strings.TrimSpace(req.Text) == "" {
		s.fail(w, r, badRequest("text está vacío"))
		return
	}
	res, err := s.d.Egress.ImportProxies(r.Context(), req.Text)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, sanitizeImport(res))
}

func (s *server) importWireGuard(w http.ResponseWriter, r *http.Request) {
	if s.d.Egress == nil {
		notImplemented(w)
		return
	}
	var req WireGuardImportRequest
	if err := decode(w, r, maxBigBody, &req, false); err != nil {
		s.fail(w, r, err)
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" || strings.TrimSpace(req.Conf) == "" {
		s.fail(w, r, badRequest("faltan name o conf"))
		return
	}
	res, err := s.d.Egress.ImportWireGuard(r.Context(), req.Name, req.Conf)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, sanitizeImport(res))
}

func (s *server) testEgress(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if s.d.Egress != nil {
		st, err := s.d.Egress.Test(r.Context(), id)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, sanitizeEgress(st))
		return
	}
	// Fallback: query the public IP through the egress.
	if s.d.Rotation == nil || s.d.PublicIP == nil {
		notImplemented(w)
		return
	}
	e := s.d.Rotation.Get(id)
	if e == nil {
		s.fail(w, r, fmt.Errorf("salida %q: %w", id, core.ErrNotFound))
		return
	}
	st := rotation.EgressStatus{ID: e.ID(), Kind: e.Kind(), Scope: e.Scope(), CheckedAt: s.now()}
	for _, cur := range s.d.Rotation.Egresses() {
		if cur.ID == id {
			st = cur
			st.CheckedAt = s.now()
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	ip, err := s.d.PublicIP(ctx, e)
	if err != nil {
		st.Healthy, st.LastError = false, err.Error()
	} else {
		st.Healthy, st.PublicIP, st.LastError = true, rotation.FormatIP(ip), ""
	}
	writeJSON(w, http.StatusOK, sanitizeEgress(st))
}

func (s *server) rotate(w http.ResponseWriter, r *http.Request) {
	m := s.rot(w)
	if m == nil {
		return
	}
	var req RotateRequest
	if err := decode(w, r, maxBody, &req, true); err != nil {
		s.fail(w, r, err)
		return
	}
	req.EgressID = strings.TrimSpace(req.EgressID)
	if req.EgressID != "" && m.Get(req.EgressID) == nil {
		s.fail(w, r, fmt.Errorf("salida %q: %w", req.EgressID, core.ErrNotFound))
		return
	}
	if err := m.Rotate(r.Context(), req.EgressID); err != nil {
		s.fail(w, r, err)
		return
	}
	writeOK(w)
}

func (s *server) publicIPs(w http.ResponseWriter, r *http.Request) {
	m := s.rot(w)
	if m == nil {
		return
	}
	list := m.Egresses()
	out := make([]EgressIP, len(list))
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	for i, st := range list {
		out[i] = EgressIP{ID: st.ID, Kind: st.Kind, PublicIP: st.PublicIP}
		if s.d.PublicIP == nil {
			continue // report the last known IP
		}
		e := m.Get(st.ID)
		if e == nil {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			ip, err := s.d.PublicIP(ctx, e)
			if err != nil {
				out[i].Error = redactCreds(err.Error())
				return
			}
			out[i].PublicIP = rotation.FormatIP(ip)
		}()
	}
	wg.Wait()
	writeJSON(w, http.StatusOK, IPResponse{Egresses: out})
}

func (s *server) getSettings(w http.ResponseWriter, r *http.Request) {
	if s.d.Settings == nil {
		notImplemented(w)
		return
	}
	st, err := s.d.Settings.Get(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *server) putSettings(w http.ResponseWriter, r *http.Request) {
	if s.d.Settings == nil {
		notImplemented(w)
		return
	}
	// Decode on top of the current view so partial bodies keep the rest.
	cur, err := s.d.Settings.Get(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err := decode(w, r, maxBody, &cur, false); err != nil {
		s.fail(w, r, err)
		return
	}
	out, err := s.d.Settings.Put(r.Context(), cur)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *server) selfTest(w http.ResponseWriter, r *http.Request) {
	if s.d.SelfTest == nil {
		notImplemented(w)
		return
	}
	res, err := s.d.SelfTest.SelfTest(r.Context(), r.PathValue("hoster"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	res.Error = redactCreds(res.Error)
	writeJSON(w, http.StatusOK, res)
}
