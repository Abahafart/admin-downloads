package gdrive

import (
	"context"
	"fmt"
	"net/http"

	"admindescargas/internal/hosters"
)

// SelfTest implements hosters.SelfTester. Without options it checks that
// drive.usercontent.google.com still answers a bogus id with a "not found"
// outcome; with Options selftest_file / selftest_folder it resolves them
// and prepares the first file.
func (h *Hoster) SelfTest(ctx context.Context, s *hosters.Session) error {
	resp, err := h.get(ctx, s, h.anonDownloadURL(target{kind: tFile, id: "0000000000000000000000000000000000"}), true)
	if err != nil {
		return fmt.Errorf("gdrive selftest: usercontent: %w", err)
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusPartialContent {
		if !isHTML(resp.Header) {
			return fmt.Errorf("gdrive selftest: bogus id served a file (HTTP %d)", resp.StatusCode)
		}
	}
	if resp.StatusCode >= 500 {
		return fmt.Errorf("gdrive selftest: usercontent answered HTTP %d", resp.StatusCode)
	}
	for _, opt := range []string{"selftest_file", "selftest_folder"} {
		link := h.cfg.Options[opt]
		if link == "" {
			continue
		}
		res, err := h.Resolve(ctx, s, link)
		if err != nil {
			return fmt.Errorf("gdrive selftest: resolve %s: %w", opt, err)
		}
		if len(res.Files) == 0 {
			return fmt.Errorf("gdrive selftest: %s resolved to no files", opt)
		}
		if _, err := h.Prepare(ctx, s, res.Files[0]); err != nil && !hosters.IsQuota(err) {
			return fmt.Errorf("gdrive selftest: prepare %s: %w", opt, err)
		}
	}
	return nil
}
