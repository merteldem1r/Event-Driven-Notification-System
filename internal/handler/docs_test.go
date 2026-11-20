package handler

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// /openapi.yaml serves exactly docs/openapi.yaml, so the built-in copy can't drift from the file.
func TestOpenAPISpec_ServesTheFile(t *testing.T) {
	want, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	NewRouter(slog.New(slog.DiscardHandler), fakePinger{}, nil, nil).
		ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/openapi.yaml", nil))
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/yaml" {
		t.Fatalf("GET /openapi.yaml = %d %q, want 200 application/yaml", rec.Code, rec.Header().Get("Content-Type"))
	}
	if !bytes.Equal(rec.Body.Bytes(), want) {
		t.Error("served spec differs from docs/openapi.yaml")
	}
}

// /docs is the Swagger UI page: the pinned version, integrity hashes, and our spec.
func TestSwaggerUI(t *testing.T) {
	rec := httptest.NewRecorder()
	NewRouter(slog.New(slog.DiscardHandler), fakePinger{}, nil, nil).
		ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/docs", nil))
	if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("GET /docs = %d %q, want 200 text/html", rec.Code, rec.Header().Get("Content-Type"))
	}
	body := rec.Body.String()
	for _, want := range []string{
		"swagger-ui-dist@" + swaggerUIVersion + "/swagger-ui-bundle.js",
		`integrity="sha384-`,
		`url: "/openapi.yaml"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page is missing %s", want)
		}
	}
}
