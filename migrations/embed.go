package migrations

import "embed"

// Files holds the SQL migrations embedded into the binary, so the app migrates
// on startup without the migrate CLI or the source tree present.
//
//go:embed *.sql
var Files embed.FS
