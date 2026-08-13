// Package demoflow holds the in-process demo narrative as an event stream, so
// the terminal demo (cmd/demo) and the browser demo (cmd/demoweb) render the
// same run without duplicating it. Run drives the flow on a simulated chain and
// emits one Event per beat; the terminal renders Event.Text, while a UI reads
// the structured fields. gate is called before each numbered step so a caller
// can pause between steps.
package demoflow

import (
	"context"
	"crypto/ecdsa"
	"encoding/hex"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient/simulated"
	"github.com/ethereum/go-ethereum/params"

	"github.com/kfangw/stablecoin-x402-gateway/ledger"
	"github.com/kfangw/stablecoin-x402-gateway/registry"
	"github.com/kfangw/stablecoin-x402-gateway/token"
	"github.com/kfangw/stablecoin-x402-gateway/wallet"
	"github.com/kfangw/stablecoin-x402-gateway/x402"
)

// Event is one beat of the demo. Text is the terminal rendering (with its own
// newlines); the structured fields let a UI update role panels. Kind is one of
// step, log, balance, tx, error, audit, done.
type Event struct {
	Step      int               `json:"step,omitempty"`
	Title     string            `json:"title,omitempty"`
	Kind      string            `json:"kind"`
	Text      string            `json:"text,omitempty"`
	Balances  map[string]string `json:"balances,omitempty"`
	TxHash    string            `json:"txHash,omitempty"`
	ErrorCode string            `json:"errorCode,omitempty"`
	MandateID string            `json:"mandateId,omitempty"`
	AuditStep string            `json:"auditStep,omitempty"`
	AuditOK   bool              `json:"auditOk,omitempty"`
}

