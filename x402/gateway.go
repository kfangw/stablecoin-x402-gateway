package x402

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"

	"github.com/kfangw/stablecoin-x402-gateway/token"
)

// Backend is the minimal interface needed to wait for settlement transactions.
type Backend interface {
	bind.DeployBackend // TransactionReceipt, CodeAt
}

// Gateway is an x402 payment gateway in front of a paid resource. It builds the
// 402 payment terms, delegates verification and settlement to a Facilitator,
// and records the settlements it observes. With a remote Facilitator the
// gateway needs neither an RPC connection nor a private key.
type Gateway struct {
	Token   *token.Token
	Backend Backend
	// Transactor signs settlement transactions for the built-in local
	// facilitator. It is unused (and may be nil) when a remote Facilitator is set.
	Transactor *bind.TransactOpts
	PayTo      common.Address // address that receives the payment
	Price      *big.Int       // resource price in tKRW
	Network    string         // e.g. eip155:1337
	Timeout    time.Duration  // how long to wait for settlement finality

	// Commit is called by the local facilitator right after a settlement
	// transaction is submitted. On a simulated backend it mines a block;
	// against a real node leave it nil.
	Commit func()

	// Facilitator verifies and settles payments. If nil, a LocalFacilitator is
	// built lazily from Token, Backend, Transactor, Commit and Timeout, which
	// keeps the simulated demo and the existing tests working unchanged.
	Facilitator Facilitator
	facOnce     sync.Once

	mu          sync.Mutex
	Settlements []SettlementRecord
}

// SettlementRecord describes a settlement the gateway executed.
type SettlementRecord struct {
	Payer  common.Address
	Amount *big.Int
	TxHash common.Hash
	Time   time.Time
}

// Requirements builds the payment terms of this gateway.
func (g *Gateway) Requirements(resource string) PaymentRequirements {
	return PaymentRequirements{
		Scheme:            SchemeExact,
		Network:           g.Network,
		MaxAmountRequired: g.Price.String(),
		Resource:          resource,
		Description:       "x402 protected resource",
		MimeType:          "application/json",
		PayTo:             g.PayTo.Hex(),
		MaxTimeoutSeconds: int(g.timeout().Seconds()),
		Asset:             g.Token.Address.Hex(),
		Extra:             map[string]string{"name": "KRW Test Stablecoin", "version": "1"},
	}
}

func (g *Gateway) timeout() time.Duration {
	if g.Timeout == 0 {
		return 60 * time.Second
	}
	return g.Timeout
}

// facilitator returns the configured Facilitator, building a LocalFacilitator
// from the gateway's own fields on first use if none was set.
func (g *Gateway) facilitator() Facilitator {
	g.facOnce.Do(func() {
		if g.Facilitator == nil {
			g.Facilitator = &LocalFacilitator{
				Token:      g.Token,
				Backend:    g.Backend,
				Transactor: g.Transactor,
				Commit:     g.Commit,
				Timeout:    g.Timeout,
			}
		}
	})
	return g.Facilitator
}

// Middleware protects a handler with x402 payments.
// Requests without an X-PAYMENT header receive 402 and the payment terms;
// requests with a valid payment are settled and passed through.
func (g *Gateway) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resource := "http://" + r.Host + r.URL.Path

		header := r.Header.Get(HeaderPayment)
		if header == "" {
			g.writeRequirements(w, resource, "X-PAYMENT header is required")
			return
		}

		record, errMsg := g.verifyAndSettle(r.Context(), header, g.Requirements(resource))
		if errMsg != "" {
			g.writeRequirements(w, resource, errMsg)
			return
		}

		settleHeader, err := EncodeHeader(SettlementResponse{
			Success:     true,
			Transaction: record.TxHash.Hex(),
			Network:     g.Network,
			Payer:       record.Payer.Hex(),
		})
		if err == nil {
			w.Header().Set(HeaderPaymentResponse, settleHeader)
		}
		next.ServeHTTP(w, r)
	})
}

func (g *Gateway) writeRequirements(w http.ResponseWriter, resource, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusPaymentRequired)
	body, _ := json.Marshal(RequirementsResponse{
		X402Version: Version,
		Error:       msg,
		Accepts:     []PaymentRequirements{g.Requirements(resource)},
	})
	_, _ = w.Write(body)
}

// verifyAndSettle runs the payment through the facilitator (verify, then
// settle) and records the outcome. On failure it returns a human-readable
// reason (empty on success), which the caller serves as the 402 error.
func (g *Gateway) verifyAndSettle(ctx context.Context, header string, reqs PaymentRequirements) (SettlementRecord, string) {
	var p PaymentPayload
	if err := DecodeHeader(header, &p); err != nil {
		return SettlementRecord{}, fmt.Sprintf("invalid X-PAYMENT header: %v", err)
	}

	fac := g.facilitator()

	vr, err := fac.Verify(ctx, p, reqs)
	if err != nil {
		return SettlementRecord{}, fmt.Sprintf("verification error: %v", err)
	}
	if vr == nil || !vr.IsValid {
		reason := "payment verification failed"
		if vr != nil && vr.InvalidReason != "" {
			reason = vr.InvalidReason
		}
		return SettlementRecord{}, reason
	}

	sr, err := fac.Settle(ctx, p, reqs)
	if err != nil {
		return SettlementRecord{}, fmt.Sprintf("settlement error: %v", err)
	}
	if sr == nil || !sr.Success {
		reason := "settlement failed"
		if sr != nil && sr.ErrorReason != "" {
			reason = sr.ErrorReason
		}
		return SettlementRecord{}, reason
	}

	// Reconstruct the record from the (already validated) payload and receipt.
	auth, _, err := parseExactPayload(p.Payload)
	if err != nil {
		return SettlementRecord{}, fmt.Sprintf("record settlement: %v", err)
	}
	record := SettlementRecord{
		Payer:  auth.From,
		Amount: auth.Value,
		TxHash: common.HexToHash(sr.Transaction),
		Time:   time.Now(),
	}
	g.mu.Lock()
	g.Settlements = append(g.Settlements, record)
	g.mu.Unlock()
	return record, ""
}
