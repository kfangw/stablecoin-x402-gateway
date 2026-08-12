package x402_test

import (
	"context"
	"encoding/hex"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient/simulated"
	"github.com/ethereum/go-ethereum/params"

	"github.com/kfangw/stablecoin-x402-gateway/spend"
	"github.com/kfangw/stablecoin-x402-gateway/token"
	"github.com/kfangw/stablecoin-x402-gateway/x402"
)

var parityFarFuture = big.NewInt(1 << 62)

// parityCase is one shared scenario run against both the off-chain MandatePolicy
// and the on-chain DelegatedSpend contract. wantAccept is what both must agree on.
type parityCase struct {
	name          string
	maxPerPayment int64
	budget        int64
	amount        int64
	payeeAllowed  bool
	expired       bool
	revoked       bool
	wantAccept    bool
}

var parityCases = []parityCase{
	{"normal", 500, 1000, 200, true, false, false, true},
	{"over per-payment limit", 500, 100000, 600, true, false, false, false},
	{"disallowed payee", 500, 100000, 200, false, false, false, false},
	{"expired", 500, 100000, 200, true, true, false, false},
	{"revoked", 500, 100000, 200, true, false, true, false},
	{"over cumulative budget", 500, 100, 200, true, false, false, false},
}

// The gateway's MandatePolicy and the DelegatedSpend contract must reach the same
// accept/reject verdict on a shared table of mandate cases.
func TestMandateEnforcementParity(t *testing.T) {
	for _, c := range parityCases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			off := offchainAccepts(t, c)
			on := onchainAccepts(t, c)
			if off != c.wantAccept {
				t.Errorf("off-chain accept = %v, want %v", off, c.wantAccept)
			}
			if on != c.wantAccept {
				t.Errorf("on-chain accept = %v, want %v", on, c.wantAccept)
			}
			if off != on {
				t.Errorf("parity broken: off-chain %v vs on-chain %v", off, on)
			}
		})
	}
}

// offchainAccepts runs the case through the gateway's MandatePolicy.
func offchainAccepts(t *testing.T, c parityCase) bool {
	t.Helper()
	dk, _ := crypto.GenerateKey()
	delegator := crypto.PubkeyToAddress(dk.PublicKey)
	ak, _ := crypto.GenerateKey()
	agent := crypto.PubkeyToAddress(ak.PublicKey)
	payTo := common.HexToAddress("0x00000000000000000000000000000000000000ee")
	other := common.HexToAddress("0x000000000000000000000000000000000000dead")

	validBefore := big.NewInt(2_000_000) // fixedNow is 1_000_000, so this is valid
	if c.expired {
		validBefore = big.NewInt(1)
	}
	m := x402.Mandate{
		Delegator:           delegator,
		Agent:               agent,
		MaxAmountPerPayment: big.NewInt(c.maxPerPayment),
		AllowedPayees:       []common.Address{payTo},
		ValidAfter:          big.NewInt(0),
		ValidBefore:         validBefore,
		BudgetAmount:        big.NewInt(c.budget),
		BudgetWindowSeconds: big.NewInt(3600),
		MandateID:           [32]byte{0x01},
	}
	sm := signMandateJSON(t, dk, m)

	mp := x402.NewMandatePolicy(mandateChainID)
	mp.Now = fixedNow
	gw := &x402.Gateway{Network: "eip155:1337", Policy: x402.Chain{x402.AlwaysVerify{}, mp}}
	if c.revoked {
		sig, err := x402.SignRevocation(dk, m.MandateID, mandateChainID)
		if err != nil {
			t.Fatal(err)
		}
		if err := gw.RevokeMandate(x402.RevocationJSON{
			MandateID: "0x" + hex.EncodeToString(m.MandateID[:]),
			Signature: "0x" + hex.EncodeToString(sig),
		}); err != nil {
			t.Fatal(err)
		}
	}

	target := payTo
	if !c.payeeAllowed {
		target = other
	}
	amount := big.NewInt(c.amount).String()
	pc := mandateContext(sm, agent, target, "http://gw/r", amount)
	// The cumulative-budget accounting keys on the payment nonce, so a valid
	// payment must carry one.
	pc.Payload.Payload.Authorization.Nonce = "0x" + hex.EncodeToString(make([]byte, 32))
	d := mp.Decide(context.Background(), pc)
	return d.Action == x402.ActionApprove
}

