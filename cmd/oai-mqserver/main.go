package main

import (
	// Tune GOMAXPROCS to the container's CPU quota. Without this a pod on a
	// busy node sees every core and the Go scheduler oversubscribes it.
	_ "go.uber.org/automaxprocs/maxprocs"

	"github.com/onexstack/onexai/cmd/oai-mqserver/app"
)

// The default entry point of a Go program. Serves as the starting point
// for reading the project code.
func main() {
	// Build and launch the oai-mqserver application.
	app.NewApp().Run()
}
