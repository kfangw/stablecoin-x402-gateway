package sim

import "testing"

// The wrong-answer rate realizes the configured error rate.
func TestResponderErrorRate(t *testing.T) {
	r := NewResponder(1, 0.30, 0, 0)
	wrong, total := 0, 5000
	for i := 0; i < total; i++ {
		should := i%2 == 0
		answer, responded := r.Answer(should)
		if !responded {
			t.Fatal("responder with no non-response rate must always answer")
		}
		if answer != should {
			wrong++
		}
	}
	got := float64(wrong) / float64(total)
	if got < 0.27 || got > 0.33 {
		t.Fatalf("wrong-answer rate = %.3f, want ~0.30", got)
	}
}

// The silence rate realizes the configured non-response rate.
func TestResponderNonResponse(t *testing.T) {
	r := NewResponder(2, 0, 0.20, 0)
	silent, total := 0, 5000
	for i := 0; i < total; i++ {
		if _, responded := r.Answer(true); !responded {
			silent++
		}
	}
	got := float64(silent) / float64(total)
	if got < 0.17 || got > 0.23 {
		t.Fatalf("non-response rate = %.3f, want ~0.20", got)
	}
}

// Fatigue raises the error rate as the questions accumulate: late answers are
// wrong more often than early ones.
func TestResponderFatigue(t *testing.T) {
	r := NewResponder(3, 0.0, 0, 0.001) // error rate grows 0.001 per question
	var earlyWrong, early, lateWrong, late int
	for i := 0; i < 600; i++ {
		answer, _ := r.Answer(true)
		switch {
		case i < 100:
			early++
			if !answer {
				earlyWrong++
			}
		case i >= 500:
			late++
			if !answer {
				lateWrong++
			}
		}
	}
	earlyRate := float64(earlyWrong) / float64(early)
	lateRate := float64(lateWrong) / float64(late)
	if lateRate <= earlyRate {
		t.Fatalf("fatigue should raise the error rate: early %.3f, late %.3f", earlyRate, lateRate)
	}
}
