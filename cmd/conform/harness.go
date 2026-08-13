package main

import (
	"context"
	"encoding/hex"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient/simulated"
	"github.com/ethereum/go-ethereum/params"

	"github.com/kfangw/stablecoin-x402-gateway/token"
	"github.com/kfangw/stablecoin-x402-gateway/wallet"
	"github.com/kfangw/stablecoin-x402-gateway/x402"
)

// harness is an in-process gateway on a simulated chain, with the pieces the
// conformance checks need: a funded payer, a way to sign payments, and access to
// the gateway's settlement count and how many times a resource was served.
type harness struct {
	sim       *simulated.Backend
	server    *httptest.Server
	gw        *x402.Gateway
	payer     *wallet.Wallet
	domain    [32]byte
	payTo     common.Address
	network   string
	price     int64
	resourceA string
	resourceB string
	servedN   int64
}

func (h *harness) close()           { h.server.Close(); h.sim.Close() }
func (h *harness) settlements() int { return len(h.gw.Settlements) }
func (h *harness) served() int      { return int(atomic.LoadInt64(&h.servedN)) }

// serializingFacilitator serializes settlement so concurrent requests reach the
// simulated backend one at a time. Verification stays concurrent. The single
// settlement is still enforced by the on-chain nonce; this only keeps the
// simulated backend safe under load.
type serializingFacilitator struct {
	inner x402.Facilitator
	mu    *sync.Mutex
}

func (s serializingFacilitator) Verify(ctx context.Context, p x402.PaymentPayload, r x402.PaymentRequirements) (*x402.VerifyResult, error) {
	return s.inner.Verify(ctx, p, r)
}

func (s serializingFacilitator) Settle(ctx context.Context, p x402.PaymentPayload, r x402.PaymentRequirements) (*x402.SettlementResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inner.Settle(ctx, p, r)
}

// failingSettleFacilitator verifies normally but always fails settlement, to
// probe that a failed settlement delivers nothing and records nothing.
type failingSettleFacilitator struct {
	inner x402.Facilitator
}

func (f failingSettleFacilitator) Verify(ctx context.Context, p x402.PaymentPayload, r x402.PaymentRequirements) (*x402.VerifyResult, error) {
	return f.inner.Verify(ctx, p, r)
}

func (f failingSettleFacilitator) Settle(ctx context.Context, p x402.PaymentPayload, r x402.PaymentRequirements) (*x402.SettlementResponse, error) {
	return &x402.SettlementResponse{Success: false, ErrorReason: "injected settlement failure"}, nil
}

// buildHarness sets up the chain, token, funded payer, and a gateway, then wraps
// the facilitator with wrap and mounts the resource paths.
func buildHarness(requireBound, protect bool, wrap func(x402.Facilitator, *simulated.Backend, common.Address) x402.Facilitator) (*harness, error) {
	ctx := context.Background()
	chainID := params.AllDevChainProtocolChanges.ChainID

	issuerKey, _ := crypto.GenerateKey()
	gatewayKey, _ := crypto.GenerateKey()
	payer, _ := wallet.New()
	issuerAddr := crypto.PubkeyToAddress(issuerKey.PublicKey)
	gatewayAddr := crypto.PubkeyToAddress(gatewayKey.PublicKey)

	eth := new(big.Int).Mul(big.NewInt(100), big.NewInt(params.Ether))
	sim := simulated.NewBackend(types.GenesisAlloc{
		issuerAddr:  {Balance: eth},
		gatewayAddr: {Balance: eth},
	})
	client := sim.Client()

	issuerOpts, _ := bind.NewKeyedTransactorWithChainID(issuerKey, chainID)
	gatewayOpts, _ := bind.NewKeyedTransactorWithChainID(gatewayKey, chainID)

	tok, deployTx, err := token.Deploy(issuerOpts, client)
	if err != nil {
		sim.Close()
		return nil, err
	}
	sim.Commit()
	if _, err := bind.WaitDeployed(ctx, client, deployTx); err != nil {
		sim.Close()
		return nil, err
	}
	mintTx, err := tok.Mint(issuerOpts, payer.Address, big.NewInt(1_000_000))
	if err != nil {
		sim.Close()
		return nil, err
	}
	sim.Commit()
	if _, err := bind.WaitMined(ctx, client, mintTx); err != nil {
		sim.Close()
		return nil, err
	}
	domain, err := tok.DomainSeparator()
	if err != nil {
		sim.Close()
		return nil, err
	}

	const price = 1
	h := &harness{
		sim: sim, payer: payer, domain: domain, payTo: gatewayAddr,
		network: fmt.Sprintf("eip155:%s", chainID), price: price,
	}
	commit := func() { sim.Commit() }
	gw := &x402.Gateway{
		Token:             tok,
		Backend:           client,
		Transactor:        gatewayOpts,
		PayTo:             gatewayAddr,
		Price:             big.NewInt(price),
		Network:           h.network,
		Commit:            commit,
		RequireBoundNonce: requireBound,
	}
	base := &x402.LocalFacilitator{Token: tok, Backend: client, Transactor: gatewayOpts, Commit: commit}
	gw.Facilitator = wrap(base, sim, gatewayAddr)
	h.gw = gw

	resource := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&h.servedN, 1)
		fmt.Fprint(w, `{"data":"premium"}`)
	})
	mux := http.NewServeMux()
	if protect {
		mux.Handle("/a", gw.Middleware(resource))
		mux.Handle("/b", gw.Middleware(resource))
	} else {
		// An unprotected endpoint that serves without payment, to confirm the
		// checks flag a non-conforming target.
		mux.Handle("/a", resource)
		mux.Handle("/b", resource)
	}
	h.server = httptest.NewServer(mux)
	h.resourceA = h.server.URL + "/a"
	h.resourceB = h.server.URL + "/b"
	return h, nil
}

