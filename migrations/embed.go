// Package migrations exposes the project SQL migrations as the single embedded migration source.
package migrations

import "embed"

// FS contains every versioned project SQL migration.
//
//go:embed *.sql
var FS embed.FS
