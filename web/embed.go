// Package web exposes the built single-page app embedded in the binary.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// FS returns the built frontend rooted at the dist directory.
func FS() (fs.FS, error) { return fs.Sub(dist, "dist") }
