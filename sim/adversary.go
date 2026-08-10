package sim

import "math/rand"

// The attack catalog. Each attack is generated with the same amount and risk
// draw as a benign task, so a report shows loss reduction next to the cost to
// normal work. Three attacks:
//
//   - inflated terms: the server charges well above the agreed price.
//   - payee spoof: the payment is directed to an address outside the mandate.
//   - repeat purchase: an induced re-buy, carried here as an elevated risk
//     signal that a risk-aware policy can refuse.
//
// Loss is what the attack costs if it settles: the overcharge for inflated
// terms, the full amount for a spoof or an unwanted repeat.
func attackItem(r *rand.Rand, amount int64, risk float64) WorkItem {
	switch r.Intn(3) {
	case 0:
		served := amount * 3
		return WorkItem{
			Amount:       amount,
			ServedAmount: served,
			Risk:         risk,
			Kind:         AttackInflatedTerms,
			Loss:         served - amount,
		}
	case 1:
		return WorkItem{
			Amount:       amount,
			ServedAmount: amount,
			Risk:         risk,
			Kind:         AttackPayeeSpoof,
			SpoofPayee:   true,
			Loss:         amount,
		}
	default:
		return WorkItem{
			Amount:       amount,
			ServedAmount: amount,
			Risk:         0.9, // an induced repeat shows up as a high risk score
			Kind:         AttackRepeatPurchase,
			Loss:         amount,
		}
	}
}
