//go:build !(linux && cgo)

// eirl-engine needs GStreamer through cgo: it only builds for the board (linux with cgo, see
// build/engine.Dockerfile). Elsewhere it is a stub, so `go build ./...` and `go vet ./...` work.
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "eirl-engine: built without GStreamer (needs linux with cgo)")
	os.Exit(1)
}
