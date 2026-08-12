package x402

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math/big"
	"net/http"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"

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

	// Scorer, if set, fills PaymentContext.RiskScore before the policy runs. The
	// default (nil) leaves the score at 0.
	Scorer func(PaymentContext) float64

	// Journal, if set, durably records each settlement before the gateway
	// answers, so settlements survive a crash and an outbox can publish them.
	// When nil the gateway keeps its original in-memory-only behavior.
	Journal *Journal

	// ConfirmDepth is how many blocks deep a deferred settlement must be before
	// the gateway delivers the resource. It only matters when a policy defers.
	ConfirmDepth uint64

	// Deliverer, if set, hands the purchased asset to the payer after settlement
	// in the two-transaction flow. When nil the gateway settles without delivering.
	Deliverer Deliverer

	// Refunder, if set, returns a settled payment to the payer when delivery
	// fails, so a post-settlement delivery failure is recorded as a refund rather
	// than a silent loss. When nil, a delivery failure is journaled as an
	// outstanding (pending) refund instead.
	Refunder *Refunder

	mu          sync.Mutex
	Settlements []SettlementRecord

	inflightMu sync.Mutex
	inflight   map[[32]byte]*deferredSettlement
}

// deferredSettlement is a settled-but-not-yet-delivered payment, kept until its
// settlement reaches the confirm depth. It is keyed by the payment's nonce.
type deferredSettlement struct {
	record         SettlementRecord
	submittedBlock uint64
}

