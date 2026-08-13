package x402_test

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

// With a logger set, a settled payment emits a structured settlement event.
func TestGatewayLogsSettlement(t *testing.T) {
	gw, agent, url := receiptHarness(t, nil, false)
	var buf bytes.Buffer
	gw.Logger = slog.New(slog.NewTextHandler(&buf, nil))

	if _, err := agent.Get(url); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "settlement") {
		t.Errorf("expected a settlement log line, got %q", out)
	}
}

// A gateway with a nil logger stays silent and must not panic on the logged
// paths.
func TestGatewayNilLoggerSilent(t *testing.T) {
	gw, agent, url := receiptHarness(t, nil, false) // Logger left nil
	_ = gw
	if _, err := agent.Get(url); err != nil {
		t.Fatalf("a nil-logger gateway must serve payments without panicking: %v", err)
	}
}
