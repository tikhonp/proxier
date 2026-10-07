// Package migrations holds the platform's own tables. Tables without a module
// prefix belong to the platform (docs/architecture.md#data).
package migrations

import (
	"embed"
	"io/fs"
)

//go:embed *.sql
var files embed.FS

// FS is the platform's goose migrations.
var FS fs.FS = files
