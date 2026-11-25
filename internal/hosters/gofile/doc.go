// Package gofile implements the Gofile plugin.
//
// Links: https://gofile.io/d/<contentId> (always a folder, possibly nested).
//
// Flow:
//
//  1. Account token: a premium account (kind apikey, APIKey = the account
//     token shown in the Gofile profile) or, anonymously, a guest token from
//     POST https://api.gofile.io/accounts (cached in the Session).
//  2. Website token "wt": scraped from the site JS
//     (https://gofile.io/dist/js/global.js; regex in endpoints.go), cached
//     in the plugin for wt_ttl; override it with options.website_token when
//     Gofile changes its JS.
//  3. GET https://api.gofile.io/contents/<id>?wt=<wt> with
//     "Authorization: Bearer <token>" (wt also sent as X-Website-Token).
//     Sub-folders are listed recursively (rate limited by
//     requests_per_second).
//  4. Downloads use the file "link" and need the cookie
//     accountToken=<token>, which Prepare stores in the Session jar.
//
// Password-protected folders are reported as NeedsAuth (a password can be
// given with options.password_<contentId> = "<plain password>"; it is sent
// as its SHA-256 like the site does).
//
// Options ([hosters.gofile.options]):
//
//	api_base        default https://api.gofile.io
//	site_base       default https://gofile.io
//	js_url          default <site_base>/dist/js/global.js
//	website_token   fixed wt (skips scraping)
//	wt_ttl          how long a scraped wt is reused (Go duration, default 6h)
package gofile
