// Package protosrc embeds the public protobuf contracts (securebrand.fb.v1,
// securebrand.content.v1, securebrand.analysis.v1) so `fbctl schemas apply`
// can register them with a Schema Registry.
package protosrc

import "embed"

// FS holds the public .proto files at their import paths.
//
//go:embed securebrand/fb/v1/*.proto securebrand/content/v1/*.proto securebrand/analysis/v1/*.proto
var FS embed.FS
