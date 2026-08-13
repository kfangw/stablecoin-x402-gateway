package x402

import (
	"context"
	"math/big"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
)

// MandatePolicy enforces a delegator-signed mandate carried with the payment.
// It stacks after the identity policy in the chain and rejects a payment that
// falls outside the mandate's scope. Checks run cheapest first and stateful
// checks last, extending the gateway's verification-order principle: presence,
// signature, payer-to-agent binding, validity window, allowed payees and
// resources, then the per-payment amount. Cumulative and rate limits build on
// this in a later change; here every violation is a rejection with its own code.
type MandatePolicy struct {
	ChainID *big.Int
	// Now returns the current time; nil defaults to time.Now. Tests inject it.
	Now func() time.Time
	// AskOnExceed turns a limit-type violation (per-payment cap, cumulative
	// budget) into an ask for the delegator's confirmation instead of a
	// rejection. Entitlement violations (signature, revocation, scope) are never
	// asked. A valid confirmation bound to the payment promotes it to approval.
	AskOnExceed bool
	// DeferAbove, when positive, defers settlement delivery for payments at or
	// above this amount: the gateway settles but withholds the resource until the
	// settlement reaches the confirm depth. Zero disables deferral.
	DeferAbove *big.Int
	// accounts holds the cumulative and rate windows. It is nil for a
	// scope-only policy and set by NewMandatePolicy; the gateway uses the
	// constructor so accounting is on whenever mandates are required.
	accounts *mandateAccounts
	// revocations holds mandates the delegator has withdrawn, keyed by
	// (delegator, mandateId). Set by NewMandatePolicy.
	revocations *mandateRevocations
	// history records the confirmation flow per delegator. Set by
	// NewMandatePolicy; read through PaymentContext.History.
	history *ConfirmationHistory
}

// NewMandatePolicy returns a mandate policy with cumulative and rate accounting,
// a revocation set, and confirmation history enabled.
func NewMandatePolicy(chainID *big.Int) MandatePolicy {
	return MandatePolicy{
		ChainID:     chainID,
		accounts:    newMandateAccounts(),
		revocations: newMandateRevocations(),
		history:     newConfirmationHistory(),
	}
}

// DelegatorHistory returns a snapshot of a delegator's confirmation history.
func (p MandatePolicy) DelegatorHistory(delegator common.Address) DelegatorHistory {
	if p.history == nil {
		return DelegatorHistory{}
	}
	return p.history.Snapshot(delegator)
}

// revoke records that the delegator has withdrawn a mandate.
func (p MandatePolicy) revoke(delegator common.Address, mandateID [32]byte) {
	if p.revocations != nil {
		p.revocations.add(delegator, mandateID)
	}
}

// RestoreSpend re-adds a committed spend to a mandate's accounting, so the
// cumulative and frequency windows survive a gateway restart when replayed from
// the journal.
func (p MandatePolicy) RestoreSpend(mandateID [32]byte, at time.Time, amount *big.Int) {
	if p.accounts != nil {
		p.accounts.restore(mandateID, at, amount)
	}
}

// RestoreRevocation re-adds a revocation to the set, rebuilding it from the
// journal on startup.
func (p MandatePolicy) RestoreRevocation(delegator common.Address, mandateID [32]byte) {
	p.revoke(delegator, mandateID)
}

func (p MandatePolicy) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

