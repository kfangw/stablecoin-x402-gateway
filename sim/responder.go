package sim

import "math/rand"

// Responder is a scripted stand-in for a delegator answering confirmation
// requests. It is a pure script with three failure modes: it sometimes gives no
// answer (silence, or a reply too late to matter), it sometimes answers wrongly
// (confirming what it should decline, or declining what it should confirm), and
// its error rate climbs as the questions pile up (fatigue). It is seeded, so a
// run reproduces exactly.
type Responder struct {
	errorRate       float64
	nonResponseRate float64
	fatigue         float64 // added to the error rate per question already asked
	rng             *rand.Rand
	asked           int
}

// NewResponder builds a responder from a seed and its rates.
func NewResponder(seed int64, errorRate, nonResponseRate, fatigue float64) *Responder {
	return &Responder{
		errorRate:       errorRate,
		nonResponseRate: nonResponseRate,
		fatigue:         fatigue,
		rng:             rand.New(rand.NewSource(seed)),
	}
}

// Answer decides whether to confirm a payment the delegator would ideally
// approve (shouldApprove is true for a benign task, false for an attack). It may
// give no answer, and it may answer wrongly, with the chance of a wrong answer
// growing as the delegator tires.
func (r *Responder) Answer(shouldApprove bool) (approve, responded bool) {
	r.asked++
	if r.rng.Float64() < r.nonResponseRate {
		return false, false
	}
	errRate := r.errorRate + r.fatigue*float64(r.asked-1)
	if errRate > 1 {
		errRate = 1
	}
	answer := shouldApprove
	if r.rng.Float64() < errRate {
		answer = !answer
	}
	return answer, true
}
