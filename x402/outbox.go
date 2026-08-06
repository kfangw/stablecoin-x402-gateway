package x402

import (
	"context"
	"log"
	"time"
)

// Sink delivers a settlement event to an external system.
type Sink interface {
	Publish(ctx context.Context, e JournalEntry) error
}

// Outbox scans the journal and publishes unpublished entries in order through a
// Sink. Delivery is at-least-once: an entry is marked published only after the
// Sink returns success, so a crash between a successful Publish and MarkPublished
// re-delivers that entry on restart. Consumers deduplicate on JournalEntry.ID
// (the settlement transaction hash), which makes any redelivery harmless.
type Outbox struct {
	Journal  *Journal
	Sink     Sink
	Interval time.Duration
}

func (o *Outbox) interval() time.Duration {
	if o.Interval <= 0 {
		return time.Second
	}
	return o.Interval
}

// Run drains the backlog, then rescans on each tick until ctx is cancelled.
func (o *Outbox) Run(ctx context.Context) {
	t := time.NewTicker(o.interval())
	defer t.Stop()
	for {
		o.drain(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// drain publishes the unpublished entries in journal order. On a Publish failure
// it stops the pass and leaves the rest for the next tick, so ordering is
// preserved and a stalled sink does not skip ahead.
func (o *Outbox) drain(ctx context.Context) {
	for _, e := range o.Journal.Unpublished() {
		if err := o.Sink.Publish(ctx, e); err != nil {
			log.Printf("x402: outbox: publish %s failed, retrying next tick: %v", e.ID, err)
			return
		}
		if err := o.Journal.MarkPublished(e.ID); err != nil {
			log.Printf("x402: outbox: mark %s published failed: %v", e.ID, err)
			return
		}
	}
}
