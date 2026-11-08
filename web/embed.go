// Package web holds the Dashboard's assets, embedded into the pantalan
// binary: html/template files and the static files the pages load. It has
// no handlers and no build step (ADR 0002); internal/dashboard serves them.
//
//	templates/*.html        layout, login page and Dashboard shell
//	templates/pages/*.html  one file per Dashboard page
//	templates/icons/*.svg   inline icons, by file name
//	static/                 CSS, app.js and vendored htmx
package web

import (
	"embed"
	"io/fs"
)

//go:embed templates static
var files embed.FS

// Templates is the template tree (templates/ above).
func Templates() fs.FS { return sub("templates") }

// Static is the tree served under /static/<version>/.
func Static() fs.FS { return sub("static") }

func sub(dir string) fs.FS {
	f, err := fs.Sub(files, dir)
	if err != nil {
		panic(err) // dir is embedded above, so this can't happen
	}
	return f
}
