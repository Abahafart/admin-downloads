// Package terabox implements the TeraBox plugin and its mirror domains
// (PLAN.md sections 4, 5, 9.3). It is the most fragile plugin: every
// selector, parameter, endpoint and errno meaning lives in endpoints.go, and
// `adesc selftest terabox` (SelfTest, option selftest_link) checks a known
// public share.
//
// Flow: the share page (/sharing/link?surl=..., following mirror redirects)
// is loaded with the account cookies (ndus) to scrape jsToken and dp-logid;
// /api/shorturlinfo validates the share; /share/list (root=1, then dir=...
// for every subfolder, paginated) yields fs_id, size, md5, path and dlink;
// Prepare follows the dlink 302 chain to the CDN URL with the same cookies.
// Signed URLs are never persisted: FileInfo.Meta keeps surl, fs_id, the
// parent dir and the link host, and Prepare lists the directory again when
// its cached dlink is older than a few minutes.
//
// Accounts are kind cookie: Secret.Cookies holds a Netscape cookies.txt, a
// JSON export or a "k=v; ..." string; only cookies of TeraBox domains are
// used and ndus is required. CheckAccount validates the cookie and reads the
// VIP status (best effort).
//
// Options ([hosters.terabox.options]): base_url, account_url,
// replace_domains, page_size, verify_md5, selftest_link (see Opt*).
package terabox
