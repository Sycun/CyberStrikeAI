// Package web carries the browser bundle so a bare binary can serve the UI
// without knowing which directory it was started from.
package web

import "embed"

//go:embed all:static all:templates
var FS embed.FS
