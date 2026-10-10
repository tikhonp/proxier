// Package migrations holds the subscriptions module's tables (prefix subs_).
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