// Run executes the demo, emitting events and gating each step.
func Run(ctx context.Context, emit func(Event), gate func(step int)) error {
	e := &emitter{emit: emit, gate: gate}
	chainID := params.AllDevChainProtocolChanges.ChainID

	issuerKey, _ := crypto.GenerateKey()
	gatewayKey, _ := crypto.GenerateKey()
	agentWallet, err := wallet.New()
	if err != nil {
		return err
	}
	issuerAddr := crypto.PubkeyToAddress(issuerKey.PublicKey)
	gatewayAddr := crypto.PubkeyToAddress(gatewayKey.PublicKey)

	// Only the issuer and gateway hold ETH; the agent holds none and can still
	// pay, which is the point of the EIP-3009 path.
	eth := new(big.Int).Mul(big.NewInt(10), big.NewInt(params.Ether))
	sim := simulated.NewBackend(types.GenesisAlloc{
		issuerAddr:  {Balance: eth},
		gatewayAddr: {Balance: eth},
	})
	defer sim.Close()
	client := sim.Client()

	issuerOpts, _ := bind.NewKeyedTransactorWithChainID(issuerKey, chainID)
	gatewayOpts, _ := bind.NewKeyedTransactorWithChainID(gatewayKey, chainID)

	e.step(1, "Issuance: deploy tKRW and mint", "")
	tok, deployTx, err := token.Deploy(issuerOpts, client)
	if err != nil {
		return err
	}
	sim.Commit()
	if _, err := bind.WaitDeployed(ctx, client, deployTx); err != nil {
		return fmt.Errorf("deploy wait: %w", err)
	}
	e.logf("   contract: %s (issuer: %s)\n", tok.Address.Hex(), issuerAddr.Hex())

	mintAmount := big.NewInt(100_000)
	mintTx, err := tok.Mint(issuerOpts, agentWallet.Address, mintAmount)
	if err != nil {
		return err
	}
	sim.Commit()
	if _, err := bind.WaitMined(ctx, client, mintTx); err != nil {
		return err
	}
	e.logf("   minted %s tKRW to agent wallet %s (agent ETH balance: 0)\n\n", mintAmount, agentWallet.Address.Hex())

	reg, regTx, err := registry.Deploy(issuerOpts, client)
	if err != nil {
		return err
	}
	sim.Commit()
	if _, err := bind.WaitDeployed(ctx, client, regTx); err != nil {
		return fmt.Errorf("registry deploy wait: %w", err)
	}
	e.logf("   identity registry: %s\n\n", reg.Address.Hex())

	led := ledger.New(tok.Address, tok, client)

	e.step(2, "Gateway: protect a paid resource, require a registered agent", "")
	gw := &x402.Gateway{
		Token:      tok,
		Backend:    client,
		Transactor: gatewayOpts,
		PayTo:      gatewayAddr,
		Price:      big.NewInt(500),
		Network:    fmt.Sprintf("eip155:%s", chainID),
		Commit:     func() { sim.Commit() },
		Policy:     x402.Chain{x402.AlwaysVerify{}, x402.IdentityPolicy{Registry: reg}},

		RequireBoundNonce: true,
	}
	report := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"report":"market report body","paid":true}`)
	})
	server := httptest.NewServer(gw.Middleware(report))
	defer server.Close()
	e.logf("   price %s tKRW, payTo %s\n\n", gw.Price, gatewayAddr.Hex())

	e.step(3, "Identity: an unregistered agent is refused", "")
	domain, err := tok.DomainSeparator()
	if err != nil {
		return err
	}
	agent := &x402.Agent{
		Wallet:          agentWallet,
		DomainSeparator: domain,
		MaxAmount:       big.NewInt(1_000),
	}
	refused, err := agent.Get(server.URL + "/premium/report")
	if err != nil {
		return err
	}
	e.err(fmt.Sprintf("   HTTP %d, errorCode %q (agent not yet registered)\n\n", refused.StatusCode, refused.ErrorCode), refused.ErrorCode)
	if refused.StatusCode != http.StatusPaymentRequired || refused.ErrorCode != x402.ErrCodeIdentityUnregistered {
		return fmt.Errorf("expected an identity_unregistered rejection, got %d %q", refused.StatusCode, refused.ErrorCode)
	}

	e.step(4, "Register the agent", "")
	if err := fundETH(ctx, sim, issuerKey, agentWallet.Address, chainID); err != nil {
		return err
	}
	agentOpts, err := bind.NewKeyedTransactorWithChainID(agentWallet.Key(), chainID)
	if err != nil {
		return err
	}
	regReg := registry.Bind(reg.Address, client)
	registerTx, err := regReg.Register(agentOpts, "https://cards.example/demo-agent")
	if err != nil {
		return err
	}
	sim.Commit()
	if _, err := bind.WaitMined(ctx, client, registerTx); err != nil {
		return err
	}
	e.logf("   registered agent %s (tx %s)\n\n", agentWallet.Address.Hex(), registerTx.Hash().Hex())

	e.step(5, "Agent: read the 402 response and pay by signature", "")
	result, err := agent.Get(server.URL + "/premium/report")
	if err != nil {
		return err
	}
	e.logf("   HTTP %d, paid %s tKRW\n", result.StatusCode, result.AmountPaid)
	e.logf("   response body: %s\n", result.Body)
	if result.Settlement != nil {
		e.tx(fmt.Sprintf("   settlement tx: %s\n\n", result.Settlement.Transaction), result.Settlement.Transaction)
	}

	e.step(6, "Settlement check: balances", "")
	agentBal, _ := tok.BalanceOf(agentWallet.Address)
	payToBal, _ := tok.BalanceOf(gatewayAddr)
	e.balance(fmt.Sprintf("   agent balance: %s tKRW / payTo balance: %s tKRW\n\n", agentBal, payToBal),
		map[string]string{"agent tKRW": agentBal.String(), "payTo tKRW": payToBal.String()})

	e.step(7, "Ledger reconciliation: off-chain ledger vs on-chain state", "")
	rep, err := led.Reconcile(ctx)
	if err != nil {
		return err
	}
	e.logf("   %d events, %d accounts\n", rep.Events, rep.Accounts)
	e.logf("   minted %s - burned %s = ledger supply %s\n", rep.Minted, rep.Burned, rep.LedgerSupply)
	e.logf("   on-chain totalSupply %s, sum of balances %s\n", rep.OnChainSupply, rep.SumBalances)
	if !rep.OK() {
		return fmt.Errorf("reconciliation mismatches: %v", rep.Mismatches)
	}
	e.logf("   reconciliation passed: the ledger matches the chain\n")

	e.step(8, "Replay check: resend the same signature", "\n")
	replay, err := http.NewRequest(http.MethodGet, server.URL+"/premium/report", nil)
	if err != nil {
		return err
	}
	replay.Header.Set(x402.HeaderPayment, agent.LastPaymentHeader())
	resp, err := http.DefaultClient.Do(replay)
	if err != nil {
		return err
	}
	resp.Body.Close()
	e.logf("   reused X-PAYMENT header: HTTP %d (402 expected: nonce already used)\n", resp.StatusCode)
	if resp.StatusCode != http.StatusPaymentRequired {
		return fmt.Errorf("replay must be rejected, got %d", resp.StatusCode)
	}

	e.step(9, "Delegation: the gateway now also requires a signed mandate", "\n")
	delegatorKey, _ := crypto.GenerateKey()
	delegator := crypto.PubkeyToAddress(delegatorKey.PublicKey)
	mandatePolicy := x402.NewMandatePolicy(chainID)
	mandatePolicy.AskOnExceed = true
	mandatePolicy.DeferAbove = big.NewInt(1000)
	gw.Policy = x402.Chain{
		x402.AlwaysVerify{},
		x402.IdentityPolicy{Registry: reg},
		mandatePolicy,
	}
	goodMandate, err := signedMandate(delegatorKey, x402.Mandate{
		Delegator:           delegator,
		Agent:               agentWallet.Address,
		MaxAmountPerPayment: big.NewInt(500),
		AllowedPayees:       []common.Address{gatewayAddr},
		ValidAfter:          big.NewInt(0),
		ValidBefore:         big.NewInt(time.Now().Unix() + 3600),
		BudgetAmount:        big.NewInt(100_000),
		MandateID:           [32]byte{0x01},
	}, chainID)
	if err != nil {
		return err
	}
	e.mandate(fmt.Sprintf("   delegator %s signed mandate %s for the agent\n\n", delegator.Hex(), goodMandate.Mandate.MandateID), goodMandate.Mandate.MandateID)

	e.step(10, "Agent pays under the mandate", "")
	agent.Mandate = goodMandate
	within, err := agent.Get(server.URL + "/premium/report")
	if err != nil {
		return err
	}
	e.logf("   HTTP %d, paid %s tKRW under the mandate\n\n", within.StatusCode, within.AmountPaid)
	if within.StatusCode != http.StatusOK {
		return fmt.Errorf("payment under the mandate should settle, got %d %q", within.StatusCode, within.ErrorCode)
	}

	e.step(11, "A payment beyond the mandate asks the delegator", "")
	tightID := [32]byte{0x02}
	tightMandate, err := signedMandate(delegatorKey, x402.Mandate{
		Delegator:           delegator,
		Agent:               agentWallet.Address,
		MaxAmountPerPayment: big.NewInt(100),
		AllowedPayees:       []common.Address{gatewayAddr},
		ValidAfter:          big.NewInt(0),
		ValidBefore:         big.NewInt(time.Now().Unix() + 3600),
		MandateID:           tightID,
	}, chainID)
	if err != nil {
		return err
	}
	agent.Mandate = tightMandate
	agent.Confirmation = nil
	asked, err := agent.Get(server.URL + "/premium/report")
	if err != nil {
		return err
	}
	e.err(fmt.Sprintf("   HTTP %d, errorCode %q (500 tKRW over the 100 per-payment limit)\n", asked.StatusCode, asked.ErrorCode), asked.ErrorCode)
	if asked.ErrorCode != x402.ErrCodeConfirmationRequired || asked.Ask == nil {
		return fmt.Errorf("over-limit payment should be confirmation_required, got %q", asked.ErrorCode)
	}

	var nonce [32]byte
	copy(nonce[:], common.FromHex(asked.Ask.AuthorizationNonce))
	amount, _ := new(big.Int).SetString(asked.Ask.Amount, 10)
	conf, err := signConfirmation(delegatorKey, x402.Confirmation{
		MandateID:          tightID,
		AuthorizationNonce: nonce,
		Amount:             amount,
		Resource:           asked.Ask.Resource,
		ValidBefore:        big.NewInt(time.Now().Unix() + 600),
	}, chainID)
	if err != nil {
		return err
	}
	agent.Confirmation = conf
	confirmed, err := agent.Get(server.URL + "/premium/report")
	if err != nil {
		return err
	}
	e.logf("   after the delegator confirmed: HTTP %d, paid %s tKRW\n\n", confirmed.StatusCode, confirmed.AmountPaid)
	if confirmed.StatusCode != http.StatusOK {
		return fmt.Errorf("confirmed payment should settle, got %d %q", confirmed.StatusCode, confirmed.ErrorCode)
	}

	e.step(12, "Revocation: the delegator withdraws the mandate", "")
	revSig, err := x402.SignRevocation(delegatorKey, [32]byte{0x01}, chainID)
	if err != nil {
		return err
	}
	if err := gw.RevokeMandate(x402.RevocationJSON{
		MandateID: goodMandate.Mandate.MandateID,
		Signature: "0x" + hex.EncodeToString(revSig),
	}); err != nil {
		return err
	}
	agent.Mandate = goodMandate
	agent.Confirmation = nil
	revoked, err := agent.Get(server.URL + "/premium/report")
	if err != nil {
		return err
	}
	e.err(fmt.Sprintf("   HTTP %d, errorCode %q (the mandate no longer authorizes payment)\n\n", revoked.StatusCode, revoked.ErrorCode), revoked.ErrorCode)
	if revoked.ErrorCode != x402.ErrCodeMandateRevoked {
		return fmt.Errorf("payment under a revoked mandate should be mandate_revoked, got %q", revoked.ErrorCode)
	}

	e.step(13, "A large payment is delivered only after confirmations", "")
	gw.ConfirmDepth = 2
	gw.Price = big.NewInt(2000)
	agent.MaxAmount = big.NewInt(5000)
	bigMandate, err := signedMandate(delegatorKey, x402.Mandate{
		Delegator:           delegator,
		Agent:               agentWallet.Address,
		MaxAmountPerPayment: big.NewInt(2000),
		AllowedPayees:       []common.Address{gatewayAddr},
		ValidAfter:          big.NewInt(0),
		ValidBefore:         big.NewInt(time.Now().Unix() + 3600),
		BudgetAmount:        big.NewInt(100_000),
		MandateID:           [32]byte{0x03},
	}, chainID)
	if err != nil {
		return err
	}
	agent.Mandate = bigMandate
	agent.Confirmation = nil
	deferred, err := agent.Get(server.URL + "/premium/report")
	if err != nil {
		return err
	}
	e.err(fmt.Sprintf("   HTTP %d, errorCode %q (settled, delivery held for %d confirmations)\n", deferred.StatusCode, deferred.ErrorCode, gw.ConfirmDepth), deferred.ErrorCode)
	if deferred.ErrorCode != x402.ErrCodePaymentDeferred {
		return fmt.Errorf("large payment should be payment_deferred, got %q", deferred.ErrorCode)
	}
	sim.Commit()
	sim.Commit()
	delivered, err := agent.Retry(server.URL + "/premium/report")
	if err != nil {
		return err
	}
	e.logf("   after %d blocks, the same payment delivers: HTTP %d\n\n", gw.ConfirmDepth, delivered.StatusCode)
	if delivered.StatusCode != http.StatusOK {
		return fmt.Errorf("deferred payment should deliver once confirmed, got %d %q", delivered.StatusCode, delivered.ErrorCode)
	}

	e.done("demo complete: identity, signed payment, settlement, reconciliation, replay rejection, and the mandate lifecycle (grant, pay, ask and confirm, revoke, deferred delivery)\n")
	return nil
}

// emitter carries the emit and gate callbacks and formats events.
type emitter struct {
	emit func(Event)
	gate func(step int)
}

func (e *emitter) step(n int, title, lead string) {
	if e.gate != nil {
		e.gate(n)
	}
	e.emit(Event{Step: n, Kind: "step", Title: title, Text: lead + fmt.Sprintf("== %d. %s ==\n", n, title)})
}

func (e *emitter) logf(format string, args ...any) {
	e.emit(Event{Kind: "log", Text: fmt.Sprintf(format, args...)})
}

func (e *emitter) err(text, code string) {
	e.emit(Event{Kind: "error", Text: text, ErrorCode: code})
}

func (e *emitter) tx(text, hash string) {
	e.emit(Event{Kind: "tx", Text: text, TxHash: hash})
}

func (e *emitter) balance(text string, balances map[string]string) {
	e.emit(Event{Kind: "balance", Text: text, Balances: balances})
}

func (e *emitter) mandate(text, id string) {
	e.emit(Event{Kind: "log", Text: text, MandateID: id})
}

func (e *emitter) done(text string) {
	e.emit(Event{Kind: "done", Text: text})
}

func signConfirmation(key *ecdsa.PrivateKey, c x402.Confirmation, chainID *big.Int) (*x402.ConfirmationJSON, error) {
	sig, err := x402.SignConfirmation(key, c, chainID)
	if err != nil {
		return nil, err
	}
	cj := c.ToJSON()
	cj.Signature = "0x" + hex.EncodeToString(sig)
	return &cj, nil
}

func signedMandate(key *ecdsa.PrivateKey, m x402.Mandate, chainID *big.Int) (*x402.SignedMandateJSON, error) {
	sig, err := x402.SignMandate(key, m, chainID)
	if err != nil {
		return nil, err
	}
	return &x402.SignedMandateJSON{Mandate: m.ToJSON(), Signature: "0x" + hex.EncodeToString(sig)}, nil
}

func fundETH(ctx context.Context, sim *simulated.Backend, fromKey *ecdsa.PrivateKey, to common.Address, chainID *big.Int) error {
	client := sim.Client()
	from := crypto.PubkeyToAddress(fromKey.PublicKey)
	nonce, err := client.PendingNonceAt(ctx, from)
	if err != nil {
		return err
	}
	gasPrice, err := client.SuggestGasPrice(ctx)
	if err != nil {
		return err
	}
	amount := new(big.Int).Div(big.NewInt(params.Ether), big.NewInt(100))
	tx := types.NewTransaction(nonce, to, amount, 21000, gasPrice, nil)
	signed, err := types.SignTx(tx, types.LatestSignerForChainID(chainID), fromKey)
	if err != nil {
		return err
	}
	if err := client.SendTransaction(ctx, signed); err != nil {
		return err
	}
	sim.Commit()
	_, err = bind.WaitMined(ctx, client, signed)
	return err
}
