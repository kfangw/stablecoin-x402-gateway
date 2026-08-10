package x402

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"time"

	"github.com/ethereum/go-ethereum/common"

	"github.com/kfangw/stablecoin-x402-gateway/wallet"
)

func hexAddress(s string) common.Address { return common.HexToAddress(s) }

// Agent is a client that performs x402 payments.
// Without human involvement it reads a 402 response, checks the payment terms,
// signs an EIP-3009 authorization, and retries the request. It is a minimal
// form of the payment path of an autonomous agent.
type Agent struct {
	Wallet *wallet.Wallet
	HTTP   *http.Client

	// DomainSeparator is the EIP-712 domain separator of the payment token,
	// read from the contract.
	DomainSeparator [32]byte

	// MaxAmount is the spending limit delegated to the agent.
	// The agent refuses payment terms that exceed it. When Grant is nil this
	// value backs the default MaxAmountGrant policy.
	MaxAmount *big.Int

	// Grant decides whether to pay a set of terms. If nil, a MaxAmountGrant built
	// from MaxAmount is used, so the agent's original behavior is unchanged.
	Grant GrantPolicy

	// Mandate, if set, is a delegator-signed grant attached to each payment so a
	// gateway that requires mandates can check the agent's spending authority.
	Mandate *SignedMandateJSON

	// Confirmation, if set, is a delegator's approval of one over-limit payment.
	// The agent reuses the confirmation's authorization nonce so the resigned
	// payment matches what the delegator confirmed.
	Confirmation *ConfirmationJSON

	lastPaymentHeader string
	lastAmount        *big.Int
	lastResource      string
}

// LastPaymentHeader returns the X-PAYMENT header value of the most recent
// payment. Tests and the demo use it to verify replay rejection.
func (a *Agent) LastPaymentHeader() string { return a.lastPaymentHeader }

// Result is the outcome of a request that may have involved a payment.
type Result struct {
	StatusCode  int
	Body        []byte
	Paid        bool
	AmountPaid  *big.Int
	Settlement  *SettlementResponse
	Requirement *PaymentRequirements
	// ErrorCode is the machine-readable code from a 402 that refused the paid
	// request, empty otherwise. RegistrationHint reports whether that refusal
	// was an unregistered-agent rejection, which a caller can act on by
	// registering the agent and retrying.
	ErrorCode string
	// Ask is set when ErrorCode is confirmation_required: it names the payment
	// the delegator must confirm before a retry can settle.
	Ask *AskRequest
}

// RegistrationHint reports whether the paid request was refused because the
// payer is not a registered agent. When true, registering the agent and
// retrying is the way forward.
func (r *Result) RegistrationHint() bool {
	return r.ErrorCode == ErrCodeIdentityUnregistered
}

func (a *Agent) httpClient() *http.Client {
	if a.HTTP != nil {
		return a.HTTP
	}
	return http.DefaultClient
}

// grant returns the configured grant policy, defaulting to a MaxAmountGrant
// built from MaxAmount so the agent's original behavior is preserved.
func (a *Agent) grant() GrantPolicy {
	if a.Grant != nil {
		return a.Grant
	}
	return MaxAmountGrant{Max: a.MaxAmount}
}

// Get requests a resource and, on a 402 response, pays and retries once.
func (a *Agent) Get(url string) (*Result, error) {
	resp, err := a.httpClient().Get(url)
	if err != nil {
		return nil, fmt.Errorf("x402 agent: request: %w", err)
	}
	body, err := readBody(resp)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusPaymentRequired {
		return &Result{StatusCode: resp.StatusCode, Body: body}, nil
	}

	// 402: read the payment terms.
	var reqs RequirementsResponse
	if err := json.Unmarshal(body, &reqs); err != nil {
		return nil, fmt.Errorf("x402 agent: parse requirements: %w", err)
	}
	if len(reqs.Accepts) == 0 {
		return nil, fmt.Errorf("x402 agent: no accepted payment methods")
	}
	req := reqs.Accepts[0]
	if req.Scheme != SchemeExact {
		return nil, fmt.Errorf("x402 agent: unsupported scheme %q", req.Scheme)
	}

	amount, ok := new(big.Int).SetString(req.MaxAmountRequired, 10)
	if !ok {
		return nil, fmt.Errorf("x402 agent: invalid amount %q", req.MaxAmountRequired)
	}
	// The grant policy decides whether to pay these terms. A refusal stops the
	// agent; pay and ask both proceed to sign (an ask expects the agent to carry
	// a confirmation, which it attaches if it has one).
	if gd := a.grant().Decide(PaymentTermsContext{Requirements: req, Amount: amount}); gd.Action == GrantRefuse {
		return nil, fmt.Errorf("x402 agent: %s", gd.Reason)
	}

	header, err := a.buildPayment(req, amount)
	if err != nil {
		return nil, err
	}
	a.lastPaymentHeader = header
	a.lastAmount = amount
	a.lastResource = url
	return a.sendPayment(url, header, amount, &req)
}

