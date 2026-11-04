// Package migrations contains the versioned SQL schema migrations of the
// knowledge graph database. Files follow the naming scheme
// NNNN_description.up.sql / NNNN_description.down.sql and are embedded into
// the binary so that the migrator does not depend on the working directory.
package migrations

import "embed"

// FS holds all *.sql migration files.
//
//go:embed *.sql
var FS embed.FS
