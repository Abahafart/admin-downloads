// Package yandex implements the Yandex Disk plugin for public links.
//
// Links: https://disk.yandex.<tld>/d/<key>[/sub/path] (folders or files),
// /i/<key> (files), /public?hash=<key>, https://yadi.sk/d/… and /i/…, and
// disk.360.yandex.<tld>. Extra hostnames come from [hosters.yandex].domains.
//
// API (anonymous):
//
//	GET {api}/v1/disk/public/resources?public_key=<link>&path=<p>&limit=&offset=
//	GET {api}/v1/disk/public/resources/download?public_key=<link>&path=<p>
//
// Folders are listed recursively and paged (limit/offset against
// _embedded.total); RelPath is the item path relative to the shared root
// (or to the sub-path of the link). Checksums: sha256 (md5 as fallback).
// Prepare asks /download for a fresh href each time.
//
// Limits: a public file downloaded too often answers
// DiskResourceDownloadLimitExceededError, a per-file limit
// (Quota{ScopeFile}); HTTP 429 is reported as Quota{ScopeIP}.
//
// Options ([hosters.yandex.options]):
//
//	api_base   default https://cloud-api.yandex.net
//	page_size  items per listing request (default 200, max 1000)
package yandex
