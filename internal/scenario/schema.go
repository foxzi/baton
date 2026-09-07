package scenario

import (
	_ "embed"
)

// schemaJSON is the JSON Schema of the scenario format, printed by
// `baton schema` and consumed by editors. It describes what this version of
// baton parses: step kinds that are not executed yet are accepted as objects
// without inner constraints, exactly as the parser accepts them.
//
//go:embed scenario.schema.json
var schemaJSON []byte

// Schema returns the JSON Schema of the scenario format.
func Schema() []byte { return schemaJSON }
