// Package web embeds the explorer frontend (plain ES modules, no build step).
package web

import (
	"embed"
	"io/fs"
)

//go:embed static
var static embed.FS

// FS returns the frontend files rooted at static/.
func FS() fs.FS {
	f, err := fs.Sub(static, "static")
	if err != nil {
		panic(err)
	}
	return f
}
