package x402_test

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kfangw/stablecoin-x402-gateway/token"
	"github.com/kfangw/stablecoin-x402-gateway/x402"
)

// newFacilitatorServer stands up an HTTP facilitator around a LocalFacilitator,
// mirroring cmd/facilitator, so RemoteFacilitator can be exercised over the wire.
func newFacilitatorServer(t *testing.T, fac *x402.LocalFacilitator) *httptest.Server {
	t.Helper()
	decode := func(r *http.Request) (*x402.FacilitatorRequest, error) {
		var req x402.FacilitatorRequest
		err := json.NewDecoder(r.Body).Decode(&req)
		return &req, err
	}
	writeJSON := func(w http.ResponseWriter, v interface{}) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/verify", func(w http.ResponseWriter, r *http.Request) {
		req, err := decode(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		vr, err := fac.Verify(r.Context(), req.PaymentPayload, req.PaymentRequirements)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, vr)
	})
	mux.HandleFunc("/settle", func(w http.ResponseWriter, r *http.Request) {
		req, err := decode(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		sr, err := fac.Settle(r.Context(), req.PaymentPayload, req.PaymentRequirements)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, sr)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestRemoteVerifyValid(t *testing.T) {
	f := newFacFixture(t, 500, 10_000)
	srv := newFacilitatorServer(t, f.fac)
	remote := &x402.RemoteFacilitator{BaseURL: srv.URL}

	vr, err := remote.Verify(context.Background(), f.payload(t, f.payer, f.price, f.network), f.reqs)
	if err != nil {
		t.Fatalf("remote verify: %v", err)
	}
	if !vr.IsValid || vr.Payer != f.payer.Address.Hex() {
		t.Fatalf("want valid payer=%s, got isValid=%v payer=%s", f.payer.Address.Hex(), vr.IsValid, vr.Payer)
	}
}

func TestRemoteVerifyReasonPropagated(t *testing.T) {
	f := newFacFixture(t, 500, 10_000)
	srv := newFacilitatorServer(t, f.fac)
	remote := &x402.RemoteFacilitator{BaseURL: srv.URL}

	// Wrong network: the invalid reason must survive the round trip.
	vr, err := remote.Verify(context.Background(), f.payload(t, f.payer, f.price, "eip155:9999"), f.reqs)
	if err != nil {
		t.Fatalf("remote verify: %v", err)
	}
	if vr.IsValid || !strings.Contains(vr.InvalidReason, "network") {
		t.Fatalf("want propagated network reason, got isValid=%v reason=%q", vr.IsValid, vr.InvalidReason)
	}
}

func TestRemoteSettleMovesBalance(t *testing.T) {
	f := newFacFixture(t, 500, 10_000)
	srv := newFacilitatorServer(t, f.fac)
	remote := &x402.RemoteFacilitator{BaseURL: srv.URL}

	sr, err := remote.Settle(context.Background(), f.payload(t, f.payer, f.price, f.network), f.reqs)
	if err != nil {
		t.Fatalf("remote settle: %v", err)
	}
	if !sr.Success {
		t.Fatalf("settle failed: %s", sr.ErrorReason)
	}
	payerBal, _ := f.tok.BalanceOf(f.payer.Address)
	payToBal, _ := f.tok.BalanceOf(f.payTo)
	if payerBal.Int64() != 9_500 || payToBal.Int64() != 500 {
		t.Errorf("balances payer=%s payTo=%s, want 9500/500", payerBal, payToBal)
	}
}

// The gateway drives a payment end to end through a remote facilitator: the
// gateway itself holds no chain handle (token.At) and no key.
func TestGatewayWithRemoteFacilitator(t *testing.T) {
	f := newFacFixture(t, 500, 10_000)
	facSrv := newFacilitatorServer(t, f.fac)

	gw := &x402.Gateway{
		Token:       token.At(f.tok.Address),
		PayTo:       f.payTo,
		Price:       f.price,
		Network:     f.network,
		Facilitator: &x402.RemoteFacilitator{BaseURL: facSrv.URL},
	}
	resource := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":"premium"}`)
	})
	gwSrv := httptest.NewServer(gw.Middleware(resource))
	t.Cleanup(gwSrv.Close)

	agent := &x402.Agent{Wallet: f.payer, DomainSeparator: f.domain, MaxAmount: big.NewInt(10_000)}
	result, err := agent.Get(gwSrv.URL + "/r")
	if err != nil {
		t.Fatalf("agent get: %v", err)
	}
	if result.StatusCode != http.StatusOK || !result.Paid {
		t.Fatalf("status = %d paid=%v, want 200 paid", result.StatusCode, result.Paid)
	}
	if result.Settlement == nil || !result.Settlement.Success {
		t.Fatal("settlement response missing")
	}

	payerBal, _ := f.tok.BalanceOf(f.payer.Address)
	payToBal, _ := f.tok.BalanceOf(f.payTo)
	if payerBal.Int64() != 9_500 || payToBal.Int64() != 500 {
		t.Errorf("balances payer=%s payTo=%s, want 9500/500", payerBal, payToBal)
	}
	if len(gw.Settlements) != 1 {
		t.Errorf("settlement records = %d, want 1", len(gw.Settlements))
	}

	// Replaying the same authorization is rejected through the remote path too.
	replay, _ := http.NewRequest(http.MethodGet, gwSrv.URL+"/r", nil)
	replay.Header.Set(x402.HeaderPayment, agent.LastPaymentHeader())
	resp, err := http.DefaultClient.Do(replay)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusPaymentRequired {
		t.Errorf("replay status = %d, want 402", resp.StatusCode)
	}
}