// Retry resends the most recent payment unchanged. It is how an agent follows up
// a deferred payment: the same authorization, so the gateway recognizes the
// in-flight settlement instead of treating it as a replay.
func (a *Agent) Retry(url string) (*Result, error) {
	if a.lastPaymentHeader == "" {
		return nil, fmt.Errorf("x402 agent: no prior payment to retry")
	}
	return a.sendPayment(url, a.lastPaymentHeader, a.lastAmount, nil)
}

// sendPayment sends one request carrying the payment header and builds the
// Result from the response.
func (a *Agent) sendPayment(url, header string, amount *big.Int, req *PaymentRequirements) (*Result, error) {
	paid, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	paid.Header.Set(HeaderPayment, header)
	resp, err := a.httpClient().Do(paid)
	if err != nil {
		return nil, fmt.Errorf("x402 agent: paid request: %w", err)
	}
	body, err := readBody(resp)
	if err != nil {
		return nil, err
	}

	result := &Result{
		StatusCode:  resp.StatusCode,
		Body:        body,
		Paid:        resp.StatusCode < 400,
		AmountPaid:  amount,
		Requirement: req,
	}
	if h := resp.Header.Get(HeaderPaymentResponse); h != "" {
		var s SettlementResponse
		if err := DecodeHeader(h, &s); err == nil {
			result.Settlement = &s
		}
	}
	// A 402 on the paid request carries a machine-readable code explaining the
	// refusal (for example an unregistered agent, or a confirmation request);
	// surface it on the result.
	if resp.StatusCode == http.StatusPaymentRequired {
		var refused RequirementsResponse
		if err := json.Unmarshal(body, &refused); err == nil {
			result.ErrorCode = refused.ErrorCode
			result.Ask = refused.Ask
		}
	}
	return result, nil
}

// buildPayment builds an EIP-3009 authorization matching the payment terms,
// signs it, and encodes it as a header value.
func (a *Agent) buildPayment(req PaymentRequirements, amount *big.Int) (string, error) {
	nonce, err := a.paymentNonce()
	if err != nil {
		return "", err
	}
	timeout := req.MaxTimeoutSeconds
	if timeout <= 0 {
		timeout = 60
	}
	auth := wallet.Authorization{
		From:        a.Wallet.Address,
		To:          hexAddress(req.PayTo),
		Value:       amount,
		ValidAfter:  big.NewInt(0),
		ValidBefore: big.NewInt(time.Now().Unix() + int64(timeout)),
		Nonce:       nonce,
	}
	sig, err := a.Wallet.SignAuthorization(a.DomainSeparator, auth)
	if err != nil {
		return "", err
	}
	return EncodeHeader(PaymentPayload{
		X402Version:  Version,
		Scheme:       SchemeExact,
		Network:      req.Network,
		Mandate:      a.Mandate,
		Confirmation: a.Confirmation,
		Payload: ExactPayload{
			Signature: "0x" + hex.EncodeToString(sig),
			Authorization: AuthorizationJSON{
				From:        auth.From.Hex(),
				To:          auth.To.Hex(),
				Value:       auth.Value.String(),
				ValidAfter:  auth.ValidAfter.String(),
				ValidBefore: auth.ValidBefore.String(),
				Nonce:       "0x" + hex.EncodeToString(auth.Nonce[:]),
			},
		},
	})
}

// paymentNonce returns a fresh random nonce, unless a confirmation is attached,
// in which case it reuses the confirmed authorization nonce so the resigned
// payment is the one the delegator approved.
func (a *Agent) paymentNonce() ([32]byte, error) {
	if a.Confirmation != nil {
		b := common.FromHex(a.Confirmation.AuthorizationNonce)
		if len(b) != 32 {
			return [32]byte{}, fmt.Errorf("x402 agent: confirmation nonce is not 32 bytes")
		}
		var n [32]byte
		copy(n[:], b)
		return n, nil
	}
	return wallet.NewNonce()
}

func readBody(resp *http.Response) ([]byte, error) {
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("x402 agent: read body: %w", err)
	}
	return b, nil
}
