package x402

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"

	"github.com/kfangw/stablecoin-x402-gateway/token"
	"github.com/kfangw/stablecoin-x402-gateway/wallet"
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

func (g *Gateway) domain() ([32]byte, error) {
	g.domainOnce.Do(func() {
		g.domainSeparator, g.domainErr = g.Token.DomainSeparator()
	})
	return g.domainSeparator, g.domainErr
}

// Middleware protects a handler with x402 payments.
// Requests without an X-PAYMENT header receive 402 and the payment terms;
// requests with a valid payment are settled on-chain and passed through.
func (g *Gateway) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resource := "http://" + r.Host + r.URL.Path

		header := r.Header.Get(HeaderPayment)
		if header == "" {
			g.writeRequirements(w, resource, "X-PAYMENT header is required")
			return
		}

		record, errMsg := g.verifyAndSettle(r.Context(), header)
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

// verifyAndSettle validates the payment payload and executes on-chain
// settlement. On failure it returns a human-readable reason (empty on success).
func (g *Gateway) verifyAndSettle(ctx context.Context, header string) (SettlementRecord, string) {
	var p PaymentPayload
	if err := DecodeHeader(header, &p); err != nil {
		return SettlementRecord{}, fmt.Sprintf("invalid X-PAYMENT header: %v", err)
	}

	// 1. Protocol-level checks
	if p.X402Version != Version {
		return SettlementRecord{}, fmt.Sprintf("unsupported x402 version %d", p.X402Version)
	}
	if p.Scheme != SchemeExact {
		return SettlementRecord{}, fmt.Sprintf("unsupported scheme %q", p.Scheme)
	}
	if p.Network != g.Network {
		return SettlementRecord{}, fmt.Sprintf("wrong network %q (want %q)", p.Network, g.Network)
	}

	auth, sig, err := parseExactPayload(p.Payload)
	if err != nil {
		return SettlementRecord{}, err.Error()
	}

	// 2. Payment terms
	if auth.To != g.PayTo {
		return SettlementRecord{}, fmt.Sprintf("payTo mismatch: %s (want %s)", auth.To, g.PayTo)
	}
	if auth.Value.Cmp(g.Price) < 0 {
		return SettlementRecord{}, fmt.Sprintf("insufficient amount %s (need %s)", auth.Value, g.Price)
	}
	now := time.Now().Unix()
	if auth.ValidBefore.Cmp(big.NewInt(now+5)) <= 0 {
		return SettlementRecord{}, "authorization expires too soon"
	}
	if auth.ValidAfter.Cmp(big.NewInt(now)) >= 0 {
		return SettlementRecord{}, "authorization not yet valid"
	}

	// 3. Signature check, off-chain first so bad payments never reach the chain
	domain, err := g.domain()
	if err != nil {
		return SettlementRecord{}, fmt.Sprintf("domain separator: %v", err)
	}
	signer, err := wallet.RecoverSigner(domain, auth, sig)
	if err != nil {
		return SettlementRecord{}, fmt.Sprintf("signature recovery failed: %v", err)
	}
	if signer != auth.From {
		return SettlementRecord{}, fmt.Sprintf("signer %s != from %s", signer, auth.From)
	}

	// 4. Replay and balance checks
	used, err := g.Token.AuthorizationState(auth.From, auth.Nonce)
	if err != nil {
		return SettlementRecord{}, fmt.Sprintf("authorization state: %v", err)
	}
	if used {
		return SettlementRecord{}, "authorization already used"
	}
	bal, err := g.Token.BalanceOf(auth.From)
	if err != nil {
		return SettlementRecord{}, fmt.Sprintf("balanceOf: %v", err)
	}
	if bal.Cmp(auth.Value) < 0 {
		return SettlementRecord{}, fmt.Sprintf("payer balance %s < %s", bal, auth.Value)
	}

	// 5. On-chain settlement: the gateway pays gas and executes EIP-3009.
	v, rr, ss, err := wallet.SplitSignature(sig)
	if err != nil {
		return SettlementRecord{}, err.Error()
	}
	tx, err := g.Token.TransferWithAuthorization(
		g.Facilitator, auth.From, auth.To, auth.Value, auth.ValidAfter, auth.ValidBefore, auth.Nonce, v, rr, ss,
	)
	if err != nil {
		return SettlementRecord{}, fmt.Sprintf("settlement submit failed: %v", err)
	}
	if g.Commit != nil {
		g.Commit()
	}

	waitCtx, cancel := context.WithTimeout(ctx, g.timeout())
	defer cancel()
	receipt, err := bind.WaitMined(waitCtx, g.Backend, tx)
	if err != nil {
		return SettlementRecord{}, fmt.Sprintf("settlement wait failed: %v", err)
	}
	if receipt.Status != types.ReceiptStatusSuccessful {
		return SettlementRecord{}, "settlement transaction reverted"
	}

	record := SettlementRecord{Payer: auth.From, Amount: auth.Value, TxHash: tx.Hash(), Time: time.Now()}
	g.mu.Lock()
	g.Settlements = append(g.Settlements, record)
	g.mu.Unlock()
	return record, ""
}

func parseExactPayload(p ExactPayload) (wallet.Authorization, []byte, error) {
	var a wallet.Authorization
	if !common.IsHexAddress(p.Authorization.From) || !common.IsHexAddress(p.Authorization.To) {
		return a, nil, fmt.Errorf("invalid from/to address")
	}
	a.From = common.HexToAddress(p.Authorization.From)
	a.To = common.HexToAddress(p.Authorization.To)

	var ok bool
	if a.Value, ok = new(big.Int).SetString(p.Authorization.Value, 10); !ok || a.Value.Sign() < 0 {
		return a, nil, fmt.Errorf("invalid value %q", p.Authorization.Value)
	}
	if a.ValidAfter, ok = new(big.Int).SetString(p.Authorization.ValidAfter, 10); !ok {
		return a, nil, fmt.Errorf("invalid validAfter")
	}
	if a.ValidBefore, ok = new(big.Int).SetString(p.Authorization.ValidBefore, 10); !ok {
		return a, nil, fmt.Errorf("invalid validBefore")
	}

	nonceBytes, err := hex.DecodeString(strings.TrimPrefix(p.Authorization.Nonce, "0x"))
	if err != nil || len(nonceBytes) != 32 {
		return a, nil, fmt.Errorf("invalid nonce")
	}
	copy(a.Nonce[:], nonceBytes)

	sig, err := hex.DecodeString(strings.TrimPrefix(p.Signature, "0x"))
	if err != nil || len(sig) != 65 {
		return a, nil, fmt.Errorf("invalid signature encoding")
	}
	return a, sig, nil
}
