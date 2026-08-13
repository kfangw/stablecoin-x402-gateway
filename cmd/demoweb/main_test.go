package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kfangw/stablecoin-x402-gateway/internal/demoflow"
)

// connectSSE opens an /events stream and returns a channel of decoded events and
// a cancel that closes the stream.
func connectSSE(t *testing.T, url string) (<-chan demoflow.Event, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	out := make(chan demoflow.Event, 256)
	go func() {
		defer close(out)
		defer resp.Body.Close()
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
		for sc.Scan() {
			line := sc.Text()
			data, ok := strings.CutPrefix(line, "data: ")
			if !ok {
				continue
			}
			var e demoflow.Event
			if err := json.Unmarshal([]byte(data), &e); err != nil {
				continue
			}
			select {
			case out <- e:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, cancel
}

// nextStep reads until the next step event or the deadline.
func nextStep(t *testing.T, ch <-chan demoflow.Event, within time.Duration) demoflow.Event {
	t.Helper()
	timeout := time.After(within)
	for {
		select {
		case e, ok := <-ch:
			if !ok {
				t.Fatal("event stream closed early")
			}
			if e.Kind == "step" {
				return e
			}
		case <-timeout:
			t.Fatal("timed out waiting for a step event")
		}
	}
}

func control(t *testing.T, base, action string) {
	t.Helper()
	resp, err := http.Post(base+"/control", "application/json", strings.NewReader(`{"action":"`+action+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("control %q: status %d", action, resp.StatusCode)
	}
}

// After start, each next advances exactly one step, and the steps arrive in
// order over the stream.
func TestControlStepsInOrder(t *testing.T) {
	h := newHub(false, 10*time.Millisecond)
	srv := httptest.NewServer(h.handler())
	defer srv.Close()

	ch, cancel := connectSSE(t, srv.URL+"/events")
	defer cancel()

	control(t, srv.URL, "start")
	for i := 1; i <= 5; i++ {
		control(t, srv.URL, "next")
		e := nextStep(t, ch, 5*time.Second)
		if e.Step != i {
			t.Fatalf("step %d, want %d", e.Step, i)
		}
	}
}

// In manual mode nothing advances until a next arrives.
func TestManualWaitsForNext(t *testing.T) {
	h := newHub(false, 10*time.Millisecond)
	srv := httptest.NewServer(h.handler())
	defer srv.Close()

	ch, cancel := connectSSE(t, srv.URL+"/events")
	defer cancel()

	control(t, srv.URL, "start")
	select {
	case e := <-ch:
		if e.Kind == "step" {
			t.Fatalf("step %d arrived without a next", e.Step)
		}
	case <-time.After(300 * time.Millisecond):
	}

	control(t, srv.URL, "next")
	if e := nextStep(t, ch, 5*time.Second); e.Step != 1 {
		t.Fatalf("first step = %d, want 1", e.Step)
	}
}

// In auto mode steps advance on their own, without a next per step.
func TestAutoAdvances(t *testing.T) {
	h := newHub(true, 5*time.Millisecond)
	srv := httptest.NewServer(h.handler())
	defer srv.Close()

	ch, cancel := connectSSE(t, srv.URL+"/events")
	defer cancel()

	control(t, srv.URL, "start")
	for i := 1; i <= 3; i++ {
		if e := nextStep(t, ch, 5*time.Second); e.Step != i {
			t.Fatalf("auto step %d, want %d", e.Step, i)
		}
	}
}

// A browser that connects after several steps replays them from the start.
func TestLateJoinerReplays(t *testing.T) {
	h := newHub(false, 10*time.Millisecond)
	srv := httptest.NewServer(h.handler())
	defer srv.Close()

	early, cancelEarly := connectSSE(t, srv.URL+"/events")
	defer cancelEarly()

	control(t, srv.URL, "start")
	for i := 1; i <= 3; i++ {
		control(t, srv.URL, "next")
		nextStep(t, early, 5*time.Second)
	}

	late, cancelLate := connectSSE(t, srv.URL+"/events")
	defer cancelLate()
	for i := 1; i <= 3; i++ {
		if e := nextStep(t, late, 5*time.Second); e.Step != i {
			t.Fatalf("replayed step %d, want %d", e.Step, i)
		}
	}
}

// Reset clears the run: connected browsers get a reset, a browser connecting
// afterward sees an empty log, and a fresh start replays from step 1.
func TestResetRestarts(t *testing.T) {
	h := newHub(false, 10*time.Millisecond)
	srv := httptest.NewServer(h.handler())
	defer srv.Close()

	ch, cancel := connectSSE(t, srv.URL+"/events")
	defer cancel()

	control(t, srv.URL, "start")
	control(t, srv.URL, "next")
	nextStep(t, ch, 5*time.Second)
	control(t, srv.URL, "next")
	nextStep(t, ch, 5*time.Second)

	control(t, srv.URL, "reset")
	gotReset := false
	timeout := time.After(2 * time.Second)
	for !gotReset {
		select {
		case e := <-ch:
			if e.Kind == "reset" {
				gotReset = true
			}
		case <-timeout:
			t.Fatal("no reset event after reset")
		}
	}

	late, cancelLate := connectSSE(t, srv.URL+"/events")
	defer cancelLate()
	select {
	case e := <-late:
		if e.Kind == "step" {
			t.Fatalf("late joiner saw step %d after reset", e.Step)
		}
	case <-time.After(300 * time.Millisecond):
	}

	control(t, srv.URL, "start")
	control(t, srv.URL, "next")
	if e := nextStep(t, late, 5*time.Second); e.Step != 1 {
		t.Fatalf("after restart step = %d, want 1", e.Step)
	}
}

func TestHealthzAndIndex(t *testing.T) {
	h := newHub(false, time.Second)
	srv := httptest.NewServer(h.handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || strings.TrimSpace(string(body)) != "ok" {
		t.Fatalf("healthz = %d %q", resp.StatusCode, body)
	}

	resp, err = http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("index status = %d", resp.StatusCode)
	}
	if !strings.Contains(strings.ToLower(string(body)), "<!doctype html>") {
		t.Error("index did not serve HTML")
	}
}
