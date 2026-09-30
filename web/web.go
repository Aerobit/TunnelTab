// Package web embeds the dashboard (HTML, CSS, JS and vendored libraries such
// as xterm.js) into the TunnelTab binary, so the app needs no external files
// and loads nothing from the internet.
package web

import "embed"

// Files holds the dashboard assets. Everything under static/ is served by
// internal/server.
//
//go:embed static
var Files embed.FS
