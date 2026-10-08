// Package migrations holds the subscriptions module's tables (prefix subs_).
// The whole Phase 2 schema is written ahead; later sub-phases build on it.
package migrations

import (
	"embed"
	"io/fs"
)

//go:embed *.sql
var files embed.FS

// FS is the module's goose migrations.
var FS fs.FS = files
