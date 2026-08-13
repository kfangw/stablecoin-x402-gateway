package demoflow_test

import (
	"context"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/kfangw/stablecoin-x402-gateway/internal/demoflow"
)

// hexAddr matches chain-generated hex (addresses, tx hashes, mandate ids) that
// varies from run to run, so the golden compares the stable narrative around it.
var hexAddr = regexp.MustCompile(`0x[0-9a-fA-F]{6,}`)

func normalize(s string) string {
	return hexAddr.ReplaceAllString(s, "0xHEX")
}

// TestRunGolden pins the terminal rendering of the event stream. It guards the
// refactor that moved the demo narrative out of cmd/demo: the sequence of
// Event.Text must render to the same output the command printed before.
func TestRunGolden(t *testing.T) {
	var buf strings.Builder
	steps := 0
	err := demoflow.Run(context.Background(), func(e demoflow.Event) {
		buf.WriteString(e.Text)
		if e.Kind == "step" {
			steps++
		}
	}, func(int) {})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if steps != 13 {
		t.Fatalf("expected 13 step events, got %d", steps)
	}

	got := normalize(buf.String())
	want, err := os.ReadFile("testdata/demo.golden")
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	if got != string(want) {
		t.Errorf("rendered demo does not match testdata/demo.golden\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

// TestRunGate confirms gate is called once per numbered step, in order, so a
// caller (the browser demo) can pause between steps.
func TestRunGate(t *testing.T) {
	var gated []int
	err := demoflow.Run(context.Background(), func(demoflow.Event) {}, func(step int) {
		gated = append(gated, step)
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	want := []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13}
	if len(gated) != len(want) {
		t.Fatalf("gate called %d times, want %d: %v", len(gated), len(want), gated)
	}
	for i, s := range want {
		if gated[i] != s {
			t.Fatalf("gate order mismatch at %d: got %d want %d (%v)", i, gated[i], s, gated)
		}
	}
}
