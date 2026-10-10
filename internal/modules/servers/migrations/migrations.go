// Package migrations holds the servers module's tables (prefix servers_).
// Schema changes go into new migrations.
package migrations

import (
	"embed"
	"io/fs"
)

//go:embed *.sql
var files embed.FS

// FS is the module's goose migrations.
var FS fs.FS = files
