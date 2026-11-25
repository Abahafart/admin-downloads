// Package pixeldrain implements the Pixeldrain plugin.
//
// Supported links (pixeldrain.com, pixeldrain.net, pixeldra.in plus
// [hosters.pixeldrain].domains):
//
//	https://pixeldrain.com/u/<id>            one file
//	https://pixeldrain.com/u/<id1>,<id2>     several loose files
//	https://pixeldrain.com/l/<id>            list (flat folder, RelPath = name)
//	https://pixeldrain.com/l/<id>#item=<n>   one file of a list (0-based)
//	https://pixeldrain.com/api/file/<id>     API links
//
// API: GET /api/file/<id>/info (name, size, sha256, availability),
// GET /api/file/<id>?download (Range supported), GET /api/list/<id>,
// GET /api/user (account check).
//
// Accounts: kind apikey; the key is sent as HTTP basic auth with an empty
// user name (":<key>"), which lifts the anonymous limits.
//
// Limits: anonymous downloads have a per-IP transfer/speed limit, reported
// as Quota{ScopeIP}; files flagged "rate limited" require a captcha for
// anonymous users, reported as NeedsAuth (an API key bypasses it).
//
// Options ([hosters.pixeldrain.options]):
//
//	api_base   API root, default https://pixeldrain.com/api (tests, mirrors)
package pixeldrain
