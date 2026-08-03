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

}

// Result is the outcome of a request that may have involved a payment.
type Result struct {
	StatusCode  int
	Body        []byte
	Paid        bool
	AmountPaid  *big.Int
	Settlement  *SettlementResponse
	Requirement *PaymentRequirements
}

func (a *Agent) httpClient() *http.Client {
	if a.HTTP != nil {
		return a.HTTP
	}
	return http.DefaultClient
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
	header, err := a.buildPayment(req, amount)
	if err != nil {
		return nil, err
	}

	// Retry with the payment attached.
	retry, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	retry.Header.Set(HeaderPayment, header)
	resp2, err := a.httpClient().Do(retry)
	if err != nil {
		return nil, fmt.Errorf("x402 agent: paid request: %w", err)
	}
	body2, err := readBody(resp2)
	if err != nil {
		return nil, err
	}

	result := &Result{
		StatusCode:  resp2.StatusCode,
		Body:        body2,
		Paid:        resp2.StatusCode < 400,
		AmountPaid:  amount,
		Requirement: &req,
	}
	if h := resp2.Header.Get(HeaderPaymentResponse); h != "" {
		var s SettlementResponse
		if err := DecodeHeader(h, &s); err == nil {
			result.Settlement = &s
		}
	}
	return result, nil
}

// buildPayment builds an EIP-3009 authorization matching the payment terms,
// signs it, and encodes it as a header value.
func (a *Agent) buildPayment(req PaymentRequirements, amount *big.Int) (string, error) {
	nonce, err := wallet.NewNonce()
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
		X402Version: Version,
		Scheme:      SchemeExact,
		Network:     req.Network,
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

func readBody(resp *http.Response) ([]byte, error) {
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("x402 agent: read body: %w", err)
	}
	return b, nil
}
