// Package gdrive implements the Google Drive plugin.
//
// # Links
//
//	drive.google.com/file/d/<id>[/view]     drive.google.com/open?id=<id>
//	drive.google.com/uc?id=<id>             drive.usercontent.google.com/download?id=<id>
//	drive.google.com/drive/folders/<id>     drive.google.com/drive/u/N/folders/<id>
//	docs.google.com/{document,spreadsheets,presentation,drawings}/d/<id>
//
// A "resourcekey" query parameter is kept and sent as
// X-Goog-Drive-Resource-Keys / resourcekey.
//
// # Modes
//
// The mode depends on the session:
//
//   - OAuth account (kind "oauth", Secret.OAuthToken): Drive API v3 with the
//     user's token. Expired tokens are refreshed with client_id /
//     client_secret (HosterConfig.Options, or Secret.Extra["client_id"] /
//     ["client_secret"]) and the refreshed token is persisted with
//     Session.SetSecret. Without client credentials an expired token is
//     reported as NeedsAuth.
//   - API key (Options["api_key"], or an account of kind "apikey" with
//     Secret.APIKey): Drive API v3 for public files, the key is sent in the
//     X-Goog-Api-Key header (never in URLs).
//   - Anonymous: drive.usercontent.google.com/download?id=..&export=download
//     &confirm=t, following the virus-scan form (action + hidden inputs);
//     folders through drive.google.com/embeddedfolderview (best effort:
//     sizes are unknown, very large folders may be incomplete; such files
//     carry Meta["listing"] = "embedded" and a warning is logged).
//
// Every HTTP client derives from Session.Client's transport so the egress
// (proxy, Tor, WireGuard...) applies to API calls, token refreshes and
// downloads alike.
//
// # Per-file quota and the copy workaround
//
// "Download quota exceeded" is per file: rotating the IP does not help, so
// it is reported as Quota{ScopeFile}. When the session has an OAuth account
// and Options["copy_on_quota"] is not "false", Prepare copies the file into
// the user's Drive (files.copy, tagged with appProperties adescCopyOf=<id>),
// downloads the copy instead, and the copy is deleted by Cleanup.
//
// Engine contract: after a gdrive download ends (completed, failed for good
// or removed by the user) call
//
//	if c, ok := h.(interface {
//		Cleanup(context.Context, *hosters.Session, hosters.FileInfo) error
//	}); ok { _ = c.Cleanup(ctx, s, f) }
//
// with any session of the same account. Cleanup is a no-op when no copy
// exists, for anonymous sessions or when Options["delete_copy"] = "false".
// Copies are found by their appProperties tag, so a restart between Prepare
// and Cleanup does not leak them. On Quota{ScopeFile} the engine may call
// CanBypassFileQuota(s): when true, simply calling Prepare again (no wait)
// switches to the copy.
//
// # Google Docs
//
// Docs, Sheets, Slides and Drawings are exported (not resumable, size
// unknown). Defaults: docx, xlsx, pptx, pdf. Options:
//
//	export_format       = "pdf"   # override for every type (if valid for it)
//	export_document     = "docx"  # docx pdf odt rtf txt epub md zip
//	export_spreadsheet  = "xlsx"  # xlsx pdf ods csv tsv zip
//	export_presentation = "pptx"  # pptx pdf odp txt
//	export_drawing      = "pdf"   # pdf png jpg svg
//
// # All HosterConfig.Options
//
//	client_id, client_secret   OAuth client used to refresh tokens
//	api_key                    Drive API key for anonymous sessions
//	copy_on_quota = "true"     copy workaround on per-file quota (OAuth only)
//	delete_copy   = "true"     delete the temporary copy in Cleanup
//	export_*                   see above
//	selftest_file, selftest_folder   public links used by SelfTest
package gdrive
