// Package apispec embeds the OpenAPI document that describes the HTTP API. The file is the contract: the route-parity
// test fails when it and the router disagree, and the end-to-end tests validate real responses against it.
package apispec

import _ "embed"

// Spec is api/openapi.yaml, served at GET /openapi.yaml.
//
//go:embed openapi.yaml
var Spec []byte
