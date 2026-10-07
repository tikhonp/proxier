// Package migrations holds the servers module's tables (prefix servers_). The
// whole Phase 1 schema is written ahead, as Phase 0's was; later sub-phases
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
