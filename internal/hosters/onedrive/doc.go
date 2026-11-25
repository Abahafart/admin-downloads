// Package onedrive implements the OneDrive (personal) plugin for share links.
//
// Links: https://1drv.ms/<type>/... (u, f, t, i, ... and the newer
// /<type>/c/<cid>/<token> form) and https://onedrive.live.com/... with
// resid/id/redeem parameters (redir, embed, download, ...).
//
// Every link is turned into a share id ("u!" + unpadded base64url of the
// link) and resolved anonymously with the OneDrive API:
//
//	GET {api}/shares/u!<b64>/root                      the shared item
//	GET {api}/shares/u!<b64>/root/children             first level
//	GET {api}/shares/u!<b64>/items/<id>/children       sub-folders
//	GET {api}/shares/u!<b64>/items/<id>                fresh download URL
//	GET {api}/shares/u!<b64>/items/<id>/content        302 to the file
//
// Children are paged with @odata.nextLink (only followed when it stays on
// the API host). Prepare fetches the item again to obtain a fresh
// pre-authenticated @content.downloadUrl (valid about an hour; Expires is
// set so the downloader calls Prepare again) and falls back to /content.
// Hashes: sha256Hash, then sha1Hash, then crc32Hash.
//
// Business/SharePoint links (*.sharepoint.com) are not handled: they need
// Microsoft Graph with authentication (the generic plugin may still
// download "anyone" links with ?download=1).
//
// Options ([hosters.onedrive.options]):
//
//	api_base   default https://api.onedrive.com/v1.0
package onedrive
