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
	"os"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient/simulated"
	"github.com/ethereum/go-ethereum/params"

	"github.com/kfangw/stablecoin-x402-gateway/asset"
	"github.com/kfangw/stablecoin-x402-gateway/dvp"
	"github.com/kfangw/stablecoin-x402-gateway/eligibility"
	"github.com/kfangw/stablecoin-x402-gateway/ledger"
	"github.com/kfangw/stablecoin-x402-gateway/registry"
	"github.com/kfangw/stablecoin-x402-gateway/token"
	"github.com/kfangw/stablecoin-x402-gateway/wallet"
	"github.com/kfangw/stablecoin-x402-gateway/x402"
)

// transferTopic is the ERC-20 Transfer event signature. The audit step scans a
// settlement receipt's logs for the payment and delivery transfers.
var transferTopic = crypto.Keccak256Hash([]byte("Transfer(address,address,uint256)"))

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

	// mine commits a block and waits for a transaction, so the remaining steps
	// can submit provisioning transactions without repeating the boilerplate.
	mine := func(tx *types.Transaction, err error) error {
		if err != nil {
			return err
		}
		sim.Commit()
		_, err = bind.WaitMined(ctx, client, tx)
		return err
	}

	e.step(14, "Provision: eligibility registry, RWA asset, and the DvP contract", "")
	sellerKey, _ := crypto.GenerateKey()
	sellerAddr := crypto.PubkeyToAddress(sellerKey.PublicKey)
	if err := fundETH(ctx, sim, issuerKey, sellerAddr, chainID); err != nil {
		return err
	}
	if err := fundETH(ctx, sim, issuerKey, delegator, chainID); err != nil {
		return err
	}
	sellerOpts, err := bind.NewKeyedTransactorWithChainID(sellerKey, chainID)
	if err != nil {
		return err
	}
	delegatorOpts, err := bind.NewKeyedTransactorWithChainID(delegatorKey, chainID)
	if err != nil {
		return err
	}

	elig, etx, err := eligibility.Deploy(issuerOpts, client)
	if err := mine(etx, err); err != nil {
		return err
	}
	ast, atx, err := asset.Deploy(issuerOpts, client, elig.Address)
	if err := mine(atx, err); err != nil {
		return err
	}
	dvpC, dtx, err := dvp.Deploy(issuerOpts, client, tok.Address, ast.Address)
	if err := mine(dtx, err); err != nil {
		return err
	}
	e.logf("   eligibility %s, asset %s, dvp %s\n", elig.Address.Hex(), ast.Address.Hex(), dvpC.Address.Hex())

	// The delegator is eligible and sponsors the agent, so the agent inherits
	// eligibility; the seller stocks the asset and approves the DvP contract to
	// pull it on delivery.
	if err := mine(elig.SetEligible(issuerOpts, delegator, true)); err != nil {
		return err
	}
	if err := mine(elig.DelegateEligibility(delegatorOpts, agentWallet.Address)); err != nil {
		return err
	}
	// The seller must be eligible to hold the asset it delivers.
	if err := mine(elig.SetEligible(issuerOpts, sellerAddr, true)); err != nil {
		return err
	}
	if err := mine(ast.Mint(issuerOpts, sellerAddr, big.NewInt(10))); err != nil {
		return err
	}
	if err := mine(ast.Approve(sellerOpts, dvpC.Address, big.NewInt(100))); err != nil {
		return err
	}
	e.logf("   delegator %s is eligible and sponsors the agent; seller %s holds 10 tRWA\n\n", delegator.Hex(), sellerAddr.Hex())

	// A second gateway settles through the DvP contract, gated by eligibility,
	// and signs receipts. Its journal is the audit's revocation source.
	dvpMandatePolicy := x402.NewMandatePolicy(chainID)
	dvpGW := &x402.Gateway{
		Token:             tok,
		Backend:           client,
		PayTo:             sellerAddr,
		Price:             big.NewInt(500),
		Network:           fmt.Sprintf("eip155:%s", chainID),
		Commit:            func() { sim.Commit() },
		DvPAddress:        dvpC.Address,
		RequireBoundNonce: true,
		ReceiptKey:        gatewayKey,
		Policy: x402.Chain{
			x402.AlwaysVerify{},
			x402.IdentityPolicy{Registry: reg},
			x402.EligibilityPolicy{Registry: elig},
			dvpMandatePolicy,
		},
		Facilitator: &x402.LocalFacilitator{
			Token:       tok,
			Backend:     client,
			Transactor:  sellerOpts,
			Commit:      func() { sim.Commit() },
			DvP:         dvpC,
			AssetAmount: big.NewInt(1),
		},
	}
	journalFile, err := os.CreateTemp("", "demoweb-journal-*.log")
	if err != nil {
		return err
	}
	journalFile.Close()
	defer os.Remove(journalFile.Name())
	journal, err := x402.Open(journalFile.Name())
	if err != nil {
		return err
	}
	defer journal.Close()
	dvpGW.AttachJournal(journal)

	assetHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"asset":"tRWA","delivered":true}`)
	})
	dvpServer := httptest.NewServer(dvpGW.Middleware(assetHandler))
	defer dvpServer.Close()
	assetResource := dvpServer.URL + "/premium/asset"

	dvpMandate, err := signedMandate(delegatorKey, x402.Mandate{
		Delegator:           delegator,
		Agent:               agentWallet.Address,
		MaxAmountPerPayment: big.NewInt(500),
		AllowedPayees:       []common.Address{sellerAddr},
		ValidAfter:          big.NewInt(0),
		ValidBefore:         big.NewInt(time.Now().Unix() + 3600),
		BudgetAmount:        big.NewInt(100_000),
		MandateID:           [32]byte{0x04},
	}, chainID)
	if err != nil {
		return err
	}

	e.step(15, "Refuse: an ineligible payer is turned away before settlement", "")
	if err := mine(elig.SetEligible(issuerOpts, delegator, false)); err != nil {
		return err
	}
	agent.Mandate = dvpMandate
	agent.Confirmation = nil
	agent.MaxAmount = big.NewInt(500)
	ineligible, err := agent.Get(assetResource)
	if err != nil {
		return err
	}
	e.err(fmt.Sprintf("   HTTP %d, errorCode %q (the agent's sponsored eligibility was withdrawn)\n", ineligible.StatusCode, ineligible.ErrorCode), ineligible.ErrorCode)
	if ineligible.ErrorCode != x402.ErrCodeNotEligible {
		return fmt.Errorf("ineligible payer should be payer_not_eligible, got %q", ineligible.ErrorCode)
	}
	if err := mine(elig.SetEligible(issuerOpts, delegator, true)); err != nil {
		return err
	}
	e.logf("   sponsorship restored: the agent is eligible again\n\n")

	e.step(16, "Deliver atomically: DvP settles payment and delivery in one transaction", "")
	bought, err := agent.Get(assetResource)
	if err != nil {
		return err
	}
	if bought.StatusCode != http.StatusOK {
		return fmt.Errorf("DvP purchase should settle, got %d %q", bought.StatusCode, bought.ErrorCode)
	}
	settleTx := ""
	if bought.Settlement != nil {
		settleTx = bought.Settlement.Transaction
	}
	e.tx(fmt.Sprintf("   settlement tx: %s (payment and delivery together)\n", settleTx), settleTx)
	agentTKRW, _ := tok.BalanceOf(agentWallet.Address)
	sellerTKRW, _ := tok.BalanceOf(sellerAddr)
	agentTRWA, _ := ast.BalanceOf(agentWallet.Address)
	sellerTRWA, _ := ast.BalanceOf(sellerAddr)
	e.balance(fmt.Sprintf("   agent: %s tKRW / %s tRWA, seller: %s tKRW / %s tRWA\n\n", agentTKRW, agentTRWA, sellerTKRW, sellerTRWA),
		map[string]string{
			"agent tKRW":  agentTKRW.String(),
			"agent tRWA":  agentTRWA.String(),
			"seller tKRW": sellerTKRW.String(),
			"seller tRWA": sellerTRWA.String(),
		})

	e.step(17, "Receipt: the gateway signs the settlement", "")
	if bought.Settlement == nil || bought.Settlement.Receipt == nil {
		return fmt.Errorf("DvP settlement should carry a signed receipt")
	}
	signedReceipt := bought.Settlement.Receipt
	receipt, err := signedReceipt.Receipt.ToReceipt()
	if err != nil {
		return err
	}
	e.logf("   settlement tx %s, mandate %s\n", receipt.SettlementTx.Hex(), "0x"+hex.EncodeToString(receipt.MandateID[:]))
	e.logf("   signed for delegator %s, payer %s\n\n", receipt.Delegator.Hex(), receipt.Payer.Hex())

	e.step(18, "Audit: verify the receipt offline, then confirm settlement on chain", "")
	receiptSig, err := hex.DecodeString(strings.TrimPrefix(signedReceipt.Signature, "0x"))
	if err != nil {
		return err
	}
	signer, verifyErr := x402.VerifyReceipt(receipt, receiptSig)
	e.audit("receipt signature", verifyErr == nil && signer == gatewayAddr, fmt.Sprintf("signed by %s", signer.Hex()))

	mand, err := dvpMandate.Mandate.ToMandate()
	if err != nil {
		return err
	}
	mandateSig, err := hex.DecodeString(strings.TrimPrefix(dvpMandate.Signature, "0x"))
	if err != nil {
		return err
	}
	recovered, mandateErr := x402.VerifyMandate(mand, mandateSig, chainID)
	chainOK := mandateErr == nil && recovered == receipt.Delegator && mand.Agent == receipt.Payer && mand.MandateID == receipt.MandateID
	e.audit("mandate delegation chain", chainOK, fmt.Sprintf("delegator %s, agent %s", recovered.Hex(), mand.Agent.Hex()))

	mandateHex := "0x" + hex.EncodeToString(receipt.MandateID[:])
	revokedBefore := false
	for _, en := range journal.Entries() {
		if en.Kind == "revocation" && en.Revocation != nil &&
			strings.EqualFold(en.Revocation.MandateID, mandateHex) && en.At < receipt.IssuedAt {
			revokedBefore = true
		}
	}
	e.audit("revocation status", !revokedBefore, "no revocation before the receipt was issued")

	scopeErr := mand.WithinScope(receipt.PayTo, receipt.Resource, receipt.Amount)
	e.audit("payment within mandate scope", scopeErr == nil, "amount, payee, and resource are within scope")

	rc, err := client.TransactionReceipt(ctx, receipt.SettlementTx)
	if err != nil {
		return err
	}
	paidOnChain := rc.Status == types.ReceiptStatusSuccessful && hasTransfer(rc, receipt.PayTo, receipt.Amount)
	e.audit("settlement on chain", paidOnChain, fmt.Sprintf("settlement tx %s status %d", receipt.SettlementTx.Hex(), rc.Status))
	e.audit("asset delivery on chain", hasTransfer(rc, receipt.Payer, nil), "asset delivered within the settlement transaction")

	e.done("demo complete: identity, signed payment, settlement, reconciliation, replay rejection, the mandate lifecycle (grant, pay, ask and confirm, revoke, deferred delivery), eligibility-gated DvP delivery, a signed receipt, and an offline audit\n")
	return nil
}

// hasTransfer reports whether the receipt logs contain an ERC-20 Transfer to
// `to`; when amount is non-nil the value must also match. The recipient is the
// third indexed topic. It mirrors the audit command's on-chain check.
func hasTransfer(rc *types.Receipt, to common.Address, amount *big.Int) bool {
	for _, lg := range rc.Logs {
		if len(lg.Topics) != 3 || lg.Topics[0] != transferTopic {
			continue
		}
		if common.BytesToAddress(lg.Topics[2].Bytes()) != to {
			continue
		}
		if amount == nil {
			return true
		}
		if new(big.Int).SetBytes(lg.Data).Cmp(amount) == 0 {
			return true
		}
	}
	return false
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

func (e *emitter) audit(name string, ok bool, detail string) {
	status := "PASS"
	if !ok {
		status = "FAIL"
	}
	e.emit(Event{Kind: "audit", AuditStep: name, AuditOK: ok, Text: fmt.Sprintf("   [%s] %s: %s\n", status, name, detail)})
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
