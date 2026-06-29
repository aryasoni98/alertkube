// Command alertkube is the entrypoint for the AlertKube controller binary.
//
// It is intentionally thin: all wiring, flag parsing, and the controller loop
// live in the importable alertkube/internal/app package so they can be unit
// tested without building the binary. The build stamps the version via
// -ldflags "-X alertkube/internal/app.version=<v>".
package main

import "alertkube/internal/app"

func main() {
	app.Run()
}
