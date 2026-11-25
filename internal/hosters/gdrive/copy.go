package gdrive

import (
	"context"
	"errors"
	"fmt"

	"google.golang.org/api/drive/v3"
	"google.golang.org/api/googleapi"

	"admindescargas/internal/accounts"
	"admindescargas/internal/hosters"
)

// valCopyPrefix + original id caches the id of the temporary copy.
const valCopyPrefix = "gdrive:copy:"

// fileQuotaError is the per-file quota outcome. Rotating the IP does not
// help; an OAuth account allows the copy workaround.
func fileQuotaError(haveOAuth bool) error {
	msg := "gdrive: download quota exceeded for this file (too many users downloaded it recently). " +
		"The limit is per file, so rotating the IP will not help"
	if haveOAuth {
		msg += "; the copy workaround is disabled (copy_on_quota = false), wait for the quota to reset (usually within 24 h)"
	} else {
		msg += "; wait for the quota to reset (usually within 24 h) or connect a Google Drive account (OAuth) " +
			"so the file can be copied to your Drive and the copy downloaded"
	}
	return hosters.NewQuota(hosters.ScopeFile, 0, errors.New(msg))
}

// prepareCopy implements the quota workaround: copy the file into the
// user's Drive (or reuse an earlier copy) and download the copy.
func (h *Hoster) prepareCopy(ctx context.Context, s *hosters.Session, api *apiClient, orig *drive.File, spec *hosters.DownloadSpec) (*hosters.DownloadSpec, error) {
	copyID, err := h.findCopy(ctx, s, api, orig.Id)
	if err != nil {
		return nil, err
	}
	if copyID == "" {
		if err := h.limiter.Wait(ctx); err != nil {
			return nil, hosters.NewFatal(err)
		}
		call := api.svc.Files.Copy(orig.Id, &drive.File{
			Name:          orig.Name,
			Description:   copyDesc,
			AppProperties: map[string]string{appPropCopyOf: orig.Id},
		}).SupportsAllDrives(true).Fields("id,name,size,md5Checksum").Context(ctx)
		if orig.ResourceKey != "" {
			call.Header().Set(headerResourceKeys, rkHeader(orig.Id, orig.ResourceKey))
		}
		cp, err := call.Do()
		if err != nil {
			return nil, copyError(err)
		}
		copyID = cp.Id
		logOf(s).Info("gdrive: download quota exceeded, downloading a temporary copy from the account's Drive", "file", orig.Id, "copy", copyID)
	} else {
		logOf(s).Info("gdrive: download quota exceeded, reusing the temporary copy", "file", orig.Id, "copy", copyID)
	}
	s.SetValue(valCopyPrefix+orig.Id, copyID)

	out := *spec
	out.Header = spec.Header.Clone()
	out.Header.Del(headerResourceKeys) // the copy is ours
	out.URL = h.mediaURL(copyID, false)
	if _, _, err := h.probe(ctx, api.http, out.URL, out.Header); err != nil {
		return nil, fmt.Errorf("gdrive: download of the temporary copy failed: %w", err)
	}
	return &out, nil
}

// copyError maps files.copy failures.
func copyError(err error) error {
	var ge *googleapi.Error
	if errors.As(err, &ge) {
		for _, e := range ge.Errors {
			switch e.Reason {
			case "storageQuotaExceeded":
				return hosters.NewQuota(hosters.ScopeAccount, 0,
					errors.New("gdrive: download quota exceeded and the file cannot be copied: the account's Drive storage is full"))
			case "cannotCopyFile", "insufficientFilePermissions", "fileNotDownloadable", "forbidden":
				return hosters.NewQuota(hosters.ScopeFile, 0,
					fmt.Errorf("gdrive: download quota exceeded and the file cannot be copied (%s; the owner may have disabled copying)", e.Reason))
			}
		}
	}
	return apiError("files.copy", err)
}

// findCopy returns the id of an existing temporary copy of origID: the one
// cached in the session, or one found by its appProperties tag (copies made
// before a restart).
func (h *Hoster) findCopy(ctx context.Context, s *hosters.Session, api *apiClient, origID string) (string, error) {
	if v, ok := s.Value(valCopyPrefix + origID); ok {
		if id, _ := v.(string); id != "" {
			if f, err := h.getFile(ctx, api, id, ""); err == nil && !f.Trashed {
				return id, nil
			}
			s.DeleteValue(valCopyPrefix + origID)
		}
	}
	ids, err := h.listCopies(ctx, api, origID)
	if err != nil {
		return "", err
	}
	if len(ids) > 0 {
		return ids[0], nil
	}
	return "", nil
}

// listCopies finds the temporary copies of origID owned by the user.
func (h *Hoster) listCopies(ctx context.Context, api *apiClient, origID string) ([]string, error) {
	if err := h.limiter.Wait(ctx); err != nil {
		return nil, hosters.NewFatal(err)
	}
	q := fmt.Sprintf("appProperties has { key='%s' and value='%s' } and trashed = false and 'me' in owners", appPropCopyOf, escapeQ(origID))
	r, err := api.svc.Files.List().Q(q).Fields("files(id)").PageSize(100).Context(ctx).Do()
	if err != nil {
		return nil, apiError("files.list (copies)", err)
	}
	ids := make([]string, 0, len(r.Files))
	for _, f := range r.Files {
		ids = append(ids, f.Id)
	}
	return ids, nil
}

// Cleanup deletes the temporary copies made for f by the quota workaround.
// It is safe to call for any file and any session: it does nothing for
// anonymous / API key sessions, when no copy exists or when
// delete_copy = false. See the package documentation for when the engine
// should call it.
func (h *Hoster) Cleanup(ctx context.Context, s *hosters.Session, f hosters.FileInfo) error {
	if !h.deleteCopy || s == nil || s.Account == nil || s.Account.Kind != accounts.KindOAuth {
		return nil
	}
	t := targetOf(f)
	if t.id == "" {
		return nil
	}
	api, err := h.api(ctx, s)
	if err != nil || api == nil || !api.oauth {
		return err
	}
	ids := map[string]bool{}
	if v, ok := s.Value(valCopyPrefix + t.id); ok {
		if id, _ := v.(string); id != "" {
			ids[id] = true
		}
	}
	found, err := h.listCopies(ctx, api, t.id)
	if err != nil {
		return err
	}
	for _, id := range found {
		ids[id] = true
	}
	var errs []error
	for id := range ids {
		if id == t.id { // never delete the original
			continue
		}
		if err := h.limiter.Wait(ctx); err != nil {
			return hosters.NewFatal(err)
		}
		err := api.svc.Files.Delete(id).SupportsAllDrives(true).Context(ctx).Do()
		switch {
		case err == nil:
			logOf(s).Info("gdrive: temporary copy deleted", "file", t.id, "copy", id)
		case !hosters.IsNotFound(apiError("files.delete", err)):
			errs = append(errs, apiError("files.delete", err))
		}
	}
	if len(errs) == 0 {
		s.DeleteValue(valCopyPrefix + t.id)
	}
	return errors.Join(errs...)
}
