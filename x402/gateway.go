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

	// Policy decides whether to approve a payment before settlement. If nil,
	// AlwaysVerify is used, which approves exactly when verification passed.
	Policy Policy

	// Journal, if set, durably records each settlement before the gateway
	// answers, so settlements survive a crash and an outbox can publish them.
	// When nil the gateway keeps its original in-memory-only behavior.
	Journal *Journal

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

// policy returns the configured accept policy, defaulting to AlwaysVerify.
func (g *Gateway) policy() Policy {
	if g.Policy != nil {
		return g.Policy
	}
	return AlwaysVerify{}
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

	// The accept decision is a swappable policy, evaluated before settlement.
	// The default AlwaysVerify rejects exactly when verification failed, so the
	// reason below matches the original behavior; a custom policy may reject a
	// verified payment, in which case the fallback reason is used.
	pc := PaymentContext{Payload: p, Requirements: reqs, Verification: vr}
	if d := g.policy().Decide(ctx, pc); d.Action != ActionApprove {
		reason := d.Reason
		if reason == "" {
			reason = "payment verification failed"
			if vr != nil && vr.InvalidReason != "" {
				reason = vr.InvalidReason
			}
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
	// Persist to the journal before the response goes out: the settlement is
	// durable by the time the caller learns it succeeded. The journal is the
	// source of truth; the in-memory slice mirrors it for callers that read it.
	if g.Journal != nil {
		if err := g.Journal.Append(g.journalEntry(record)); err != nil {
			return SettlementRecord{}, fmt.Sprintf("journal settlement: %v", err)
		}
	}
	g.mu.Lock()
	g.Settlements = append(g.Settlements, record)
	g.mu.Unlock()
	return record, ""
}

// journalEntry projects a settlement record into its durable journal form.
func (g *Gateway) journalEntry(r SettlementRecord) JournalEntry {
	tx := r.TxHash.Hex()
	return JournalEntry{
		ID:      tx, // settlement tx hash: unique and idempotent
		Payer:   r.Payer.Hex(),
		Amount:  r.Amount.String(),
		TxHash:  tx,
		Network: g.Network,
		At:      r.Time.Unix(),
	}
}

// AttachJournal sets the settlement journal and rebuilds Settlements from it, so
// a gateway restarted after a crash recovers the settlements recorded before it.
func (g *Gateway) AttachJournal(j *Journal) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.Journal = j
	g.Settlements = g.Settlements[:0]
	for _, e := range j.Entries() {
		amount, _ := new(big.Int).SetString(e.Amount, 10)
		g.Settlements = append(g.Settlements, SettlementRecord{
			Payer:  common.HexToAddress(e.Payer),
			Amount: amount,
			TxHash: common.HexToHash(e.TxHash),
			Time:   time.Unix(e.At, 0),
		})
	}
}
