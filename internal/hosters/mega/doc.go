// Package mega implements the MEGA plugin (PLAN.md sections 4, 5, 9.3).
//
// It speaks MEGA's JSON API (g.api.mega.co.nz/cs) with a small client of its
// own instead of t3rm1n4l/go-mega: go-mega's Login also downloads the whole
// account filesystem and starts an event-polling goroutine, its calls take
// no context, it has no public-folder ("n=") support for downloads and it
// keeps the sid inside an opaque client. The crypto here is a few hundred
// lines and is checked against vectors from an independent implementation
// (testdata/mega/gen_vectors.py).
//
// Supported links (mega.nz, mega.co.nz, mega.io, mega.app and the extra
// [hosters.mega].domains): /file/<h>#<k>, /embed/<h>#<k>, /folder/<h>#<k>,
// /folder/<h>#<k>/file/<id>, /folder/<h>#<k>/folder/<sub>, and the legacy
// #!h!k, #F!h!k and #F!h!k!sub. Password-protected links (#P!) are reported
// as unsupported.
//
// Downloads: Prepare returns the storage URL plus a Decryptor yielding the
// AES-128-CTR keystream at any offset (counter = nonce || offset/16), so
// segments can run in parallel and resume on another egress. The Checksum is
// AlgoMEGAMAC with the node key as value; VerifyMAC checks it.
//
// Quota: HTTP 509 on storage and EOVERQUOTA (-17) become Quota{IP} (Account
// for Pro sessions) with X-MEGA-Time-Left / "tl" / Retry-After or the
// configured retry_after (default 1h).
//
// Accounts (kind password, Account.Username = e-mail): login v1 and v2
// (PBKDF2-HMAC-SHA512), 2FA through Secret.Extra["mfa"] (single use; a
// missing code yields NeedsAuth wrapping ErrMFARequired, and CheckAccount
// reports accounts.StatusNeeds2FA), the sid is kept in Secret.Session and
// sent on every request; on ESID the plugin logs in again once.
// CheckAccount uses "uq" (level, Pro expiry, transfer quota).
//
// Options ([hosters.mega.options]): api_url, ssl, selftest_link,
// hashcash_timeout (see the Opt* constants).
package mega
