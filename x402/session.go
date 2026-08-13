package x402

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
)

// sessionMargin is how close to the authorization's expiry the gateway settles a
// session, so it always settles while the authorization is still valid.
const sessionMargin = 60 * time.Second

// paymentSession is a settled-at-close payment: one authorization for the whole
// budget, drawn down by many requests, settled when the budget runs out, the
// authorization is about to expire, or the agent closes it. Unused budget is
// refunded on settlement.
type paymentSession struct {
	mu          sync.Mutex
	payload     PaymentPayload
	reqs        PaymentRequirements
	pc          PaymentContext
	payer       common.Address
	budget      *big.Int
	spent       *big.Int
	validBefore int64
	settled     bool
}

type sessionStore struct {
	mu       sync.Mutex
	sessions map[string]*paymentSession
}

func newSessionStore() *sessionStore {
	return &sessionStore{sessions: make(map[string]*paymentSession)}
}

func (s *sessionStore) put(id string, sess *paymentSession) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[id] = sess
}

func (s *sessionStore) get(id string) (*paymentSession, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[id]
	return sess, ok
}

func (s *sessionStore) remove(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, id)
}

// sessions returns the gateway's session store, building it on first use.
func (g *Gateway) sessions() *sessionStore {
	g.sessionOnce.Do(func() { g.sessionStore = newSessionStore() })
	return g.sessionStore
}

func newSessionID() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// openSession verifies a full-budget authorization, runs the accept policy with
// the whole budget as the amount, then holds the authorization unsettled and
// serves the first request against it. It answers with the session id header.
func (g *Gateway) openSession(w http.ResponseWriter, r *http.Request, next http.Handler, resource string, p PaymentPayload) {
	if g.Refunder == nil {
		g.writeRequirements(w, resource, &failure{Code: ErrCodePaymentRequired, Reason: "sessions need a refund path; the gateway is not configured for them"})
		return
	}
	ctx := r.Context()
	reqs := g.Requirements(resource)

	// The session-open authorization is bound to this resource, like any payment.
	if fail := g.checkResourceBinding(p, reqs); fail != nil {
		g.writeRequirements(w, resource, fail)
		return
	}

	vr, err := g.facilitator().Verify(ctx, p, reqs)
	if err != nil {
		g.writeRequirements(w, resource, &failure{Code: ErrCodeVerificationError, Reason: fmt.Sprintf("verification error: %v", err)})
		return
	}
	if vr == nil || !vr.IsValid {
		reason := "payment verification failed"
		if vr != nil && vr.InvalidReason != "" {
			reason = vr.InvalidReason
		}
		g.writeRequirements(w, resource, &failure{Code: ErrCodeVerificationFailed, Reason: reason})
		return
	}
	auth, _, err := parseExactPayload(p.Payload)
	if err != nil {
		g.writeRequirements(w, resource, &failure{Code: ErrCodeInvalidHeader, Reason: err.Error()})
		return
	}

	// The policy sees the full budget as the payment amount, so a mandate's
	// per-payment and cumulative checks bind against the whole session.
	pc := PaymentContext{Payload: p, Requirements: reqs, Verification: vr, Stage: StagePreSettlement}
	g.enrich(&pc)
	if d := g.policy().Decide(ctx, pc); d.Action != ActionApprove {
		g.writeRequirements(w, resource, g.refusal(d, pc, vr))
		return
	}

	id, err := newSessionID()
	if err != nil {
		g.writeRequirements(w, resource, &failure{Code: ErrCodeVerificationError, Reason: "could not open a session"})
		return
	}
	sess := &paymentSession{
		payload:     p,
		reqs:        reqs,
		pc:          pc,
		payer:       auth.From,
		budget:      new(big.Int).Set(auth.Value),
		spent:       new(big.Int).Set(g.Price),
		validBefore: auth.ValidBefore.Int64(),
	}
	g.sessions().put(id, sess)
	g.Metrics.IncSessionOpened()
	g.logInfo("session opened", "id", id, "payer", auth.From.Hex(), "budget", sess.budget.String())
	w.Header().Set(HeaderPaymentSession, id)
	next.ServeHTTP(w, r)
}