func (p MandatePolicy) Decide(_ context.Context, pc PaymentContext) Decision {
	if pc.Payload.Mandate == nil {
		return rejectMandate(ErrCodeMandateMissing, "a signed mandate is required")
	}
	m, err := pc.Payload.Mandate.Mandate.ToMandate()
	if err != nil {
		return rejectMandate(ErrCodeMandateInvalid, "malformed mandate")
	}
	sig := common.FromHex(pc.Payload.Mandate.Signature)
	if _, err := VerifyMandate(m, sig, p.ChainID); err != nil {
		return rejectMandate(ErrCodeMandateInvalid, "mandate signature does not verify")
	}

	// The payer (the EIP-3009 signer recovered during verification) must be the
	// agent the mandate was granted to.
	if pc.Verification == nil || pc.Verification.Payer == "" {
		return rejectMandate(ErrCodeMandateInvalid, "cannot bind the payer to the mandate")
	}
	if common.HexToAddress(pc.Verification.Payer) != m.Agent {
		return rejectMandate(ErrCodeMandateInvalid, "payer is not the mandated agent")
	}

	now := big.NewInt(p.now().Unix())
	if m.ValidAfter != nil && now.Cmp(m.ValidAfter) < 0 {
		return rejectMandate(ErrCodeMandateExpired, "mandate is not yet valid")
	}
	if m.ValidBefore != nil && now.Cmp(m.ValidBefore) >= 0 {
		return rejectMandate(ErrCodeMandateExpired, "mandate has expired")
	}

	if p.revocations != nil && p.revocations.has(m.Delegator, m.MandateID) {
		return rejectMandate(ErrCodeMandateRevoked, "mandate has been revoked")
	}

	// An empty allowlist places no constraint on that dimension; a non-empty one
	// admits only its members (prefix match for resources).
	payTo := common.HexToAddress(pc.Requirements.PayTo)
	if len(m.AllowedPayees) > 0 && !containsAddress(m.AllowedPayees, payTo) {
		return rejectMandate(ErrCodeMandateExceeded, "payee is not allowed by the mandate")
	}
	if len(m.AllowedResources) > 0 && !prefixAllowed(m.AllowedResources, pc.Requirements.Resource) {
		return rejectMandate(ErrCodeMandateExceeded, "resource is not allowed by the mandate")
	}

	amount, ok := paymentAmount(pc)
	if !ok {
		return rejectMandate(ErrCodeMandateInvalid, "payment amount is unreadable")
	}

	// A confirmation, bound to this exact payment and signed by the delegator,
	// lets a limit-type violation through. It never rescues an entitlement
	// violation; all of those were checked above.
	confirmed := p.confirmationApproves(pc, m, amount)
	attached := pc.Payload.Confirmation != nil

	if m.MaxAmountPerPayment != nil && m.MaxAmountPerPayment.Sign() > 0 && amount.Cmp(m.MaxAmountPerPayment) > 0 {
		if !confirmed {
			return p.overLimit(m.Delegator, attached, ErrCodeMandateExceeded, "amount exceeds the per-payment limit")
		}
		p.recordConfirmation(m.Delegator)
	}

	// Cumulative and rate limits are stateful, so they run last. The reservation
	// counts this payment against the windows immediately; the gateway confirms
	// it on settlement success and drops it otherwise, so a rejected or failed
	// payment never spends the budget. A confirmation waives the budget check
	// (but never the rate cap) while still recording the spend. On a deferred
	// payment's later re-evaluation the settlement already happened, so the
	// accounting was done on the first pass and must not run again.
	if p.accounts != nil && pc.Stage == StagePreSettlement {
		nonce, ok := paymentNonce(pc)
		if !ok {
			return rejectMandate(ErrCodeMandateInvalid, "payment nonce is unreadable")
		}
		switch p.accounts.reserve(m, nonce, amount, p.now(), confirmed) {
		case ErrCodeMandateBudget:
			return p.overLimit(m.Delegator, attached, ErrCodeMandateBudget, "payment exceeds the mandate's cumulative budget")
		case ErrCodeMandateRate:
			return rejectMandate(ErrCodeMandateRate, "payment exceeds the mandate's rate limit")
		}
	}

	// A large payment is delivered only once its settlement is deep enough. The
	// gateway settles it now and re-evaluates as the stage advances; here the
	// policy holds delivery until the confirmed stage.
	if p.DeferAbove != nil && p.DeferAbove.Sign() > 0 && amount.Cmp(p.DeferAbove) >= 0 && pc.Stage < StageConfirmed {
		return Decision{Action: ActionDefer, Code: ErrCodePaymentDeferred, Reason: "large payment delivered after settlement confirmations"}
	}

	return Decision{Action: ActionApprove}
}

// overLimit turns a limit violation into an ask when AskOnExceed is set, and a
// rejection otherwise. Under the ask flow it also records the delegator's
// history: an attached-but-invalid confirmation is a failure, none is a fresh
// ask.
func (p MandatePolicy) overLimit(delegator common.Address, attached bool, code, reason string) Decision {
	if p.AskOnExceed {
		if p.history != nil {
			if attached {
				p.history.recordFailure(delegator)
			} else {
				p.history.recordAsk(delegator, p.now())
			}
		}
		return Decision{Action: ActionAsk, Code: ErrCodeConfirmationRequired, Reason: reason + "; confirm with the delegator"}
	}
	return rejectMandate(code, reason)
}

