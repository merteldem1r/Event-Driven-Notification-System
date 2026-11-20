// Package docs holds the API specification, built into the API binary so the API can serve it
// together with Swagger UI.
package docs

import _ "embed" // for go:embed

//go:embed openapi.yaml
var openAPI []byte

// OpenAPI returns the API specification, docs/openapi.yaml.
func OpenAPI() []byte {
	return openAPI
}