// onchainAccepts runs the equivalent case through the DelegatedSpend contract.
func onchainAccepts(t *testing.T, c parityCase) bool {
	t.Helper()
	chainID := params.AllDevChainProtocolChanges.ChainID
	ik, _ := crypto.GenerateKey()
	dk, _ := crypto.GenerateKey()
	ak, _ := crypto.GenerateKey()
	issuerAddr := crypto.PubkeyToAddress(ik.PublicKey)
	delegAddr := crypto.PubkeyToAddress(dk.PublicKey)
	agentAddr := crypto.PubkeyToAddress(ak.PublicKey)
	payee := common.HexToAddress("0x00000000000000000000000000000000000000ee")
	other := common.HexToAddress("0x000000000000000000000000000000000000dead")

	eth := new(big.Int).Mul(big.NewInt(10), big.NewInt(params.Ether))
	sim := simulated.NewBackend(types.GenesisAlloc{
		issuerAddr: {Balance: eth},
		delegAddr:  {Balance: eth},
		agentAddr:  {Balance: eth},
	})
	t.Cleanup(func() { sim.Close() })
	client := sim.Client()

	issuer, _ := bind.NewKeyedTransactorWithChainID(ik, chainID)
	delegator, _ := bind.NewKeyedTransactorWithChainID(dk, chainID)
	agent, _ := bind.NewKeyedTransactorWithChainID(ak, chainID)

	mine := func(tx *types.Transaction, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		sim.Commit()
		if _, err := bind.WaitMined(context.Background(), client, tx); err != nil {
			t.Fatal(err)
		}
	}

	tok, tx, err := token.Deploy(issuer, client)
	mine(tx, err)
	sp, stx, err := spend.Deploy(issuer, client, tok.Address)
	mine(stx, err)
	mine(tok.Mint(issuer, delegAddr, big.NewInt(100_000)))
	mine(tok.Approve(delegator, sp.Address, big.NewInt(100_000)))
	mine(sp.Deposit(delegator, big.NewInt(100_000)))

	validBefore := parityFarFuture
	if c.expired {
		validBefore = big.NewInt(1)
	}
	id := [32]byte{0x01}
	mine(sp.SetMandate(delegator, id, agentAddr,
		big.NewInt(c.maxPerPayment), big.NewInt(0), validBefore,
		big.NewInt(c.budget), big.NewInt(3600),
		[]common.Address{payee}))
	if c.revoked {
		mine(sp.Revoke(delegator, id))
	}

	target := payee
	if !c.payeeAllowed {
		target = other
	}
	ptx, err := sp.Pay(agent, id, target, big.NewInt(c.amount))
	if err != nil {
		return false // reverted at estimation
	}
	sim.Commit()
	if _, err := bind.WaitMined(context.Background(), client, ptx); err != nil {
		return false
	}
	return true
}

// TestWindowSemanticsDifferByDesign documents the one intended divergence between
// the two enforcers: the cumulative budget window. The contract uses a fixed
// window that resets whole once it elapses; the gateway uses a sliding window
// that only forgets a spend once it ages out of the trailing window. Right after
// a boundary the fixed window admits a payment the sliding window would still
// reject, because the sliding window keeps counting a spend made just before the
// boundary. The gateway's sliding behavior is covered in the accounting tests;
// here we demonstrate the contract's fixed reset that produces the mismatch.
func TestWindowSemanticsDifferByDesign(t *testing.T) {
	chainID := params.AllDevChainProtocolChanges.ChainID
	ik, _ := crypto.GenerateKey()
	dk, _ := crypto.GenerateKey()
	ak, _ := crypto.GenerateKey()
	issuerAddr := crypto.PubkeyToAddress(ik.PublicKey)
	delegAddr := crypto.PubkeyToAddress(dk.PublicKey)
	agentAddr := crypto.PubkeyToAddress(ak.PublicKey)
	payee := common.HexToAddress("0x00000000000000000000000000000000000000ee")

	eth := new(big.Int).Mul(big.NewInt(10), big.NewInt(params.Ether))
	sim := simulated.NewBackend(types.GenesisAlloc{
		issuerAddr: {Balance: eth},
		delegAddr:  {Balance: eth},
		agentAddr:  {Balance: eth},
	})
	t.Cleanup(func() { sim.Close() })
	client := sim.Client()

	issuer, _ := bind.NewKeyedTransactorWithChainID(ik, chainID)
	delegator, _ := bind.NewKeyedTransactorWithChainID(dk, chainID)
	agent, _ := bind.NewKeyedTransactorWithChainID(ak, chainID)

	mine := func(tx *types.Transaction, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		sim.Commit()
		if _, err := bind.WaitMined(context.Background(), client, tx); err != nil {
			t.Fatal(err)
		}
	}

	tok, tx, err := token.Deploy(issuer, client)
	mine(tx, err)
	sp, stx, err := spend.Deploy(issuer, client, tok.Address)
	mine(stx, err)
	mine(tok.Mint(issuer, delegAddr, big.NewInt(100_000)))
	mine(tok.Approve(delegator, sp.Address, big.NewInt(100_000)))
	mine(sp.Deposit(delegator, big.NewInt(100_000)))

	id := [32]byte{0x02}
	// Budget 500 over a 100-second window, per-payment cap high enough not to bind.
	mine(sp.SetMandate(delegator, id, agentAddr,
		big.NewInt(1000), big.NewInt(0), parityFarFuture,
		big.NewInt(500), big.NewInt(100),
		[]common.Address{payee}))

	pay := func(amount int64) error {
		ptx, err := sp.Pay(agent, id, payee, big.NewInt(amount))
		if err != nil {
			return err
		}
		sim.Commit()
		_, err = bind.WaitMined(context.Background(), client, ptx)
		return err
	}

	// Spend 300, then 200 sixty seconds later: the window budget (500) is used up.
	if err := pay(300); err != nil {
		t.Fatal(err)
	}
	if err := sim.AdjustTime(60 * time.Second); err != nil {
		t.Fatal(err)
	}
	sim.Commit()
	if err := pay(200); err != nil {
		t.Fatal(err)
	}
	// Cross the fixed window boundary (another 60s puts us past 100s from the
	// window start). A 400 payment now succeeds: the fixed window reset the whole
	// budget. A sliding 100-second window would still count the 200 spent 60s ago,
	// so 200 + 400 > 500 and it would reject. That divergence is by design.
	if err := sim.AdjustTime(60 * time.Second); err != nil {
		t.Fatal(err)
	}
	sim.Commit()
	if err := pay(400); err != nil {
		t.Fatalf("fixed window should have reset, admitting the payment: %v", err)
	}
}
