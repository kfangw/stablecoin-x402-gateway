package main

import "testing"

// register must reject bad arguments before it dials a node, so these cases need
// no RPC endpoint. The on-chain path is covered by the e2e suite.
func TestRegisterArgValidation(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"missing registry", []string{"--card", "https://cards.example/a"}},
		{"invalid registry", []string{"--registry", "not-an-address", "--card", "https://cards.example/a"}},
		{"missing card", []string{"--registry", "0x0000000000000000000000000000000000000001"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := runRegister(tc.args); err == nil {
				t.Fatalf("runRegister(%v) = nil, want an error", tc.args)
			}
		})
	}
}
