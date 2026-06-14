// Package migrations embeds the SQL migration files so the engine can apply them without an
// external migration CLI.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
