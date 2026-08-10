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
			g.writeRequirements(w, resource, &failure{Code: ErrCodePaymentRequired, Reason: "X-PAYMENT header is required"})
			return
		}

		record, fail := g.verifyAndSettle(r.Context(), header, g.Requirements(resource))
		if fail != nil {
			g.writeRequirements(w, resource, fail)
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

func (g *Gateway) writeRequirements(w http.ResponseWriter, resource string, fail *failure) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusPaymentRequired)
	body, _ := json.Marshal(RequirementsResponse{
		X402Version: Version,
		Error:       fail.Reason,
		ErrorCode:   fail.Code,
		Accepts:     []PaymentRequirements{g.Requirements(resource)},
		Ask:         fail.Ask,
	})
	_, _ = w.Write(body)
}

// failure is a non-approval outcome: the machine-readable code served as
// errorCode and the human-readable reason served as error. Ask is set only on a
// confirmation_required outcome.
type failure struct {
	Code   string
	Reason string
	Ask    *AskRequest
}

// verifyAndSettle runs the payment through the facilitator (verify, then
// settle) and records the outcome. It returns a nil failure on success, or a
// failure carrying the 402 error code and reason.
func (g *Gateway) verifyAndSettle(ctx context.Context, header string, reqs PaymentRequirements) (SettlementRecord, *failure) {
	var p PaymentPayload
	if err := DecodeHeader(header, &p); err != nil {
		return SettlementRecord{}, &failure{Code: ErrCodeInvalidHeader, Reason: fmt.Sprintf("invalid X-PAYMENT header: %v", err)}
	}

	fac := g.facilitator()

	vr, err := fac.Verify(ctx, p, reqs)
	if err != nil {
		return SettlementRecord{}, &failure{Code: ErrCodeVerificationError, Reason: fmt.Sprintf("verification error: %v", err)}
	}

	// The accept decision is a swappable policy, evaluated before settlement.
	// A non-approval carries the policy's own code and reason, falling back to
	// the outcome's default code and the facilitator's reason when either is
	// empty. The default AlwaysVerify sets verification_failed, matching the
	// original behavior.
	pc := PaymentContext{Payload: p, Requirements: reqs, Verification: vr}
	if d := g.policy().Decide(ctx, pc); d.Action != ActionApprove {
		code := d.Code
		if code == "" {
			code = codeForAction(d.Action)
		}
		reason := d.Reason
		if reason == "" {
			reason = "payment verification failed"
			if vr != nil && vr.InvalidReason != "" {
				reason = vr.InvalidReason
			}
		}
		f := &failure{Code: code, Reason: reason}
		if d.Action == ActionAsk {
			f.Ask = askFromContext(pc)
		}
		return SettlementRecord{}, f
	}

	// The policy approved, so any reservation it made must be finalized: notify
	// settlers with the outcome of every path from here on.
	settlers := g.settlers()
	notify := func(ok bool) {
		for _, s := range settlers {
			s.Settled(pc, ok)
		}
	}

	sr, err := fac.Settle(ctx, p, reqs)
	if err != nil {
		notify(false)
		return SettlementRecord{}, &failure{Code: ErrCodeSettlementError, Reason: fmt.Sprintf("settlement error: %v", err)}
	}
	if sr == nil || !sr.Success {
		notify(false)
		reason := "settlement failed"
		if sr != nil && sr.ErrorReason != "" {
			reason = sr.ErrorReason
		}
		return SettlementRecord{}, &failure{Code: ErrCodeSettlementFailed, Reason: reason}
	}

	// Reconstruct the record from the (already validated) payload and receipt.
	auth, _, err := parseExactPayload(p.Payload)
	if err != nil {
		notify(false)
		return SettlementRecord{}, &failure{Code: ErrCodeSettlementError, Reason: fmt.Sprintf("record settlement: %v", err)}
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
			notify(false)
			return SettlementRecord{}, &failure{Code: ErrCodeSettlementError, Reason: fmt.Sprintf("journal settlement: %v", err)}
		}
	}
	notify(true)
	g.mu.Lock()
	g.Settlements = append(g.Settlements, record)
	g.mu.Unlock()
	return record, nil
}

// askFromContext names the payment a confirmation must be signed for.
func askFromContext(pc PaymentContext) *AskRequest {
	mandateID := ""
	if pc.Payload.Mandate != nil {
		mandateID = pc.Payload.Mandate.Mandate.MandateID
	}
	return &AskRequest{
		MandateID:          mandateID,
		AuthorizationNonce: pc.Payload.Payload.Authorization.Nonce,
		Amount:             pc.Payload.Payload.Authorization.Value,
		Resource:           pc.Requirements.Resource,
	}
}

// RevokeMandate verifies a delegator's signed revocation and records it, so the
// next payment under that mandate is rejected. It fails if the gateway is not
// enforcing mandates or the signature does not verify.
func (g *Gateway) RevokeMandate(rev RevocationJSON) error {
	mp, ok := g.mandatePolicy()
	if !ok {
		return fmt.Errorf("x402: gateway is not enforcing mandates")
	}
	mandateID, err := parseBytes32(rev.MandateID)
	if err != nil {
		return fmt.Errorf("x402: revocation mandate id: %w", err)
	}
	delegator, err := VerifyRevocation(mandateID, common.FromHex(rev.Signature), mp.ChainID)
	if err != nil {
		return err
	}
	mp.revoke(delegator, mandateID)
	return nil
}

// mandatePolicy returns the mandate policy in the active chain, if any.
func (g *Gateway) mandatePolicy() (MandatePolicy, bool) {
	switch p := g.policy().(type) {
	case Chain:
		for _, e := range p {
			if mp, ok := e.(MandatePolicy); ok {
				return mp, true
			}
		}
	case MandatePolicy:
		return p, true
	}
	return MandatePolicy{}, false
}

// settlers returns the policies in the active chain that observe settlement.
func (g *Gateway) settlers() []PaymentSettler {
	var out []PaymentSettler
	switch p := g.policy().(type) {
	case Chain:
		for _, e := range p {
			if s, ok := e.(PaymentSettler); ok {
				out = append(out, s)
			}
		}
	default:
		if s, ok := p.(PaymentSettler); ok {
			out = append(out, s)
		}
	}
	return out
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
