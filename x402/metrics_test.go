package x402_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kfangw/stablecoin-x402-gateway/x402"
)

// Render produces the Prometheus text format: HELP and TYPE lines and the
// counters, including labeled refusals and settlement outcomes.
func TestMetricsRender(t *testing.T) {
	m := x402.NewMetrics()
	m.IncRequest()
	m.IncRequest()
	m.IncRefusal("payment_required")
	m.IncRefusal("identity_unregistered")
	m.IncRefusal("identity_unregistered")
	m.IncSettleSuccess()
	m.IncDelivery()
	m.ObserveSettleLatency(250 * time.Millisecond)

	out := m.Render()
	for _, want := range []string{
		"# TYPE x402_requests_total counter",
		"x402_requests_total 2",
		`x402_refusals_total{code="identity_unregistered"} 2`,
		`x402_refusals_total{code="payment_required"} 1`,
		`x402_settlements_total{outcome="success"} 1`,
		"x402_deliveries_total 1",
		"x402_settlement_latency_seconds_count 1",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("metrics output missing %q\n---\n%s", want, out)
		}
	}
}

// A nil registry is safe: every method is a no-op and Render is empty.
func TestMetricsNilSafe(t *testing.T) {
	var m *x402.Metrics
	m.IncRequest()
	m.IncRefusal("x")
	m.IncSettleSuccess()
	m.ObserveSettleLatency(time.Second)
	if m.Render() != "" {
		t.Error("nil metrics must render empty")
	}
}

// Concurrent increments are counted exactly (run under -race).
func TestMetricsConcurrent(t *testing.T) {
	m := x402.NewMetrics()
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m.IncRequest()
			m.IncSettleSuccess()
		}()
	}
	wg.Wait()
	if !strings.Contains(m.Render(), "x402_requests_total 100") {
		t.Errorf("want 100 requests, got:\n%s", m.Render())
	}
}

// The gateway's endpoints respond, and a settled payment shows up in the metrics.
func TestGatewayHealthAndMetricsEndpoints(t *testing.T) {
	gw, agent, url := receiptHarness(t, nil, false)
	gw.Metrics = x402.NewMetrics()

	if _, err := agent.Get(url); err != nil {
		t.Fatal(err)
	}

	health := httptest.NewServer(gw.HealthHandler())
	defer health.Close()
	resp, err := http.Get(health.URL)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("healthz status = %d, want 200", resp.StatusCode)
	}
	resp.Body.Close()

	metrics := httptest.NewServer(gw.MetricsHandler())
	defer metrics.Close()
	mresp, err := http.Get(metrics.URL)
	if err != nil {
		t.Fatal(err)
	}
	body := make([]byte, 4096)
	n, _ := mresp.Body.Read(body)
	mresp.Body.Close()
	if !strings.Contains(string(body[:n]), `x402_settlements_total{outcome="success"} 1`) {
		t.Errorf("metrics did not record the settlement:\n%s", body[:n])
	}
}
