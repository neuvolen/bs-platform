// Package migrations embeds the SQL migration files so the server can apply
// them itself on startup. No manual SQL runs in TablePlus are needed.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