// recordConfirmation notes that a delegator's confirmation cleared an overage.
func (p MandatePolicy) recordConfirmation(delegator common.Address) {
	if p.history != nil {
		p.history.recordConfirmation(delegator)
	}
}

// confirmationApproves reports whether the payment carries a delegator-signed
// confirmation bound to this exact payment (mandate, nonce, amount, resource)
// and still valid.
func (p MandatePolicy) confirmationApproves(pc PaymentContext, m Mandate, amount *big.Int) bool {
	cj := pc.Payload.Confirmation
	if cj == nil {
		return false
	}
	c, err := cj.ToConfirmation()
	if err != nil {
		return false
	}
	signer, err := VerifyConfirmation(c, common.FromHex(cj.Signature), p.ChainID)
	if err != nil || signer != m.Delegator {
		return false
	}
	if c.MandateID != m.MandateID {
		return false
	}
	nonce, ok := paymentNonce(pc)
	if !ok || c.AuthorizationNonce != nonce {
		return false
	}
	if c.Amount == nil || amount == nil || c.Amount.Cmp(amount) != 0 {
		return false
	}
	if c.Resource != pc.Requirements.Resource {
		return false
	}
	if c.ValidBefore == nil || p.now().Unix() >= c.ValidBefore.Int64() {
		return false
	}
	return true
}

// Settled finalizes the reservation made in Decide: a successful settlement
// commits the spend to the window, a failed one releases it. This makes
// MandatePolicy a PaymentSettler that the gateway notifies after settlement.
func (p MandatePolicy) Settled(pc PaymentContext, success bool) {
	if p.accounts == nil || pc.Payload.Mandate == nil {
		return
	}
	m, err := pc.Payload.Mandate.Mandate.ToMandate()
	if err != nil {
		return
	}
	nonce, ok := paymentNonce(pc)
	if !ok {
		return
	}
	if success {
		p.accounts.commit(m, nonce, p.now())
	} else {
		p.accounts.release(m, nonce)
	}
}

func rejectMandate(code, reason string) Decision {
	return Decision{Action: ActionReject, Code: code, Reason: reason}
}

func containsAddress(set []common.Address, a common.Address) bool {
	for _, x := range set {
		if x == a {
			return true
		}
	}
	return false
}

func prefixAllowed(prefixes []string, resource string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(resource, p) {
			return true
		}
	}
	return false
}

// paymentAmount reads the transfer value from the (already verified) payment.
func paymentAmount(pc PaymentContext) (*big.Int, bool) {
	return new(big.Int).SetString(pc.Payload.Payload.Authorization.Value, 10)
}

// paymentNonce reads the unique EIP-3009 nonce, which identifies a payment
// across the reserve and commit steps.
func paymentNonce(pc PaymentContext) ([32]byte, bool) {
	var n [32]byte
	b := common.FromHex(pc.Payload.Payload.Authorization.Nonce)
	if len(b) != 32 {
		return n, false
	}
	copy(n[:], b)
	return n, true
}

// allTime stands in for a missing window: the limit then applies cumulatively
// over the whole run rather than a bounded window.
const allTime = time.Duration(1) << 62

func windowDur(seconds *big.Int) time.Duration {
	if seconds == nil || seconds.Sign() <= 0 {
		return allTime
	}
	return time.Duration(seconds.Int64()) * time.Second
}

// mandateRevocations is the set of mandates a delegator has withdrawn, keyed by
// (delegator, mandateId) so only the mandate's own delegator can revoke it.
type mandateRevocations struct {
	mu  sync.Mutex
	set map[[52]byte]bool
}

func newMandateRevocations() *mandateRevocations {
	return &mandateRevocations{set: make(map[[52]byte]bool)}
}

func revocationKey(delegator common.Address, mandateID [32]byte) [52]byte {
	var k [52]byte
	copy(k[:20], delegator.Bytes())
	copy(k[20:], mandateID[:])
	return k
}

func (r *mandateRevocations) add(delegator common.Address, mandateID [32]byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.set[revocationKey(delegator, mandateID)] = true
}

func (r *mandateRevocations) has(delegator common.Address, mandateID [32]byte) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.set[revocationKey(delegator, mandateID)]
}

// mandateAccounts tracks per-mandate cumulative spend and payment frequency in
// sliding windows, plus the reservations of in-flight payments. It is safe for
// concurrent use.
type mandateAccounts struct {
	mu   sync.Mutex
	acct map[[32]byte]*mandateAccount
}

