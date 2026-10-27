// Package webapp embeds the built browser client so the daemon can serve it with no external files
// and no CDN. The bundle is produced by `npm ci && npm run build` in web/, whose Vite config writes
// to internal/webapp/dist.
package webapp

import (
	"embed"
	"io/fs"
)

// Assets is the built Vite bundle: index.html, the hashed JS/CSS chunks and the IBM Plex font
// files. `all:` includes dotfiles, so nothing Vite emits is silently dropped.
//
//go:embed all:dist
var Assets embed.FS

// Dist is Assets rooted at the bundle directory, ready for the gateway's `Static fs.FS`, which
// serves it at "/".
func Dist() (fs.FS, error) {
	return fs.Sub(Assets, "dist")
}
