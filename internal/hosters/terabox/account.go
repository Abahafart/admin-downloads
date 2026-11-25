package terabox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"admindescargas/internal/accounts"
	"admindescargas/internal/hosters"
)

// CheckAccount implements hosters.AccountChecker for cookie accounts: it
// checks that the ndus cookie is still logged in and, best effort, reads the
// premium (VIP) status. TeraBox exposes no transfer quota.
func (p *Plugin) CheckAccount(ctx context.Context, s *hosters.Session) (*accounts.CheckResult, error) {
	if s == nil || s.Anonymous() {
		return nil, hosters.ErrNoAccount
	}
	res := &accounts.CheckResult{Status: accounts.StatusOK, QuotaTotal: -1, QuotaUsed: -1}
	if _, err := p.accountCookies(ctx, s); err != nil {
		res.Status, res.Message = accounts.StatusInvalid, err.Error()
		return res, nil //nolint:nilerr // the failure is reported as the account status
	}
	r, err := p.get(ctx, s, p.accountBase+pathCheckLogin+"?"+commonParams(tokens{}).Encode(), p.accountBase+"/", true, 1<<20, http.Header{"Accept": {"application/json"}})
	if err != nil {
		return nil, err
	}
	if ae, ok := apiErrorFromBody(r.body); ok {
		herr := p.errnoError(ae)
		if hosters.IsNeedsAuth(herr) {
			res.Status, res.Message = accounts.StatusExpired, "TeraBox cookie expired: export the ndus cookie again"
			return res, nil
		}
		return nil, herr
	}
	if r.resp.StatusCode != http.StatusOK {
		if he := p.Classify(r.resp, r.body, nil); he != nil {
			return nil, he
		}
	}
	if !json.Valid(r.body) {
		return nil, hosters.NewTemporary(0, errors.New("terabox: unexpected login check response"))
	}
	res.Message = "TeraBox"
	if vip, until, err := p.membership(ctx, s); err == nil {
		res.Premium, res.PremiumUntil = vip, until
		if vip {
			res.Message = "TeraBox Premium"
		}
	} else if s.Log != nil {
		s.Log.Debug("terabox: membership query failed", "err", err)
	}
	return res, nil
}

// membership reads the VIP flag and expiry (best effort; the response
// shape is not documented).
func (p *Plugin) membership(ctx context.Context, s *hosters.Session) (bool, time.Time, error) {
	v := commonParams(tokens{})
	v.Set("method", "query")
	r, err := p.get(ctx, s, p.accountBase+pathMembership+"?"+v.Encode(), p.accountBase+"/", true, 1<<20, http.Header{"Accept": {"application/json"}})
	if err != nil {
		return false, time.Time{}, err
	}
	var m struct {
		Errno flexInt `json:"errno"`
		Data  struct {
			MemberInfo memberInfo `json:"member_info"`
		} `json:"data"`
		MemberInfo *memberInfo `json:"member_info"`
	}
	if err := json.Unmarshal(r.body, &m); err != nil {
		return false, time.Time{}, fmt.Errorf("decoding membership: %w", err)
	}
	if m.Errno != 0 {
		return false, time.Time{}, &APIError{Errno: int(m.Errno)}
	}
	mi := m.Data.MemberInfo
	if m.MemberInfo != nil {
		mi = *m.MemberInfo
	}
	var until time.Time
	if mi.VIPEndTime > 0 {
		until = time.Unix(int64(mi.VIPEndTime), 0).UTC()
	}
	return mi.IsVIP != 0, until, nil
}

type memberInfo struct {
	IsVIP      flexInt `json:"is_vip"`
	VIPEndTime flexInt `json:"vip_end_time"`
}

// SelfTest implements hosters.SelfTester: resolves the configured public
// share and prepares its first file (nothing is downloaded beyond the
// 1-byte range used to follow the dlink).
func (p *Plugin) SelfTest(ctx context.Context, s *hosters.Session) error {
	if p.selfTestLink == "" {
		return errors.New("terabox: set [hosters.terabox.options] selftest_link to a public share")
	}
	if u, err := url.Parse(p.selfTestLink); err != nil || !p.Match(u) {
		return errors.New("terabox: selftest_link is not a TeraBox share link")
	}
	res, err := p.Resolve(ctx, s, p.selfTestLink)
	if err != nil {
		return fmt.Errorf("terabox selftest: resolve: %w", err)
	}
	if len(res.Files) == 0 {
		return errors.New("terabox selftest: the share has no files")
	}
	if _, err := p.Prepare(ctx, s, res.Files[0]); err != nil {
		return fmt.Errorf("terabox selftest: prepare: %w", err)
	}
	return nil
}
