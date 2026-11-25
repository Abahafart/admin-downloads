// Package mediafire implements the MediaFire plugin.
//
// Supported links (www.mediafire.com, mediafire.com and any extra host in
// [hosters.mediafire].domains):
//
//	/file/<key>[/<name>[/file]]   /file_premium/<key>/...   /view/<key>
//	/download/<key>   /download.php?<key>   /?<key>          (files)
//	/folder/<key>[/<name>]        /#<folderkey>              (folders)
//
// Files: metadata comes from api/1.5/file/get_info.php (name, size, sha256
// hash used as Checksum); the direct link is scraped from the file page
// (a#downloadButton href, its base64 data-scrambled-url, or a regex for
// downloadNNNN.mediafire.com links). Removed, dangerous, password protected
// and captcha pages are reported with typed errors.
//
// Folders: api/1.5/folder/get_info.php for the root name plus
// folder/get_content.php (content_type=folders|files, chunk=N) recursively;
// files are emitted while the tree is listed (StreamingResolver) and RelPath
// is built from the sanitized folder names.
//
// Accounts are optional (kind "cookie"): Secret.Cookies holds a Netscape
// cookies.txt, a JSON cookie export or a "name=value; ..." header; the
// cookies are loaded into the session jar before any request.
//
// HosterConfig.Options:
//
//	selftest_file   = "https://www.mediafire.com/file/..."  (optional)
//	selftest_folder = "https://www.mediafire.com/folder/..." (optional)
//
// Selectors, API paths and regexes live in endpoints.go.
package mediafire
