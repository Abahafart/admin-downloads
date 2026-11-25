package api

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"admindescargas/internal/core"
)

// engineOr answers 501 when no engine is wired.
func (s *server) engine(w http.ResponseWriter) core.Engine {
	if s.d.Engine == nil {
		notImplemented(w)
	}
	return s.d.Engine
}

func nonNil[T any](v []T) []T {
	if v == nil {
		return []T{}
	}
	return v
}

func (s *server) addLinks(w http.ResponseWriter, r *http.Request) {
	e := s.engine(w)
	if e == nil {
		return
	}
	var req core.AddLinksRequest
	if err := decode(w, r, maxBigBody, &req, false); err != nil {
		s.fail(w, r, err)
		return
	}
	if strings.TrimSpace(req.Text) == "" && len(req.URLs) == 0 {
		s.fail(w, r, badRequest("no hay enlaces: envía text o urls"))
		return
	}
	if req.Source == "" {
		req.Source = "api"
	}
	out, err := e.AddLinks(r.Context(), req)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, nonNil(out))
}

func (s *server) listGrabber(w http.ResponseWriter, r *http.Request) {
	e := s.engine(w)
	if e == nil {
		return
	}
	out, err := e.ListLinkGrabber(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, nonNil(out))
}

func (s *server) confirmGrabber(w http.ResponseWriter, r *http.Request) {
	e := s.engine(w)
	if e == nil {
		return
	}
	var req core.ConfirmRequest
	if err := decode(w, r, maxBody, &req, false); err != nil {
		s.fail(w, r, err)
		return
	}
	if len(req.Items) == 0 {
		s.fail(w, r, badRequest("items está vacío"))
		return
	}
	out, err := e.ConfirmLinkGrabber(r.Context(), req)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, nonNil(out))
}

// readIDs decodes an IDsRequest and requires at least one id.
func readIDs(w http.ResponseWriter, r *http.Request) (IDsRequest, error) {
	var req IDsRequest
	if err := decode(w, r, maxBody, &req, false); err != nil {
		return req, err
	}
	if len(req.IDs) == 0 {
		return req, badRequest("ids está vacío")
	}
	return req, nil
}

func (s *server) removeGrabber(w http.ResponseWriter, r *http.Request) {
	e := s.engine(w)
	if e == nil {
		return
	}
	req, err := readIDs(w, r)
	if err == nil {
		err = e.RemoveLinkGrabber(r.Context(), req.IDs)
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeOK(w)
}

func (s *server) listDownloads(w http.ResponseWriter, r *http.Request) {
	e := s.engine(w)
	if e == nil {
		return
	}
	q := r.URL.Query()
	var f core.DownloadFilter
	for _, v := range q["state"] {
		for _, st := range strings.Split(v, ",") {
			if st = strings.TrimSpace(st); st == "" {
				continue
			}
			if !core.State(st).Valid() {
				s.fail(w, r, badRequest("estado %q desconocido", st))
				return
			}
			f.States = append(f.States, core.State(st))
		}
	}
	if v := q.Get("package_id"); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil || id <= 0 {
			s.fail(w, r, badRequest("package_id %q no válido", v))
			return
		}
		f.PackageID = id
	}
	f.Hoster = q.Get("hoster")
	out, err := e.ListDownloads(r.Context(), f)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if text := strings.ToLower(strings.TrimSpace(q.Get("q"))); text != "" {
		kept := out[:0]
		for _, d := range out {
			if strings.Contains(strings.ToLower(d.Name), text) ||
				strings.Contains(strings.ToLower(d.RelPath), text) ||
				strings.Contains(strings.ToLower(d.URL), text) {
				kept = append(kept, d)
			}
		}
		out = kept
	}
	writeJSON(w, http.StatusOK, nonNil(out))
}

