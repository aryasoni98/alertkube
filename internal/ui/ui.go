// Package ui serves the read-only AlertKube web console. The single-page app
// (plain HTML/CSS/JS, no build step) is embedded into the binary with go:embed
// so the project stays a single static artifact - no second service, no npm
// supply chain, no runtime asset fetches. The console is static and carries no
// secrets; the data it renders comes from the token-gated /api/* endpoints, so
// the assets themselves are served unauthenticated (the browser cannot attach a
// bearer token to the initial document load). A strict Content-Security-Policy
// keeps the page self-contained: no inline script, no third-party origins.
package ui

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed all:dist
var dist embed.FS

// securityHeaders are sent with every console asset. The CSP is deliberately
// tight: script and style load only from same-origin files (no 'unsafe-inline',
// so the SPA keeps its JS/CSS in separate files), XHR talks only to the same
// origin, and the page may not be framed. These cost nothing for a static read
// only console and shrink the blast radius if an asset is ever tampered with.
const csp = "default-src 'none'; " +
	"script-src 'self'; " +
	"style-src 'self'; " +
	"img-src 'self' data:; " +
	"connect-src 'self'; " +
	"base-uri 'none'; " +
	"form-action 'none'; " +
	"frame-ancestors 'none'"

// Handler returns an http.Handler serving the embedded console at the mount
// root. Unknown paths fall back to index.html so client-side navigation works
// (single-page app). It is safe to mount on "/" of the metrics ServeMux: the
// exact API/metrics/health routes are more specific and win over this catch-all.
func Handler() http.Handler {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		// Sub only fails if the embedded path is wrong, which is a build-time
		// guarantee here; panic surfaces a broken embed at startup, not at the
		// first request.
		panic("ui: embedded dist missing: " + err.Error())
	}
	files := http.FS(sub)
	fileServer := http.FileServer(files)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", csp)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")

		// SPA fallback: if the requested file does not exist, serve index.html
		// so a deep link / reload still loads the app shell.
		upath := r.URL.Path
		if upath == "/" {
			serveFile(w, r, files, "index.html")
			return
		}
		if _, err := fs.Stat(sub, trimLeadingSlash(upath)); err != nil {
			serveFile(w, r, files, "index.html")
			return
		}
		fileServer.ServeHTTP(w, r)
	})
}

func serveFile(w http.ResponseWriter, r *http.Request, files http.FileSystem, name string) {
	f, err := files.Open(name)
	if err != nil {
		http.Error(w, "ui asset missing", http.StatusInternalServerError)
		return
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil {
		http.Error(w, "ui asset missing", http.StatusInternalServerError)
		return
	}
	http.ServeContent(w, r, name, st.ModTime(), f)
}

func trimLeadingSlash(p string) string {
	if len(p) > 0 && p[0] == '/' {
		return p[1:]
	}
	return p
}
