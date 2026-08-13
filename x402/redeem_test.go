package x402_test

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"math/big"
	"path/filepath"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient/simulated"
	"github.com/ethereum/go-ethereum/params"

	"github.com/kfangw/stablecoin-x402-gateway/reserve"
	"github.com/kfangw/stablecoin-x402-gateway/token"
	"github.com/kfangw/stablecoin-x402-gateway/wallet"
	"github.com/kfangw/stablecoin-x402-gateway/x402"
)

type redeemEnv struct {
	t          *testing.T
	sim        *simulated.Backend
	client     simulated.Client
	issuer     *bind.TransactOpts
	issuerAddr common.Address
	tok        *token.Token
	domain     [32]byte
	holder     *wallet.Wallet
}

func newRedeemEnv(t *testing.T, holderBalance int64) *redeemEnv {
	t.Helper()
	chainID := params.AllDevChainProtocolChanges.ChainID
	issuerKey, _ := crypto.GenerateKey()
	issuerAddr := crypto.PubkeyToAddress(issuerKey.PublicKey)
	holder, _ := wallet.New()
	eth := new(big.Int).Mul(big.NewInt(10), big.NewInt(params.Ether))
	sim := simulated.NewBackend(types.GenesisAlloc{issuerAddr: {Balance: eth}})
	t.Cleanup(func() { sim.Close() })
	client := sim.Client()

	issuer, _ := bind.NewKeyedTransactorWithChainID(issuerKey, chainID)
	tok, tx, err := token.Deploy(issuer, client)
	if err != nil {
		t.Fatal(err)
	}
	sim.Commit()
	if _, err := bind.WaitDeployed(context.Background(), client, tx); err != nil {
		t.Fatal(err)
	}
	mtx, err := tok.Mint(issuer, holder.Address, big.NewInt(holderBalance))
	if err != nil {
		t.Fatal(err)
	}
	sim.Commit()
	if _, err := bind.WaitMined(context.Background(), client, mtx); err != nil {
		t.Fatal(err)
	}
	domain, _ := tok.DomainSeparator()
	return &redeemEnv{t: t, sim: sim, client: client, issuer: issuer, issuerAddr: issuerAddr, tok: tok, domain: domain, holder: holder}
}

// request produces the wire form the agent's redeem-request emits, then parses
// it back, mirroring how the issuer receives it.
func (e *redeemEnv) request(amount int64) (wallet.Authorization, []byte) {
	e.t.Helper()
	nonce, _ := wallet.NewNonce()
	auth := wallet.Authorization{
		From:        e.holder.Address,
		To:          e.issuerAddr,
		Value:       big.NewInt(amount),
		ValidAfter:  big.NewInt(0),
		ValidBefore: big.NewInt(time.Now().Unix() + 300),
		Nonce:       nonce,
	}
	sig, err := e.holder.SignReceiveAuthorization(e.domain, auth)
	if err != nil {
		e.t.Fatal(err)
	}
	req := x402.RedemptionRequestJSON{
		Authorization: x402.AuthorizationJSON{
			From: auth.From.Hex(), To: auth.To.Hex(), Value: auth.Value.String(),
			ValidAfter: auth.ValidAfter.String(), ValidBefore: auth.ValidBefore.String(),
			Nonce: "0x" + hex.EncodeToString(auth.Nonce[:]),
		},
		Signature: "0x" + hex.EncodeToString(sig),
	}
	body, _ := json.Marshal(req)
	var back x402.RedemptionRequestJSON
	if err := json.Unmarshal(body, &back); err != nil {
		e.t.Fatal(err)
	}
	parsed, err := x402.ParseAuthorization(back.Authorization)
	if err != nil {
		e.t.Fatal(err)
	}
	sigBytes, _ := hex.DecodeString(back.Signature[2:])
	return parsed, sigBytes
}

// A redemption collects, burns, and records the reserve withdrawal, leaving the
// holder's balance and the total supply reduced by the redeemed amount.
func TestRedemptionRoundTrip(t *testing.T) {
	e := newRedeemEnv(t, 10_000)
	auth, sig := e.request(3_000)

	// Verify the request as the issuer would.
	if signer, err := wallet.RecoverReceiveSigner(e.domain, auth, sig); err != nil || signer != e.holder.Address {
		t.Fatalf("request must verify to the holder, got %s err %v", signer, err)
	}
	v, r, s, _ := wallet.SplitSignature(sig)

	// Step 1: collect.
	rtx, err := e.tok.ReceiveWithAuthorization(e.issuer, auth.From, auth.To, auth.Value, auth.ValidAfter, auth.ValidBefore, auth.Nonce, v, r, s)
	if err != nil {
		t.Fatal(err)
	}
	e.mine(rtx)
	// Step 2: burn.
	btx, err := e.tok.Burn(e.issuer, e.issuerAddr, auth.Value)
	if err != nil {
		t.Fatal(err)
	}
	e.mine(btx)
	// Step 3: reserve withdrawal.
	rl, _ := reserve.Open(filepath.Join(t.TempDir(), "reserve.jsonl"))
	defer rl.Close()
	rl.Append(big.NewInt(10_000), "backing")
	rl.Append(new(big.Int).Neg(auth.Value), "redemption")

	if bal, _ := e.tok.BalanceOf(e.holder.Address); bal.Int64() != 7_000 {
		t.Errorf("holder balance = %s, want 7000", bal)
	}
	if bal, _ := e.tok.BalanceOf(e.issuerAddr); bal.Sign() != 0 {
		t.Errorf("issuer balance = %s, want 0 (received then burned)", bal)
	}
	if supply, _ := e.tok.TotalSupply(); supply.Int64() != 7_000 {
		t.Errorf("total supply = %s, want 7000", supply)
	}
	if rl.Total().Int64() != 7_000 {
		t.Errorf("reserve total = %s, want 7000", rl.Total())
	}
}

// If the holder cannot cover the amount, the collect step reverts and nothing
// changes: the later steps are never taken.
func TestRedemptionInsufficientBalanceFails(t *testing.T) {
	e := newRedeemEnv(t, 1_000)
	auth, sig := e.request(5_000) // more than the holder holds
	v, r, s, _ := wallet.SplitSignature(sig)

	_, err := e.tok.ReceiveWithAuthorization(e.issuer, auth.From, auth.To, auth.Value, auth.ValidAfter, auth.ValidBefore, auth.Nonce, v, r, s)
	if err == nil {
		t.Fatal("collect must fail when the holder cannot cover the redemption")
	}
	// Nothing moved.
	if bal, _ := e.tok.BalanceOf(e.holder.Address); bal.Int64() != 1_000 {
		t.Errorf("holder balance = %s, want 1000 (unchanged)", bal)
	}
	if supply, _ := e.tok.TotalSupply(); supply.Int64() != 1_000 {
		t.Errorf("supply = %s, want 1000 (unchanged)", supply)
	}
}

func (e *redeemEnv) mine(tx *types.Transaction) {
	e.t.Helper()
	e.sim.Commit()
	if _, err := bind.WaitMined(context.Background(), e.client, tx); err != nil {
		e.t.Fatal(err)
	}
}
