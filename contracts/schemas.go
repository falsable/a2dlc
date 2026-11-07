// Package contracts embeds the JSON Schemas of the contract, so the core enforces the `required` fields they declare
// from the very files that are the contract, never from a copy.
package contracts

import "embed"

// FS holds every contracts/*.schema.json.
//
//go:embed *.schema.json
var FS embed.FS
