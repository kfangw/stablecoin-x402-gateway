package x402_test

import (
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ethereum/go-ethereum/common"

	"github.com/kfangw/stablecoin-x402-gateway/token"
	"github.com/kfangw/stablecoin-x402-gateway/x402"
)

// discoveryGateway builds a gateway with just the fields discovery needs.
func discoveryServer(t *testing.T) *httptest.Server {
	t.Helper()
	gw := &x402.Gateway{
		Token:        token.At(common.HexToAddress("0x00000000000000000000000000000000000000aa")),
		PayTo:        common.HexToAddress("0x00000000000000000000000000000000000000ee"),
		Price:        big.NewInt(500),
		Network:      "eip155:1337",
		ResourcePath: "/premium/report",
	}
	server := httptest.NewServer(http.HandlerFunc(gw.DiscoveryHandler()))
	t.Cleanup(server.Close)
	return server
}

// GET /resources returns the discovery shape, unauthenticated, with one item.
func TestDiscoveryShape(t *testing.T) {
	server := discoveryServer(t)

	resp, err := http.Get(server.URL) // no payment header
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (discovery is unauthenticated)", resp.StatusCode)
	}
	var d x402.DiscoveryResponse
	if err := json.NewDecoder(resp.Body).Decode(&d); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if d.X402Version != x402.Version {
		t.Errorf("x402Version = %d, want %d", d.X402Version, x402.Version)
	}
	if len(d.Items) != 1 {
		t.Fatalf("items = %d, want 1", len(d.Items))
	}
	it := d.Items[0]
	if it.Type != "http" {
		t.Errorf("type = %q, want http", it.Type)
	}
	if len(it.Accepts) != 1 || it.Accepts[0].MaxAmountRequired != "500" {
		t.Errorf("accepts = %+v, want one entry priced 500", it.Accepts)
	}
	if it.Resource == "" {
		t.Error("resource url is empty")
	}
}

// The agent's Discover consumes the discovery response.
func TestAgentDiscover(t *testing.T) {
	server := discoveryServer(t)
	agent := &x402.Agent{}
	d, err := agent.Discover(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Items) != 1 || d.Items[0].Accepts[0].MaxAmountRequired != "500" {
		t.Fatalf("discover parsed %+v, want one 500-priced item", d)
	}
}
