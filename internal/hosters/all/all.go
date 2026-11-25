// Package all links every hoster plugin into the binary. Import it for side
// effects (each plugin registers its factory in init):
//
//	import _ "admindescargas/internal/hosters/all"
//
// then build the registry with hosters.NewRegistryFromFactories(cfg.Hosters).
package all

import (
	// Wave 1.
	_ "admindescargas/internal/hosters/gdrive"
	_ "admindescargas/internal/hosters/generic"
	_ "admindescargas/internal/hosters/mediafire"
	_ "admindescargas/internal/hosters/mega"
	_ "admindescargas/internal/hosters/terabox"

	// Wave 2.
	_ "admindescargas/internal/hosters/dropbox"
	_ "admindescargas/internal/hosters/gofile"
	_ "admindescargas/internal/hosters/onedrive"
	_ "admindescargas/internal/hosters/onefichier"
	_ "admindescargas/internal/hosters/pixeldrain"
	_ "admindescargas/internal/hosters/yandex"
)
