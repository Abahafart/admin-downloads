package mediafire

import (
	"context"
	"errors"
	"fmt"
	"net/url"

	"admindescargas/internal/hosters"
)

// SelfTest implements hosters.SelfTester. It always checks that the API
// still answers file/get_info with its JSON error shape for a bogus key;
// when Options selftest_file / selftest_folder are set it also resolves
// them and scrapes a direct link for the first file.
func (h *Hoster) SelfTest(ctx context.Context, s *hosters.Session) error {
	_, err := h.apiCall(ctx, s, pathFileInfo, url.Values{"quick_key": {"zzzzzzzzzzzzzzz"}})
	switch {
	case err == nil:
		return errors.New("mediafire selftest: bogus quick key was accepted")
	case !hosters.IsNotFound(err) && !hosters.IsFatal(err):
		return fmt.Errorf("mediafire selftest: api: %w", err)
	}
	for _, opt := range []string{"selftest_file", "selftest_folder"} {
		link := h.cfg.Options[opt]
		if link == "" {
			continue
		}
		res, err := h.Resolve(ctx, s, link)
		if err != nil {
			return fmt.Errorf("mediafire selftest: resolve %s: %w", opt, err)
		}
		if len(res.Files) == 0 {
			return fmt.Errorf("mediafire selftest: %s resolved to no files", opt)
		}
		if _, err := h.Prepare(ctx, s, res.Files[0]); err != nil {
			return fmt.Errorf("mediafire selftest: prepare %s: %w", opt, err)
		}
	}
	return nil
}
