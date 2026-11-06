// Package web serves the dashboard. Frontend source lives in this directory
// and builds into dist/, which is embedded into the pantalan binary.
package web

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed dist
var dist embed.FS

func Handler() (http.Handler, error) {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		return nil, err
	}
	return http.FileServerFS(sub), nil
}
