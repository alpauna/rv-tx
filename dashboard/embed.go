// Package dashboard embeds the built SPA (dashboard/dist, produced by
// `npm run build`) so the control plane binary can serve it directly
// -- one systemd service, one binary, matching how every other piece
// of this project deploys. `go build` must run after `npm run build`
// has produced real content in dist/; go:embed requires the directory
// to exist with at least one file even during plain development
// builds of the Go side.
package dashboard

import "embed"

//go:embed all:dist
var DistFS embed.FS
