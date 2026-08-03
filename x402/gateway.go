package x402

import (
	"encoding/json"
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

// Gateway is an x402 payment gateway in front of a paid resource.
// It doubles as the facilitator: it verifies payments (signature and terms)
// and executes on-chain settlement, paying for gas.
type Gateway struct {
	Token       *token.Token
	Backend     Backend
	Facilitator *bind.TransactOpts // signer of settlement transactions (pays gas)
	PayTo       common.Address     // address that receives the payment
	Price       *big.Int           // resource price in tKRW
	Network     string             // e.g. eip155:1337
	Timeout     time.Duration      // how long to wait for settlement finality

	// Commit is called right after a settlement transaction is submitted.
	// On a simulated backend it mines a block; against a real node leave it nil.
	Commit func()

	domainSeparator [32]byte
	domainOnce      sync.Once
	domainErr       error
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

func (g *Gateway) domain() ([32]byte, error) {
	g.domainOnce.Do(func() {
		g.domainSeparator, g.domainErr = g.Token.DomainSeparator()
	})
	return g.domainSeparator, g.domainErr
}

// Middleware protects a handler with x402 payments.
// Payment verification is not implemented yet: every request receives 402
// with the payment terms.
func (g *Gateway) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resource := "http://" + r.Host + r.URL.Path
		g.writeRequirements(w, resource, "X-PAYMENT header is required")
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
