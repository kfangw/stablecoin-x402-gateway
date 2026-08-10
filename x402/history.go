package x402

import (
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
)

// ConfirmationHistory records, per delegator, how the confirmation flow has gone:
// how often the delegator was asked, how often a confirmation was accepted, and
// how often an attached confirmation failed to match. Policies read a snapshot
// through PaymentContext.History; this file only records and exposes it. Making
// it durable is left to the decision log. It is safe for concurrent use.
type ConfirmationHistory struct {
	mu          sync.Mutex
	byDelegator map[common.Address]*delegatorCounters
}

type delegatorCounters struct {
	asks          int
	confirmations int
	failures      int
	lastAskedAt   int64
}

// DelegatorHistory is a read-only snapshot of one delegator's counters.
type DelegatorHistory struct {
	Asks          int   `json:"asks"`
	Confirmations int   `json:"confirmations"`
	Failures      int   `json:"failures"`
	LastAskedAt   int64 `json:"lastAskedAt"` // unix seconds, 0 if never asked
}

func newConfirmationHistory() *ConfirmationHistory {
	return &ConfirmationHistory{byDelegator: make(map[common.Address]*delegatorCounters)}
}

func (h *ConfirmationHistory) counters(d common.Address) *delegatorCounters {
	c := h.byDelegator[d]
	if c == nil {
		c = &delegatorCounters{}
		h.byDelegator[d] = c
	}
	return c
}

func (h *ConfirmationHistory) recordAsk(d common.Address, now time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	c := h.counters(d)
	c.asks++
	c.lastAskedAt = now.Unix()
}

func (h *ConfirmationHistory) recordConfirmation(d common.Address) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.counters(d).confirmations++
}

func (h *ConfirmationHistory) recordFailure(d common.Address) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.counters(d).failures++
}

// Snapshot returns the current counters for a delegator.
func (h *ConfirmationHistory) Snapshot(d common.Address) DelegatorHistory {
	h.mu.Lock()
	defer h.mu.Unlock()
	c := h.byDelegator[d]
	if c == nil {
		return DelegatorHistory{}
	}
	return DelegatorHistory{
		Asks:          c.asks,
		Confirmations: c.confirmations,
		Failures:      c.failures,
		LastAskedAt:   c.lastAskedAt,
	}
}