func (s *server) getDownload(w http.ResponseWriter, r *http.Request) {
	e := s.engine(w)
	if e == nil {
		return
	}
	id, err := pathID(r, "id")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	d, err := e.GetDownload(r.Context(), id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

// Actions shared by downloads and packages.
const (
	actionPause   = "pause"
	actionResume  = "resume"
	actionRestart = "restart"
	actionRetry   = "retry"
	actionDelete  = "delete"
)

type actionSet struct {
	pause, resume, restart, retry func(context.Context, []int64) error
	del                           func(context.Context, []int64, bool) error
}

func (s *server) runAction(w http.ResponseWriter, r *http.Request, set actionSet) {
	action := r.PathValue("action")
	var run func(ctx context.Context, req IDsRequest) error
	switch action {
	case actionPause:
		run = func(ctx context.Context, req IDsRequest) error { return set.pause(ctx, req.IDs) }
	case actionResume:
		run = func(ctx context.Context, req IDsRequest) error { return set.resume(ctx, req.IDs) }
	case actionRestart:
		run = func(ctx context.Context, req IDsRequest) error { return set.restart(ctx, req.IDs) }
	case actionRetry:
		run = func(ctx context.Context, req IDsRequest) error { return set.retry(ctx, req.IDs) }
	case actionDelete:
		run = func(ctx context.Context, req IDsRequest) error { return set.del(ctx, req.IDs, req.DeleteFiles) }
	default:
		writeError(w, http.StatusNotFound, CodeNotFound, "acción desconocida: "+action)
		return
	}
	req, err := readIDs(w, r)
	if err == nil {
		err = run(r.Context(), req)
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeOK(w)
}

func (s *server) downloadAction(w http.ResponseWriter, r *http.Request) {
	e := s.engine(w)
	if e == nil {
		return
	}
	s.runAction(w, r, actionSet{
		pause: e.PauseDownloads, resume: e.ResumeDownloads, restart: e.RestartDownloads,
		retry: e.RetryDownloads, del: e.DeleteDownloads,
	})
}

func (s *server) retryFailed(w http.ResponseWriter, r *http.Request) {
	s.simple(w, r, func(e core.Engine, ctx context.Context) error { return e.RetryAllFailed(ctx) })
}

func (s *server) pauseAll(w http.ResponseWriter, r *http.Request) {
	s.simple(w, r, func(e core.Engine, ctx context.Context) error { return e.PauseAll(ctx) })
}

func (s *server) resumeAll(w http.ResponseWriter, r *http.Request) {
	s.simple(w, r, func(e core.Engine, ctx context.Context) error { return e.ResumeAll(ctx) })
}

func (s *server) simple(w http.ResponseWriter, r *http.Request, fn func(core.Engine, context.Context) error) {
	e := s.engine(w)
	if e == nil {
		return
	}
	if err := fn(e, r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}
	writeOK(w)
}

func (s *server) listPackages(w http.ResponseWriter, r *http.Request) {
	e := s.engine(w)
	if e == nil {
		return
	}
	out, err := e.ListPackages(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, nonNil(out))
}

func (s *server) getPackage(w http.ResponseWriter, r *http.Request) {
	e := s.engine(w)
	if e == nil {
		return
	}
	id, err := pathID(r, "id")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	p, ds, err := e.GetPackage(r.Context(), id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, PackageDetail{Package: p, Downloads: nonNil(ds)})
}

func (s *server) packageAction(w http.ResponseWriter, r *http.Request) {
	e := s.engine(w)
	if e == nil {
		return
	}
	s.runAction(w, r, actionSet{
		pause: e.PausePackages, resume: e.ResumePackages, restart: e.RestartPackages,
		retry: e.RetryPackages, del: e.DeletePackages,
	})
}

func (s *server) rescanPackage(w http.ResponseWriter, r *http.Request) {
	e := s.engine(w)
	if e == nil {
		return
	}
	id, err := pathID(r, "id")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out, err := e.RescanPackage(r.Context(), id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, nonNil(out))
}

func (s *server) stats(w http.ResponseWriter, r *http.Request) {
	e := s.engine(w)
	if e == nil {
		return
	}
	st, err := e.Stats(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}
