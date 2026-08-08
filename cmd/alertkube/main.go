// Command alertkube is the entrypoint for the AlertKube controller binary.
//
// It is intentionally thin: all wiring, flag parsing, and the controller loop
// live in the importable github.com/aryasoni98/alertkube/internal/app package so they can be unit
// tested without building the binary. The build stamps the version via
// -ldflags "-X github.com/aryasoni98/alertkube/internal/app.version=<v>".
package main

import "github.com/aryasoni98/alertkube/internal/app"

func main() {
	app.Run()
}
