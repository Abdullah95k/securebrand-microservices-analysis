// Package analysismigrations embeds the goose SQL migrations of the
// analysis service (schema "analysis", version table
// analysis_goose_db_version), kept apart from fbs's own migrations so the
// service can move to a database of its own.
package analysismigrations

import "embed"

//go:embed *.sql
var FS embed.FS
