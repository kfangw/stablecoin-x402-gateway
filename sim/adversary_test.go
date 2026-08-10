package sim

import (
	"math/big"
	"math/rand"
	"testing"

	"github.com/ethereum/go-ethereum/params"

	"github.com/kfangw/stablecoin-x402-gateway/x402"
)

// Each attack in the catalog carries a positive loss and its distinguishing
// shape.
func TestAttackCatalogShapes(t *testing.T) {
	seen := map[Kind]WorkItem{}
	r := rand.New(rand.NewSource(1))
	for len(seen) < 3 {
		it := attackItem(r, 1000, 0.2)
		seen[it.Kind] = it
	}
	inflated := seen[AttackInflatedTerms]
	if inflated.ServedAmount <= inflated.Amount || inflated.Loss <= 0 {
		t.Errorf("inflated-terms: %+v, want served > amount and positive loss", inflated)
	}
	spoof := seen[AttackPayeeSpoof]
	if !spoof.SpoofPayee || spoof.Loss <= 0 {
		t.Errorf("payee-spoof: %+v, want spoofed payee and positive loss", spoof)
	}
	repeat := seen[AttackRepeatPurchase]
	if repeat.Risk < 0.8 || repeat.Loss <= 0 {
		t.Errorf("repeat-purchase: %+v, want elevated risk and positive loss", repeat)
	}
}

// protectedConfig builds the mandate + table combination that defends against
// the catalog: a per-payment cap and payee allowlist (mandate), a risk ceiling
// (accept table), and an amount ceiling (grant table).
func protectedConfig(seed int64, payments int, attackMix float64) Config {
	chainID := params.AllDevChainProtocolChanges.ChainID
	acceptTable, _ := x402.LoadTablePolicy([]byte(`{"default":"reject","rules":[{"when":{"riskMax":0.8},"then":"approve"}]}`))
	grantTable, _ := x402.LoadTableGrant([]byte(`{"default":"refuse","rules":[{"when":{"amountMax":"2000"},"then":"pay"}]}`))
	return Config{
		Label:     "protected",
		Seed:      seed,
		Payments:  payments,
		AttackMix: attackMix,
		Accept:    x402.Chain{x402.AlwaysVerify{}, x402.NewMandatePolicy(chainID), acceptTable},
		Grant:     grantTable,
		Mandate: &x402.Mandate{
			MaxAmountPerPayment: big.NewInt(2000),
			ValidAfter:          big.NewInt(0),
			ValidBefore:         big.NewInt(1 << 40),
			BudgetAmount:        big.NewInt(1_000_000_000),
			MandateID:           [32]byte{0x33},
		},
	}
}

// Attacks make real losses against an unprotected policy, and the mandate+table
// combination cuts those losses. The report shows loss beside benign cost.
func TestAttacksLoseAndProtectionReduces(t *testing.T) {
	const seed, payments, mix = 5, 60, 0.5

	baseline, err := Run(Config{Label: "baseline", Seed: seed, Payments: payments, AttackMix: mix})
	if err != nil {
		t.Fatal(err)
	}
	protected, err := Run(protectedConfig(seed, payments, mix))
	if err != nil {
		t.Fatal(err)
	}

	if baseline.AttackTotal == 0 {
		t.Fatal("no attacks were generated")
	}
	if baseline.AttacksSettled == 0 || baseline.AttackLoss == 0 {
		t.Fatalf("baseline should lose to attacks: settled=%d loss=%d", baseline.AttacksSettled, baseline.AttackLoss)
	}
	if protected.AttackLoss >= baseline.AttackLoss {
		t.Fatalf("protection should cut losses: baseline=%d protected=%d", baseline.AttackLoss, protected.AttackLoss)
	}
	// Protection has a cost to benign work, which the report shows alongside.
	if protected.BenignCompleted > baseline.BenignCompleted {
		t.Fatalf("protected benign completion %d should not exceed baseline %d", protected.BenignCompleted, baseline.BenignCompleted)
	}
	t.Logf("\n%s", Render([]Report{baseline, protected}))
}
