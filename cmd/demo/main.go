// demo runs the full flow, from issuance to x402 settlement, on an in-process
// simulated chain. `go run ./cmd/demo` reproduces it without any external node.
// The narrative lives in internal/demoflow as an event stream; this command
// renders each event's text, and cmd/demoweb renders the same stream in a
// browser.
package main

import (
	"context"
	"fmt"
	"log"

	"github.com/kfangw/stablecoin-x402-gateway/internal/demoflow"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	// The terminal renders each event's text; gate is a no-op, so the demo runs
	// straight through without pausing.
	return demoflow.Run(context.Background(), func(e demoflow.Event) {
		fmt.Print(e.Text)
	}, func(int) {})
}
