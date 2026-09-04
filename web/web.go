// Package web embeds the compiled React UI served by `vozgo serve`.
//
// The bundle in dist/ is committed so that `go build` works without Node
// installed; `make web` regenerates it from web/app, and the Docker image
// rebuilds it from source so the image can never ship a stale UI.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// FS returns the built UI rooted at dist/.
func FS() (fs.FS, error) {
	return fs.Sub(dist, "dist")
}

// Index returns the entry page.
func Index() ([]byte, error) {
	return dist.ReadFile("dist/index.html")
}
