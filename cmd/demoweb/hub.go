package main

import (
	"context"
	"sync"
	"time"

	"github.com/kfangw/stablecoin-x402-gateway/internal/demoflow"
)

// hub runs one demo at a time and fans its events out to connected browsers. It
// keeps every event of the current run in memory so a browser that connects
// late replays the run from the start. Control is last-command-wins: this is a
// presentation tool, not a multi-tenant service.
type hub struct {
	mu sync.Mutex

	events []demoflow.Event                 // the current run's events, for replay
	subs   map[chan demoflow.Event]struct{} // connected SSE streams

	running bool
	runCtx  context.Context
	cancel  context.CancelFunc
	gen     int // run generation; emits from a superseded run are dropped

	auto      bool
	stepDelay time.Duration

	next chan struct{} // one signal advances the gate by a step (manual mode)
	wake chan struct{} // closed and replaced when control state changes
}

func newHub(auto bool, stepDelay time.Duration) *hub {
	return &hub{
		subs:      make(map[chan demoflow.Event]struct{}),
		auto:      auto,
		stepDelay: stepDelay,
		next:      make(chan struct{}, 1),
		wake:      make(chan struct{}),
	}
}

// start launches the demo if it is not already running. When auto is true it
// also switches the run to auto-play. Repeated starts while running only toggle
// auto, so the control is idempotent.
func (h *hub) start(auto bool) {
	h.mu.Lock()
	if auto {
		h.auto = true
		h.signalControl()
	}
	if h.running {
		h.mu.Unlock()
		return
	}
	h.gen++
	gen := h.gen
	ctx, cancel := context.WithCancel(context.Background())
	h.runCtx = ctx
	h.cancel = cancel
	h.running = true
	h.mu.Unlock()

	go func() {
		emit := func(e demoflow.Event) { h.emit(gen, e) }
		err := demoflow.Run(ctx, emit, h.gate)
		h.mu.Lock()
		if h.gen == gen {
			h.running = false
		}
		h.mu.Unlock()
		if err != nil && ctx.Err() == nil {
			emit(demoflow.Event{Kind: "error", Text: "demo failed: " + err.Error() + "\n", ErrorCode: "demo_failed"})
		}
	}()
}

// next advances one step in manual mode. The signal is buffered by one, so a
// click while a step is still emitting is not lost; extra clicks are dropped.
func (h *hub) sendNext() {
	select {
	case h.next <- struct{}{}:
	default:
	}
}

// reset cancels the running demo, clears the event log, and tells connected
// browsers to clear. The next start begins a fresh run. Because the demo is
// in-process, this restarts in well under a second.
func (h *hub) reset() {
	h.mu.Lock()
	if h.cancel != nil {
		h.cancel()
	}
	h.gen++ // orphan any emits still in flight from the old run
	h.running = false
	h.auto = false
	h.events = nil
	subs := h.snapshotSubs()
	h.signalControl()
	select {
	case <-h.next:
	default:
	}
	h.mu.Unlock()

	for _, c := range subs {
		select {
		case c <- demoflow.Event{Kind: "reset"}:
		default:
		}
	}
}

// gate blocks between steps. In manual mode it waits for a next signal; in auto
// mode it waits stepDelay (or a next). A control change (auto toggled, or a
// reset) wakes it to re-evaluate.
func (h *hub) gate(int) {
	h.mu.Lock()
	ctx := h.runCtx
	h.mu.Unlock()
	for {
		h.mu.Lock()
		auto := h.auto
		delay := h.stepDelay
		wake := h.wake
		h.mu.Unlock()

		if auto {
			select {
			case <-ctx.Done():
				return
			case <-time.After(delay):
				return
			case <-h.next:
				return
			case <-wake:
				continue
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-h.next:
			return
		case <-wake:
			continue
		}
	}
}

// emit records an event and fans it out, unless the run has been superseded.
func (h *hub) emit(gen int, e demoflow.Event) {
	h.mu.Lock()
	if gen != h.gen {
		h.mu.Unlock()
		return
	}
	h.events = append(h.events, e)
	subs := h.snapshotSubs()
	h.mu.Unlock()

	for _, c := range subs {
		select {
		case c <- e:
		default:
			// The channel is large enough for a whole run; a full buffer means a
			// dead client, whose stream goroutine will clean itself up.
		}
	}
}

// subscribe registers a stream and returns the current event log atomically, so
// the stream replays exactly the events emitted before it joined and receives
// exactly those emitted after, with no gap or duplicate.
func (h *hub) subscribe() (chan demoflow.Event, []demoflow.Event) {
	ch := make(chan demoflow.Event, 1024)
	h.mu.Lock()
	past := append([]demoflow.Event(nil), h.events...)
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch, past
}

func (h *hub) unsubscribe(ch chan demoflow.Event) {
	h.mu.Lock()
	delete(h.subs, ch)
	h.mu.Unlock()
}

// snapshotSubs copies the subscriber channels. Callers hold h.mu.
func (h *hub) snapshotSubs() []chan demoflow.Event {
	subs := make([]chan demoflow.Event, 0, len(h.subs))
	for c := range h.subs {
		subs = append(subs, c)
	}
	return subs
}

// signalControl wakes every waiting gate by closing the shared wake channel and
// replacing it. Callers hold h.mu.
func (h *hub) signalControl() {
	close(h.wake)
	h.wake = make(chan struct{})
}
