package mediafire

import (
	"regexp"
	"time"
)

// Everything that depends on MediaFire's HTML or API shape lives here so a
// breaking change on their side is fixed in one place.

const (
	// defaultWebBase is the site root (file pages, folder pages).
	defaultWebBase = "https://www.mediafire.com"
	// defaultAPIBase is the root of the public 1.5 API.
	defaultAPIBase = "https://www.mediafire.com/api/1.5"

	pathFileInfo     = "/file/get_info.php"
	pathFolderInfo   = "/folder/get_info.php"
	pathFolderList   = "/folder/get_content.php"
	pathUserInfo     = "/user/get_info.php"
	pathSessionToken = "/application/get_session_token.php" // relative to web base

	// userAgent is sent on page requests: MediaFire serves a different,
	// script-only page to unknown clients.
	userAgent = "Mozilla/5.0 (X11; Linux x86_64; rv:128.0) Gecko/20100101 Firefox/128.0"

	// maxPageBytes caps how much of an HTML page / API reply is read.
	maxPageBytes = 4 << 20

	// captchaRetryAfter is the short wait advertised for 429 / captcha
	// pages: rotating the egress is the real fix.
	captchaRetryAfter = time.Minute

	// defaultRPS limits API/page requests while listing folders.
	defaultRPS = 4.0

	// maxFolderDepth stops pathological (or cyclic) folder trees.
	maxFolderDepth = 64
)

// API error codes (api/1.5) that mean the item does not exist (anymore).
var notFoundCodes = map[int]bool{
	110: true, // Unknown or invalid QuickKey
	112: true, // Unknown or invalid FolderKey
	114: true, // file does not belong to the session user / private
	138: true, // file removed
}

// API error codes that mean "slow down".
var rateLimitCodes = map[int]bool{
	159: true,
}

var (
	// reKey validates a quick key / folder key.
	reKey = regexp.MustCompile(`^[A-Za-z0-9]{5,20}$`)
	// reDirectLink finds direct download links anywhere in a page.
	reDirectLink = regexp.MustCompile(`https?://download\d+\.mediafire\.com/[^"'\s<>\\]+`)
	// reDirectHost recognizes MediaFire download servers.
	reDirectHost = regexp.MustCompile(`(?i)^download\d+\.mediafire\.com$`)
	// reErrno extracts the reason of error.php?errno=NNN redirects.
	reErrno = regexp.MustCompile(`errno=(\d+)`)
)

// CSS selectors on the file page.
const (
	selDownloadButton = "a#downloadButton"
	attrScrambledURL  = "data-scrambled-url"
	selPasswordInput  = `input[name="downloadp"], form[name="form_password"]`
	selFileName       = "div.dl-btn-label"
)

// Lowercase markers searched in page text.
var (
	markersCaptcha = []string{
		"g-recaptcha", "h-captcha", "cf-turnstile", "captcha-form",
		"please complete the security check", "verify you are human",
	}
	markersRemoved = []string{
		"the key you provided for file download was invalid",
		"file has been removed", "has been removed by the owner",
		"file has been deleted", "invalid or deleted file",
		"this file is no longer available",
	}
	markersDangerous = []string{
		"dangerous file", "flagged as dangerous", "file has been blocked",
		"blocked for violating", "identified as malware",
	}
)

// errnoMeaning describes the error.php?errno= values seen in the wild.
var errnoMeaning = map[string]string{
	"320": "invalid or deleted file",
	"378": "file removed by its owner",
	"380": "file removed for a copyright claim",
	"386": "file blocked for violating the terms of service",
	"388": "file blocked as dangerous",
	"394": "file is private",
}
