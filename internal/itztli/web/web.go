// Package web renders the itztli pages: templ components, their view
// models, and the embedded static assets (stylesheet, htmx).
//
// The views only format; every decision about what to show is taken by
// the view-model constructors, which keep the mockup's reading rules:
// masks are highlighted as placeholders, correlations are labeled as
// correlations, and durations are written for a person.
package web

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed static
var staticFS embed.FS

// StaticHandler serves the embedded assets under /static/.
func StaticHandler() http.Handler {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		// The embedded tree is fixed at compile time; this cannot
		// happen on a build that passed its tests.
		panic(err)
	}

	return http.StripPrefix("/static/", http.FileServer(http.FS(sub)))
}
