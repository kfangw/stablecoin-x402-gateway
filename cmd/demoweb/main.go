// demoweb serves the in-process demo in a browser. It runs the same narrative
// as cmd/demo (from internal/demoflow) but streams each event over SSE so a
// presenter can step through it live. Open http://localhost:8404 after starting.
//
//	go run ./cmd/demoweb
//
// The page connects to /events for the event stream and posts to /control to
// start, advance, auto-play, or reset the run. Everything runs on an in-process
// simulated chain, so no node or external service is required.
package main

import (
	_ "embed"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"time"
)

//go:embed index.html
var indexHTML []byte

func main() {
	listen := flag.String("listen", ":8404", "address to serve on")
	auto := flag.Bool("auto", false, "auto-play the demo instead of waiting for Next")
	stepDelay := flag.Duration("step-delay", 1500*time.Millisecond, "delay between steps in auto mode")
	flag.Parse()

	h := newHub(*auto, *stepDelay)
	log.Printf("demoweb listening on %s (open http://localhost%s)", *listen, portHint(*listen))
	if err := http.ListenAndServe(*listen, h.handler()); err != nil {
		log.Fatal(err)
	}
}

// handler wires the routes. It is separate from main so tests can serve it with
// httptest.
func (h *hub) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", h.handleIndex)
	mux.HandleFunc("/events", h.handleEvents)
	mux.HandleFunc("/control", h.handleControl)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "ok")
	})
	return mux
}

func (h *hub) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(indexHTML)
}

// handleEvents streams the demo as Server-Sent Events. On connect it replays the
// current run's events, then forwards live ones until the client disconnects.
func (h *hub) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ch, past := h.subscribe()
	defer h.unsubscribe(ch)

	for _, e := range past {
		writeEvent(w, e)
	}
	flusher.Flush()

	for {
		select {
		case <-r.Context().Done():
			return
		case e := <-ch:
			writeEvent(w, e)
			flusher.Flush()
		}
	}
}

// handleControl drives the run. Body: {"action":"start"|"next"|"auto"|"reset"}.
func (h *hub) handleControl(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Action string `json:"action"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	switch body.Action {
	case "start":
		h.start(false)
	case "auto":
		h.start(true)
	case "next":
		h.sendNext()
	case "reset":
		h.reset()
	default:
		http.Error(w, "unknown action", http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// writeEvent writes one event as an SSE data frame.
func writeEvent(w http.ResponseWriter, e interface{}) {
	b, err := json.Marshal(e)
	if err != nil {
		return
	}
	fmt.Fprintf(w, "data: %s\n\n", b)
}

// portHint returns the :port portion of a listen address for the startup log.
func portHint(listen string) string {
	if len(listen) > 0 && listen[0] == ':' {
		return listen
	}
	return " at " + listen
}
