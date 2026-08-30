// Package atlas exposes the Atlas versioned migration directory as the single
// embedded schema migration source for the in-process migration runner.
package atlas

import (
	"embed"
	"io/fs"
)

// migrationsFS embeds the whole migration directory including atlas.sum.
//
//go:embed migrations
var migrationsFS embed.FS

// MigrationDir returns the embedded migration directory rooted at the SQL
// files (without the wrapping "migrations" path element).
func MigrationDir() fs.FS {
	sub, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		panic(err)
	}
	return sub
}