func newMandateAccounts() *mandateAccounts {
	return &mandateAccounts{acct: make(map[[32]byte]*mandateAccount)}
}

type mandateAccount struct {
	spends   []mandateSpend            // committed payments
	reserved map[[32]byte]mandateSpend // in-flight, keyed by payment nonce
}

type mandateSpend struct {
	at     time.Time
	amount *big.Int
}

// restore re-adds a committed spend to an account, used to rebuild accounting
// from the journal on startup. The window is applied later by reserve, which
// carries the mandate, so restore needs only the timestamp and amount.
func (a *mandateAccounts) restore(mandateID [32]byte, at time.Time, amount *big.Int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	acc := a.acct[mandateID]
	if acc == nil {
		acc = &mandateAccount{reserved: make(map[[32]byte]mandateSpend)}
		a.acct[mandateID] = acc
	}
	acc.spends = append(acc.spends, mandateSpend{at: at, amount: new(big.Int).Set(amount)})
}

// reserve counts a payment against the windows, including other reservations,
// and records it if it fits. It returns an empty string on success or the error
// code of the limit it would breach.
func (a *mandateAccounts) reserve(m Mandate, nonce [32]byte, amount *big.Int, now time.Time, skipBudget bool) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	acc := a.acct[m.MandateID]
	if acc == nil {
		acc = &mandateAccount{reserved: make(map[[32]byte]mandateSpend)}
		a.acct[m.MandateID] = acc
	}
	if !skipBudget && m.BudgetAmount != nil && m.BudgetAmount.Sign() > 0 {
		used := acc.committedAmount(now, windowDur(m.BudgetWindowSeconds))
		used.Add(used, acc.reservedAmount())
		used.Add(used, amount)
		if used.Cmp(m.BudgetAmount) > 0 {
			return ErrCodeMandateBudget
		}
	}
	if m.MaxPaymentsPerWindow != nil && m.MaxPaymentsPerWindow.Sign() > 0 {
		count := int64(acc.committedCount(now, windowDur(m.RateWindowSeconds)) + len(acc.reserved) + 1)
		if count > m.MaxPaymentsPerWindow.Int64() {
			return ErrCodeMandateRate
		}
	}
	acc.reserved[nonce] = mandateSpend{at: now, amount: new(big.Int).Set(amount)}
	return ""
}

// commit turns a reservation into a committed spend.
func (a *mandateAccounts) commit(m Mandate, nonce [32]byte, now time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	acc := a.acct[m.MandateID]
	if acc == nil {
		return
	}
	r, ok := acc.reserved[nonce]
	if !ok {
		return
	}
	delete(acc.reserved, nonce)
	acc.spends = append(acc.spends, mandateSpend{at: now, amount: r.amount})
	acc.prune(now, maxWindow(m))
}

// release drops a reservation without committing it.
func (a *mandateAccounts) release(m Mandate, nonce [32]byte) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if acc := a.acct[m.MandateID]; acc != nil {
		delete(acc.reserved, nonce)
	}
}

func maxWindow(m Mandate) time.Duration {
	b, r := windowDur(m.BudgetWindowSeconds), windowDur(m.RateWindowSeconds)
	if b > r {
		return b
	}
	return r
}

func (acc *mandateAccount) committedAmount(now time.Time, window time.Duration) *big.Int {
	cutoff := now.Add(-window)
	sum := new(big.Int)
	for _, s := range acc.spends {
		if !s.at.Before(cutoff) {
			sum.Add(sum, s.amount)
		}
	}
	return sum
}

func (acc *mandateAccount) committedCount(now time.Time, window time.Duration) int {
	cutoff := now.Add(-window)
	n := 0
	for _, s := range acc.spends {
		if !s.at.Before(cutoff) {
			n++
		}
	}
	return n
}

func (acc *mandateAccount) reservedAmount() *big.Int {
	sum := new(big.Int)
	for _, r := range acc.reserved {
		sum.Add(sum, r.amount)
	}
	return sum
}

// prune drops committed spends older than the widest window, bounding memory.
func (acc *mandateAccount) prune(now time.Time, window time.Duration) {
	cutoff := now.Add(-window)
	kept := acc.spends[:0]
	for _, s := range acc.spends {
		if !s.at.Before(cutoff) {
			kept = append(kept, s)
		}
	}
	acc.spends = kept
}