func newSelfHarness() (*harness, error) {
	mu := &sync.Mutex{}
	return buildHarness(true, true, func(f x402.Facilitator, _ *simulated.Backend, _ common.Address) x402.Facilitator {
		return serializingFacilitator{inner: f, mu: mu}
	})
}

func newFailingHarness() (*harness, error) {
	return buildHarness(true, true, func(f x402.Facilitator, _ *simulated.Backend, _ common.Address) x402.Facilitator {
		return failingSettleFacilitator{inner: f}
	})
}

// sign builds an X-PAYMENT header for the given resource, bound to it when bound
// is true (the nonce derives from a seed and the resource url).
func (h *harness) sign(resourceURL string, bound bool) (string, error) {
	var nonce [32]byte
	seedHex := ""
	if bound {
		seed, err := wallet.NewNonce()
		if err != nil {
			return "", err
		}
		nonce = wallet.BoundNonce(seed, resourceURL)
		seedHex = "0x" + hex.EncodeToString(seed[:])
	} else {
		n, err := wallet.NewNonce()
		if err != nil {
			return "", err
		}
		nonce = n
	}
	auth := wallet.Authorization{
		From:        h.payer.Address,
		To:          h.payTo,
		Value:       big.NewInt(h.price),
		ValidAfter:  big.NewInt(0),
		ValidBefore: big.NewInt(time.Now().Unix() + 300),
		Nonce:       nonce,
	}
	sig, err := h.payer.SignAuthorization(h.domain, auth)
	if err != nil {
		return "", err
	}
	return x402.EncodeHeader(x402.PaymentPayload{
		X402Version: x402.Version,
		Scheme:      x402.SchemeExact,
		Network:     h.network,
		Payload: x402.ExactPayload{
			Signature: "0x" + hex.EncodeToString(sig),
			NonceSeed: seedHex,
			Authorization: x402.AuthorizationJSON{
				From: auth.From.Hex(), To: auth.To.Hex(), Value: auth.Value.String(),
				ValidAfter: auth.ValidAfter.String(), ValidBefore: auth.ValidBefore.String(),
				Nonce: "0x" + hex.EncodeToString(nonce[:]),
			},
		},
	})
}

// post sends a GET carrying the payment header and returns the status code.
func (h *harness) post(resourceURL, header string) int {
	req, err := http.NewRequest(http.MethodGet, resourceURL, nil)
	if err != nil {
		return 0
	}
	req.Header.Set(x402.HeaderPayment, header)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0
	}
	resp.Body.Close()
	return resp.StatusCode
}
