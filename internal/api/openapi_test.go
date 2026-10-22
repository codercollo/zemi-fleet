package api

import (
	"context"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apispec "github.com/codercollo/zemi-fleet/api"
)

func loadSpec(t *testing.T) *openapi3.T {
	t.Helper()
	doc, err := openapi3.NewLoader().LoadFromData(apispec.Spec)
	require.NoError(t, err, "api/openapi.yaml does not load")
	return doc
}

func TestOpenAPI_SpecLoadsAndValidates(t *testing.T) {
	doc := loadSpec(t)

	// Validate checks the structure, every $ref and every example against its schema.
	require.NoError(t, doc.Validate(context.Background()))
	assert.Equal(t, "3.0.3", doc.OpenAPI)
}

func TestOpenAPI_SecurityIsGlobalWithHealthzAndDocsOverriding(t *testing.T) {
	doc := loadSpec(t)

	require.Len(t, doc.Security, 1)
	assert.Contains(t, doc.Security[0], "ApiKeyAuth")
	scheme := doc.Components.SecuritySchemes["ApiKeyAuth"].Value
	assert.Equal(t, "apiKey", scheme.Type)
	assert.Equal(t, "header", scheme.In)
	assert.Equal(t, "X-API-Key", scheme.Name)

	open := map[string]bool{"/healthz": true, "/docs": true, "/openapi.yaml": true}
	for path, item := range doc.Paths.Map() {
		for method, op := range item.Operations() {
			if open[path] {
				require.NotNil(t, op.Security, "%s %s must override security", method, path)
				assert.Empty(t, *op.Security, "%s %s needs no key", method, path)
			} else {
				assert.Nil(t, op.Security, "%s %s must inherit the global key", method, path)
			}
		}
	}
}

func TestOpenAPI_EveryOperationDocumentsItsErrors(t *testing.T) {
	doc := loadSpec(t)
	// Every route is authenticated and rate limited, so it can answer 401, 429 and 500; the public ones answer neither 401
	// nor (healthz) 429.
	exempt := map[string][]string{"/healthz": {"401", "429", "500"}, "/docs": {"401", "500"}, "/openapi.yaml": {"401", "500"}}
	for path, item := range doc.Paths.Map() {
		for method, op := range item.Operations() {
			for _, status := range []string{"401", "429", "500"} {
				if slices.Contains(exempt[path], status) {
					continue
				}
				assert.NotNil(t, op.Responses.Value(status), "%s %s should document %s", method, path, status)
			}
			assert.NotEmpty(t, op.OperationID, "%s %s", method, path)
		}
	}
}

// ginParam turns Gin's :id into OpenAPI's {id}.
var ginParam = regexp.MustCompile(`:([A-Za-z_]+)`)

// TestOpenAPI_RouteParity fails on any difference between the routes Gin registers and the paths in the spec, in both directions.
func TestOpenAPI_RouteParity(t *testing.T) {
	doc := loadSpec(t)

	cfg := testConfig()
	cfg.MetricsEnabled, cfg.DocsEnabled = true, true // the optional routes are part of the contract
	srv := NewServer(cfg, discardLogger(), fakeDB{}, Services{}, "test")
	registered := map[string]bool{}
	for _, r := range srv.router().Routes() {
		registered[r.Method+" "+ginParam.ReplaceAllString(r.Path, "{$1}")] = true
	}

	documented := map[string]bool{}
	base := doc.Servers[0].URL
	for path, item := range doc.Paths.Map() {
		prefix := base
		if len(item.Servers) > 0 {
			prefix = item.Servers[0].URL
		}
		full := strings.TrimRight(prefix, "/") + path
		for method := range item.Operations() {
			documented[method+" "+full] = true
		}
	}

	var missingFromSpec, missingFromRouter []string
	for r := range registered {
		if !documented[r] {
			missingFromSpec = append(missingFromSpec, r)
		}
	}
	for d := range documented {
		if !registered[d] {
			missingFromRouter = append(missingFromRouter, d)
		}
	}
	slices.Sort(missingFromSpec)
	slices.Sort(missingFromRouter)
	assert.Empty(t, missingFromSpec, "routes registered in Gin but not in api/openapi.yaml")
	assert.Empty(t, missingFromRouter, "paths in api/openapi.yaml that no Gin route serves")
	assert.NotEmpty(t, registered, "no routes found")
}
