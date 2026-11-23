// Package web holds the web UI: one HTML page with plain CSS and JavaScript, no build step.
package web

import "embed"

// Files are the UI's files, embedded in the binary.
//
//go:embed index.html app.css app.js icon.svg i18n/*.json frame plugins
var Files embed.FS
