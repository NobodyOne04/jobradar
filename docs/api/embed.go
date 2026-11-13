// Package apidocs embeds the API contract files that the code enforces, so the documented
// schema and the validated one cannot drift apart.
package apidocs

import _ "embed"

// ProfileSchemaV1 is the candidate profile JSON Schema, schema_version 1 (ADR 0006).
//
//go:embed profile.schema.json
var ProfileSchemaV1 []byte
