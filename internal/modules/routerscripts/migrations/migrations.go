// Package migrations holds the router scripts module's tables (prefix
// rscripts_). Schema changes go into new migrations.
package migrations

import (
	"embed"
	"io/fs"
)

//go:embed *.sql
var files embed.FS

// FS is the module's goose migrations.
var FS fs.FS = files
