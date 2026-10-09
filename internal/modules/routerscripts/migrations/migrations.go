// Package migrations holds the router scripts module's tables (prefix
// rscripts_). The whole Phase 4 schema is written ahead; later sub-phases
// build on it.
package migrations

import (
	"embed"
	"io/fs"
)

//go:embed *.sql
var files embed.FS

// FS is the module's goose migrations.
var FS fs.FS = files
