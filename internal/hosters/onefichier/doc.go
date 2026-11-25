// Package onefichier implements the 1fichier plugin (plugin name
// "onefichier").
//
// Links (1fichier.com and its alias domains: alterupload.com, cjoint.net,
// desfichiers.com, dfichiers.com, megadl.fr, mesfichiers.org,
// piecejointe.net, pjointe.com, tenvoi.com, dl4free.com, plus
// [hosters.onefichier].domains):
//
//	https://1fichier.com/?<id>[&af=…]   file
//	https://<id>.1fichier.com/          file (old form)
//	https://1fichier.com/dir/<id>       public folder (flat)
//
// Anonymous ("free") flow:
//   - Resolve: POST /check_links.pl (links[]=…) returns "url;name;size".
//     Folders: GET /dir/<id>?json=1 returns the file list.
//   - Prepare: GET the file page, parse it (parse.go): "you must wait N
//     minutes" / the JS countdown become Quota{ScopeIP, RetryAfter} (the
//     free wait is per IP, so rotating the egress lifts it); short
//     countdowns (<= max_inline_wait) are waited in place. The download
//     form (hidden adz token) is posted and the "Click here to download"
//     link is returned. Free links: one connection, no resume.
//
// Premium flow (account kind apikey with the 1fichier API key):
//   - POST https://api.1fichier.com/v1/file/info.cgi for name and size,
//   - POST https://api.1fichier.com/v1/download/get_token.cgi for a direct,
//     resumable link (Authorization: Bearer <key>),
//   - POST https://api.1fichier.com/v1/user/info.cgi for the account check.
//
// The API is strictly rate limited ("Flood detected"): calls are spaced by
// api_interval.
//
// Password-protected files are reported as NeedsAuth.
//
// Options ([hosters.onefichier.options]):
//
//	web_base          default https://1fichier.com
//	api_base          default https://api.1fichier.com/v1
//	max_inline_wait   longest countdown waited inside Prepare (default 90s)
//	api_interval      minimum time between API calls (default 2s)
package onefichier
