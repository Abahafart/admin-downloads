package gdrive

import (
	"context"
	"strings"

	"admindescargas/internal/accounts"
	"admindescargas/internal/hosters"
)

// CheckAccount implements hosters.AccountChecker with about.get: it
// verifies the token (refreshing and persisting it if needed) and reports
// the user's e-mail and Drive storage (QuotaTotal = -1 for unlimited).
func (h *Hoster) CheckAccount(ctx context.Context, s *hosters.Session) (*accounts.CheckResult, error) {
	res := &accounts.CheckResult{QuotaTotal: -1, QuotaUsed: -1}
	if s == nil || s.Account == nil {
		res.Status, res.Message = accounts.StatusError, "anonymous session"
		return res, nil
	}
	s.DeleteValue(valAPI) // re-read the secret: the user may have just updated it
	api, err := h.api(ctx, s)
	if err != nil {
		return checkFailure(res, err)
	}
	if api == nil {
		res.Status, res.Message = accounts.StatusInvalid, "no credentials"
		return res, nil
	}
	if !api.oauth {
		// API keys have no user: probe a nonexistent file; notFound means
		// the key itself was accepted.
		_, err := api.svc.Files.Get("0000000000000000000000000000000000").SupportsAllDrives(true).Fields("id").Context(ctx).Do()
		e := apiError("files.get", err)
		if err == nil || hosters.IsNotFound(e) {
			res.Status, res.Message = accounts.StatusOK, "API key accepted"
			return res, nil
		}
		if strings.Contains(e.Error(), "API key not valid") || strings.Contains(e.Error(), "API key expired") {
			res.Status, res.Message = accounts.StatusInvalid, "Google rejected the API key"
			return res, nil
		}
		return checkFailure(res, e)
	}
	about, err := api.svc.About.Get().Fields("user(displayName,emailAddress),storageQuota(limit,usage)").Context(ctx).Do()
	if err != nil {
		return checkFailure(res, apiError("about.get", err))
	}
	res.Status = accounts.StatusOK
	if about.User != nil {
		res.Message = about.User.EmailAddress
		if res.Message == "" {
			res.Message = about.User.DisplayName
		}
	}
	if q := about.StorageQuota; q != nil {
		if q.Limit > 0 {
			res.QuotaTotal = q.Limit
		}
		res.QuotaUsed = q.Usage
	}
	return res, nil
}

func checkFailure(res *accounts.CheckResult, err error) (*accounts.CheckResult, error) {
	res.Message = err.Error()
	switch hosters.KindOf(err) {
	case hosters.KindNeedsAuth:
		res.Status = accounts.StatusExpired
		return res, nil
	case hosters.KindQuota:
		res.Status = accounts.StatusQuota
		return res, nil
	}
	res.Status = accounts.StatusError
	return res, err
}
