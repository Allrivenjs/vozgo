// Package web embeds the single-page UI served by `vozgo serve`.
package web

import (
	"embed"
)

//go:embed index.html
var files embed.FS

// Index returns the UI page.
func Index() ([]byte, error) {
	return files.ReadFile("index.html")
}
