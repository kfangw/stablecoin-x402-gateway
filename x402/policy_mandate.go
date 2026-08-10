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
	// accounts holds the cumulative and rate windows. It is nil for a
	// scope-only policy and set by NewMandatePolicy; the gateway uses the
	// constructor so accounting is on whenever mandates are required.
	accounts *mandateAccounts
}

// NewMandatePolicy returns a mandate policy with cumulative and rate accounting
// enabled.
func NewMandatePolicy(chainID *big.Int) MandatePolicy {
	return MandatePolicy{ChainID: chainID, accounts: newMandateAccounts()}
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
	if m.MaxAmountPerPayment != nil && m.MaxAmountPerPayment.Sign() > 0 && amount.Cmp(m.MaxAmountPerPayment) > 0 {
		return rejectMandate(ErrCodeMandateExceeded, "amount exceeds the per-payment limit")
	}

	// Cumulative and rate limits are stateful, so they run last. The reservation
	// counts this payment against the windows immediately; the gateway confirms
	// it on settlement success and drops it otherwise, so a rejected or failed
	// payment never spends the budget.
	if p.accounts != nil {
		nonce, ok := paymentNonce(pc)
		if !ok {
			return rejectMandate(ErrCodeMandateInvalid, "payment nonce is unreadable")
		}
		switch p.accounts.reserve(m, nonce, amount, p.now()) {
		case ErrCodeMandateBudget:
			return rejectMandate(ErrCodeMandateBudget, "payment exceeds the mandate's cumulative budget")
		case ErrCodeMandateRate:
			return rejectMandate(ErrCodeMandateRate, "payment exceeds the mandate's rate limit")
		}
	}

	return Decision{Action: ActionApprove}
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

// reserve counts a payment against the windows, including other reservations,
// and records it if it fits. It returns an empty string on success or the error
// code of the limit it would breach.
func (a *mandateAccounts) reserve(m Mandate, nonce [32]byte, amount *big.Int, now time.Time) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	acc := a.acct[m.MandateID]
	if acc == nil {
		acc = &mandateAccount{reserved: make(map[[32]byte]mandateSpend)}
		a.acct[m.MandateID] = acc
	}
	if m.BudgetAmount != nil && m.BudgetAmount.Sign() > 0 {
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