// handleSession serves a continuation request that carries only a session id, or
// closes the session when asked. It draws the price from the remaining budget,
// settling and closing the session when the budget is spent, the authorization
// is about to expire, or the agent closes it.
func (g *Gateway) handleSession(w http.ResponseWriter, r *http.Request, next http.Handler, resource, id string) {
	sess, ok := g.sessions().get(id)
	if !ok {
		g.writeRequirements(w, resource, &failure{Code: ErrCodeSessionUnknown, Reason: "unknown or already-settled session"})
		return
	}
	sess.mu.Lock()
	defer sess.mu.Unlock()
	if sess.settled {
		g.writeRequirements(w, resource, &failure{Code: ErrCodeSessionUnknown, Reason: "session already settled"})
		return
	}
	ctx := r.Context()

	if r.Header.Get(HeaderPaymentSessionClose) == "1" {
		record, err := g.settleSession(ctx, sess)
		g.sessions().remove(id)
		if err != nil {
			g.writeRequirements(w, resource, &failure{Code: ErrCodeSettlementError, Reason: fmt.Sprintf("session settlement failed: %v", err)})
			return
		}
		g.writeSessionClosed(w, record)
		return
	}

	now := time.Now().Unix()
	expiringSoon := sess.validBefore != 0 && now >= sess.validBefore-int64(sessionMargin.Seconds())
	remaining := new(big.Int).Sub(sess.budget, sess.spent)
	if expiringSoon || remaining.Cmp(g.Price) < 0 {
		if _, err := g.settleSession(ctx, sess); err != nil {
			g.writeRequirements(w, resource, &failure{Code: ErrCodeSettlementError, Reason: fmt.Sprintf("session settlement failed: %v", err)})
			return
		}
		g.sessions().remove(id)
		reason := "session budget spent; the session settled and closed"
		if expiringSoon {
			reason = "session authorization is expiring; the session settled and closed"
		}
		g.writeRequirements(w, resource, &failure{Code: ErrCodeSessionExhausted, Reason: reason})
		return
	}

	sess.spent.Add(sess.spent, g.Price)
	w.Header().Set(HeaderPaymentSession, id)
	next.ServeHTTP(w, r)
}

// settleSession settles the held authorization for the whole budget and refunds
// the unused remainder to the payer. The caller holds sess.mu.
func (g *Gateway) settleSession(ctx context.Context, sess *paymentSession) (SettlementRecord, error) {
	if sess.settled {
		return SettlementRecord{}, nil
	}
	sr, err := g.facilitator().Settle(ctx, sess.payload, sess.reqs)
	if err != nil {
		return SettlementRecord{}, err
	}
	if sr == nil || !sr.Success {
		reason := "settlement failed"
		if sr != nil && sr.ErrorReason != "" {
			reason = sr.ErrorReason
		}
		return SettlementRecord{}, fmt.Errorf("%s", reason)
	}
	record := SettlementRecord{
		Payer:  sess.payer,
		Amount: new(big.Int).Set(sess.budget),
		TxHash: common.HexToHash(sr.Transaction),
		Time:   time.Now(),
	}
	if g.Journal != nil {
		if err := g.Journal.Append(g.journalEntry(record)); err != nil {
			return SettlementRecord{}, fmt.Errorf("journal session settlement: %w", err)
		}
	}
	g.mu.Lock()
	g.Settlements = append(g.Settlements, record)
	g.mu.Unlock()
	for _, s := range g.settlers() {
		s.Settled(sess.pc, true)
	}

	// Refund the unused budget: the seller keeps only what was spent.
	unused := new(big.Int).Sub(sess.budget, sess.spent)
	if unused.Sign() > 0 {
		refundTx, rerr := g.Refunder.Refund(ctx, sess.payer, unused)
		if rerr != nil {
			g.journalRefundPending(record)
		} else {
			g.journalRefund(SettlementRecord{Payer: sess.payer, Amount: unused, TxHash: record.TxHash}, refundTx)
		}
	}
	sess.settled = true
	g.Metrics.IncSessionSettled()
	g.logInfo("session settled", "payer", sess.payer.Hex(), "budget", sess.budget.String(), "spent", sess.spent.String(), "tx", record.TxHash.Hex())
	return record, nil
}

// writeSessionClosed answers a close request with 200 and the settlement in the
// X-PAYMENT-RESPONSE header.
func (g *Gateway) writeSessionClosed(w http.ResponseWriter, record SettlementRecord) {
	resp := SettlementResponse{
		Success:     true,
		Transaction: record.TxHash.Hex(),
		Network:     g.Network,
		Payer:       record.Payer.Hex(),
	}
	if h, err := EncodeHeader(resp); err == nil {
		w.Header().Set(HeaderPaymentResponse, h)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	body, _ := json.Marshal(map[string]string{"session": "closed", "settlement": record.TxHash.Hex()})
	_, _ = w.Write(body)
}