// SettlementRecord describes a settlement the gateway executed.
type SettlementRecord struct {
	Payer  common.Address
	Amount *big.Int
	TxHash common.Hash
	Time   time.Time
	// DeliveryTx is the asset-delivery transaction hash in the two-transaction
	// flow, zero when no delivery was performed.
	DeliveryTx common.Hash
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

		resp := SettlementResponse{
			Success:     true,
			Transaction: record.TxHash.Hex(),
			Network:     g.Network,
			Payer:       record.Payer.Hex(),
		}
		if record.DeliveryTx != (common.Hash{}) {
			resp.DeliveryTransaction = record.DeliveryTx.Hex()
		}
		settleHeader, err := EncodeHeader(resp)
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
// failure carrying the 402 error code and reason. A payment already in flight
// (deferred) skips verification and is re-evaluated for delivery.
func (g *Gateway) verifyAndSettle(ctx context.Context, header string, reqs PaymentRequirements) (SettlementRecord, *failure) {
	var p PaymentPayload
	if err := DecodeHeader(header, &p); err != nil {
		return SettlementRecord{}, &failure{Code: ErrCodeInvalidHeader, Reason: fmt.Sprintf("invalid X-PAYMENT header: %v", err)}
	}

	// A deferred payment retried with the same authorization is not a replay:
	// its settlement is already on chain, so re-verifying would fail on the used
	// nonce. Resume it instead.
	nonce, _ := payloadNonce(p)
	if df, ok := g.inflightGet(nonce); ok {
		return g.resumeDeferred(ctx, p, reqs, nonce, df)
	}

	vr, err := g.facilitator().Verify(ctx, p, reqs)
	if err != nil {
		return SettlementRecord{}, &failure{Code: ErrCodeVerificationError, Reason: fmt.Sprintf("verification error: %v", err)}
	}

	// The accept decision is a swappable policy, evaluated before settlement.
	pc := PaymentContext{Payload: p, Requirements: reqs, Verification: vr, Stage: StagePreSettlement}
	g.enrich(&pc)
	d := g.policy().Decide(ctx, pc)
	switch d.Action {
	case ActionApprove:
		record, fail := g.settle(ctx, p, reqs, pc)
		if fail != nil {
			return record, fail
		}
		return g.deliver(ctx, record)
	case ActionDefer:
		return g.beginDeferred(ctx, p, reqs, pc, nonce)
	default:
		return SettlementRecord{}, g.refusal(d, pc, vr)
	}
}

// refusal builds the 402 failure for a non-approval decision. It falls back to
// the outcome's default code and the facilitator's reason when the policy left
// either empty.
func (g *Gateway) refusal(d Decision, pc PaymentContext, vr *VerifyResult) *failure {
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
	return f
}

// settle submits the payment, journals it, records it, and notifies settlers of
// the outcome, returning the settlement record or a failure.
func (g *Gateway) settle(ctx context.Context, p PaymentPayload, reqs PaymentRequirements, pc PaymentContext) (SettlementRecord, *failure) {
	settlers := g.settlers()
	notify := func(ok bool) {
		for _, s := range settlers {
			s.Settled(pc, ok)
		}
	}

	sr, err := g.facilitator().Settle(ctx, p, reqs)
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

// beginDeferred settles a deferred payment now but withholds delivery: it
// records the settlement in the in-flight map and answers payment_deferred with
// the settlement transaction, inviting the agent to retry the same payment.
func (g *Gateway) beginDeferred(ctx context.Context, p PaymentPayload, reqs PaymentRequirements, pc PaymentContext, nonce [32]byte) (SettlementRecord, *failure) {
	record, fail := g.settle(ctx, p, reqs, pc)
	if fail != nil {
		return SettlementRecord{}, fail
	}
	g.inflightPut(nonce, &deferredSettlement{record: record, submittedBlock: g.submittedBlock(ctx, record.TxHash)})
	return SettlementRecord{}, &failure{
		Code:   ErrCodePaymentDeferred,
		Reason: fmt.Sprintf("settled in %s; delivered after %d confirmations, retry the same payment", record.TxHash.Hex(), g.ConfirmDepth),
	}
}

// resumeDeferred re-evaluates an in-flight deferred payment at its current stage.
// It delivers when the policy approves and otherwise answers payment_deferred
// again. Delivery removes the entry first, so a payment is never delivered twice.
func (g *Gateway) resumeDeferred(ctx context.Context, p PaymentPayload, reqs PaymentRequirements, nonce [32]byte, df *deferredSettlement) (SettlementRecord, *failure) {
	stage, err := g.stageOf(ctx, df.submittedBlock)
	if err != nil {
		return SettlementRecord{}, &failure{Code: ErrCodeSettlementError, Reason: fmt.Sprintf("settlement stage: %v", err)}
	}
	pc := PaymentContext{
		Payload:      p,
		Requirements: reqs,
		Verification: &VerifyResult{IsValid: true, Payer: df.record.Payer.Hex()},
		Stage:        stage,
	}
	g.enrich(&pc)
	if d := g.policy().Decide(ctx, pc); d.Action != ActionApprove {
		code := d.Code
		if code == "" {
			code = codeForAction(d.Action)
		}
		return SettlementRecord{}, &failure{Code: code, Reason: d.Reason}
	}
	g.inflightRemove(nonce)
	return g.deliver(ctx, df.record)
}

// deliver hands the asset to the payer after settlement, when a Deliverer is
// configured. It sets the delivery transaction on the record. A nil Deliverer
// leaves the record unchanged, preserving the settle-without-delivery behavior.
// A delivery failure is reported as delivery_failed; the settlement already
// happened, so this is where a refund path attaches.
func (g *Gateway) deliver(ctx context.Context, record SettlementRecord) (SettlementRecord, *failure) {
	if g.Deliverer == nil {
		return record, nil
	}
	txHash, err := g.Deliverer.Deliver(ctx, record.Payer)
	if err != nil {
		return g.refund(ctx, record, err)
	}
	record.DeliveryTx = txHash
	return record, nil
}

// refund handles a delivery failure after settlement. With a Refunder it returns
// the payment to the payer and journals the refund; without one it journals an
// outstanding refund. Either way it answers delivery_failed and leaves an
// auditable record, so the payment is never silently lost.
func (g *Gateway) refund(ctx context.Context, record SettlementRecord, deliverErr error) (SettlementRecord, *failure) {
	settled := record.TxHash.Hex()
	if g.Refunder != nil {
		refundTx, rerr := g.Refunder.Refund(ctx, record.Payer, record.Amount)
		if rerr != nil {
			// The refund transfer itself failed: record it as outstanding so the
			// loss is still visible for follow-up.
			g.journalRefundPending(record)
			return SettlementRecord{}, &failure{
				Code:   ErrCodeDeliveryFailed,
				Reason: fmt.Sprintf("settled in %s, delivery failed (%v), refund also failed (%v); refund left pending", settled, deliverErr, rerr),
			}
		}
		g.journalRefund(record, refundTx)
		return SettlementRecord{}, &failure{
			Code:   ErrCodeDeliveryFailed,
			Reason: fmt.Sprintf("settled in %s, delivery failed (%v); refunded in %s", settled, deliverErr, refundTx.Hex()),
		}
	}
	// Keyless gateway: it cannot move funds, so the refund is recorded as pending.
	g.journalRefundPending(record)
	return SettlementRecord{}, &failure{
		Code:   ErrCodeDeliveryFailed,
		Reason: fmt.Sprintf("settled in %s, delivery failed (%v); refund pending (gateway holds no key to refund)", settled, deliverErr),
	}
}

// journalRefund records an executed refund transfer, keyed by the refund tx hash.
func (g *Gateway) journalRefund(record SettlementRecord, refundTx common.Hash) {
	if g.Journal == nil {
		return
	}
	e := JournalEntry{
		ID:      refundTx.Hex(),
		Payer:   record.Payer.Hex(),
		Amount:  record.Amount.String(),
		TxHash:  refundTx.Hex(),
		Network: g.Network,
		At:      time.Now().Unix(),
		Kind:    "refund",
	}
	if err := g.Journal.Append(e); err != nil {
		log.Printf("x402: journal refund: %v", err)
	}
}

// journalRefundPending records an outstanding refund, keyed off the settlement
// tx so it does not collide with the settlement entry.
func (g *Gateway) journalRefundPending(record SettlementRecord) {
	if g.Journal == nil {
		return
	}
	e := JournalEntry{
		ID:      record.TxHash.Hex() + ":refund_pending",
		Payer:   record.Payer.Hex(),
		Amount:  record.Amount.String(),
		TxHash:  record.TxHash.Hex(),
		Network: g.Network,
		At:      time.Now().Unix(),
		Kind:    "refund_pending",
	}
	if err := g.Journal.Append(e); err != nil {
		log.Printf("x402: journal refund_pending: %v", err)
	}
}

// enrich fills the context a policy reads: the delegator's confirmation history
// (from the mandate policy, keyed by the payload's declared delegator) and the
// risk score (from the scorer hook).
func (g *Gateway) enrich(pc *PaymentContext) {
	if mp, ok := g.mandatePolicy(); ok && pc.Payload.Mandate != nil {
		h := mp.DelegatorHistory(common.HexToAddress(pc.Payload.Mandate.Mandate.Delegator))
		pc.History = &h
	}
	if g.Scorer != nil {
		pc.RiskScore = g.Scorer(*pc)
	}
}

// stageOf maps a settlement's depth to a stage. A settlement block above the
// current head means a reorg dropped it, which is reported as an error, matching
// the ledger's handling of a reorg past finality.
func (g *Gateway) stageOf(ctx context.Context, submittedBlock uint64) (Stage, error) {
	head, err := g.currentBlock(ctx)
	if err != nil {
		return StagePreSettlement, err
	}
	if head < submittedBlock {
		return StagePreSettlement, fmt.Errorf("settlement block %d rolled back below head %d", submittedBlock, head)
	}
	if head-submittedBlock >= g.ConfirmDepth {
		return StageConfirmed, nil
	}
	return StageSubmitted, nil
}

// currentBlock reads the chain head height from the backend.
func (g *Gateway) currentBlock(ctx context.Context) (uint64, error) {
	hr, ok := g.Backend.(interface {
		HeaderByNumber(ctx context.Context, number *big.Int) (*types.Header, error)
	})
	if !ok {
		return 0, fmt.Errorf("backend cannot read the chain head")
	}
	h, err := hr.HeaderByNumber(ctx, nil)
	if err != nil {
		return 0, err
	}
	return h.Number.Uint64(), nil
}

// submittedBlock returns the block a settlement transaction landed in, or 0 if
// the receipt is unavailable.
func (g *Gateway) submittedBlock(ctx context.Context, txHash common.Hash) uint64 {
	r, err := g.Backend.TransactionReceipt(ctx, txHash)
	if err != nil || r == nil || r.BlockNumber == nil {
		return 0
	}
	return r.BlockNumber.Uint64()
}

func (g *Gateway) inflightGet(nonce [32]byte) (*deferredSettlement, bool) {
	g.inflightMu.Lock()
	defer g.inflightMu.Unlock()
	df, ok := g.inflight[nonce]
	return df, ok
}

func (g *Gateway) inflightPut(nonce [32]byte, df *deferredSettlement) {
	g.inflightMu.Lock()
	defer g.inflightMu.Unlock()
	if g.inflight == nil {
		g.inflight = make(map[[32]byte]*deferredSettlement)
	}
	g.inflight[nonce] = df
}

func (g *Gateway) inflightRemove(nonce [32]byte) {
	g.inflightMu.Lock()
	defer g.inflightMu.Unlock()
	delete(g.inflight, nonce)
}

// payloadNonce reads the authorization nonce from a payment payload.
func payloadNonce(p PaymentPayload) ([32]byte, bool) {
	var n [32]byte
	b := common.FromHex(p.Payload.Authorization.Nonce)
	if len(b) != 32 {
		return n, false
	}
	copy(n[:], b)
	return n, true
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
