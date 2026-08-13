package x402

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// Metrics counts the events an operator watches: requests, refusals by code,
// settlements by outcome, deliveries and refunds, sessions, and settlement
// latency. It renders the Prometheus text format by hand, so the binary takes on
// no metrics client dependency. Only counters are used; a latency sum and count
// let a scraper compute an average without a histogram.
//
// Every method is safe on a nil receiver, so a gateway without metrics is silent
// and unchanged.
type Metrics struct {
	mu                 sync.Mutex
	requests           int64
	refusals           map[string]int64
	settleSuccess      int64
	settleFail         int64
	deliveries         int64
	refunds            int64
	sessionsOpened     int64
	sessionsSettled    int64
	settleLatencySum   float64
	settleLatencyCount int64
}

// NewMetrics returns an initialized metrics registry.
func NewMetrics() *Metrics {
	return &Metrics{refusals: make(map[string]int64)}
}

func (m *Metrics) IncRequest() {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.requests++
	m.mu.Unlock()
}

func (m *Metrics) IncRefusal(code string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	if m.refusals == nil {
		m.refusals = make(map[string]int64)
	}
	m.refusals[code]++
	m.mu.Unlock()
}

func (m *Metrics) IncSettleSuccess()  { m.add(func() { m.settleSuccess++ }) }
func (m *Metrics) IncSettleFail()     { m.add(func() { m.settleFail++ }) }
func (m *Metrics) IncDelivery()       { m.add(func() { m.deliveries++ }) }
func (m *Metrics) IncRefund()         { m.add(func() { m.refunds++ }) }
func (m *Metrics) IncSessionOpened()  { m.add(func() { m.sessionsOpened++ }) }
func (m *Metrics) IncSessionSettled() { m.add(func() { m.sessionsSettled++ }) }

// add runs an increment under the lock, guarding a nil receiver first so the
// closure never touches a nil struct.
func (m *Metrics) add(inc func()) {
	if m == nil {
		return
	}
	m.mu.Lock()
	inc()
	m.mu.Unlock()
}

// ObserveSettleLatency records how long a settlement took.
func (m *Metrics) ObserveSettleLatency(d time.Duration) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.settleLatencySum += d.Seconds()
	m.settleLatencyCount++
	m.mu.Unlock()
}

// Render writes the Prometheus text exposition format. A nil registry renders
// nothing.
func (m *Metrics) Render() string {
	if m == nil {
		return ""
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	var b strings.Builder
	counter := func(name, help string, value int64) {
		fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s counter\n%s %d\n", name, help, name, name, value)
	}

	counter("x402_requests_total", "Payment requests handled.", m.requests)

	fmt.Fprint(&b, "# HELP x402_refusals_total 402 responses by error code.\n# TYPE x402_refusals_total counter\n")
	codes := make([]string, 0, len(m.refusals))
	for c := range m.refusals {
		codes = append(codes, c)
	}
	sort.Strings(codes)
	for _, c := range codes {
		fmt.Fprintf(&b, "x402_refusals_total{code=%q} %d\n", c, m.refusals[c])
	}

	fmt.Fprint(&b, "# HELP x402_settlements_total Settlements by outcome.\n# TYPE x402_settlements_total counter\n")
	fmt.Fprintf(&b, "x402_settlements_total{outcome=\"success\"} %d\n", m.settleSuccess)
	fmt.Fprintf(&b, "x402_settlements_total{outcome=\"fail\"} %d\n", m.settleFail)

	counter("x402_deliveries_total", "Assets delivered after settlement.", m.deliveries)
	counter("x402_refunds_total", "Refunds recorded after a failed delivery.", m.refunds)

	fmt.Fprint(&b, "# HELP x402_sessions_total Payment sessions by phase.\n# TYPE x402_sessions_total counter\n")
	fmt.Fprintf(&b, "x402_sessions_total{phase=\"opened\"} %d\n", m.sessionsOpened)
	fmt.Fprintf(&b, "x402_sessions_total{phase=\"settled\"} %d\n", m.sessionsSettled)

	fmt.Fprint(&b, "# HELP x402_settlement_latency_seconds Settlement latency, as a sum and count for the average.\n# TYPE x402_settlement_latency_seconds counter\n")
	fmt.Fprintf(&b, "x402_settlement_latency_seconds_sum %g\n", m.settleLatencySum)
	fmt.Fprintf(&b, "x402_settlement_latency_seconds_count %d\n", m.settleLatencyCount)

	return b.String()
}
