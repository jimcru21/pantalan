// Package web embeds the Dashboard's single-page app into the pantalan
// binary (ADR 0003). It has no handlers; internal/dashboard serves it.
//
//	app/   the SPA's source: React, Vite and TypeScript
//	dist/  what `make web` (vite build) writes: index.html and the
//	       content-hashed files under assets/
//
// dist/ is gitignored except its placeholder, so a checkout without Node
// still compiles: the binary then serves a "frontend not built" page.
package web

import (
	"embed"
	"io/fs"
)

// all: so the placeholder, a dotfile, matches when nothing is built.
//
//go:embed all:dist
var files embed.FS

// Dist is the built SPA, rooted at web/dist.
func Dist() fs.FS {
	f, err := fs.Sub(files, "dist")
	if err != nil {
		panic(err) // dist is embedded above, so this can't happen
	}
	return f
}
