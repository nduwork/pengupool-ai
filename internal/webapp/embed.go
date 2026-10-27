// Package webapp embeds the built renderer bundle.
package webapp

import "embed"

// Dist is the production renderer (web/ build output).
//
//go:embed dist
var Dist embed.FS
