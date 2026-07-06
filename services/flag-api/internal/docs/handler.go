// Package docs provides the Redoc interactive API explorer handler.
package docs

import (
	"net/http"

	goredoc "github.com/mvrilo/go-redoc"
)

// NewHandler returns an http.Handler that serves the Redoc API explorer.
// specURL is the URL of the OpenAPI JSON spec relative to the server root —
// Redoc fetches this URL from the browser, so it must be publicly accessible.
// The Redoc JS bundle is embedded in the binary (no CDN dependency at runtime).
func NewHandler(specURL string) http.Handler {
	doc := goredoc.Redoc{
		Title:       "Tombstone API Reference",
		Description: "Production intelligence layer for feature flags — REST API reference.",
		SpecPath:    specURL,
		DocsPath:    "/api/v1/docs",
	}

	data, err := doc.Body()
	if err != nil {
		panic(err)
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)
	})
}
