// Package web embeds the built frontend (run `npm run build` in web/ first).
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// Dist is the built single-page app.
var Dist, _ = fs.Sub(dist, "dist")
