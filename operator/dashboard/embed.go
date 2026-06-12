// Package dashboard embeds the static single-page dashboard served by the
// operator's REST API at /dashboard/.
package dashboard

import "embed"

// FS holds the dashboard static assets.
//
//go:embed index.html
var FS embed.FS
