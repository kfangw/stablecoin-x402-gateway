package main

import "testing"

// sign and revoke must reject bad arguments before touching a key or a node, so
// these cases need neither. The signing path is exercised by the demo and e2e.
func TestSignArgValidation(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"missing agent", nil},
		{"invalid agent", []string{"--agent", "not-an-address"}},
		{"bad payee", []string{"--agent", "0x0000000000000000000000000000000000000001", "--payees", "nope"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := runSign(tc.args); err == nil {
				t.Fatalf("runSign(%v) = nil, want an error", tc.args)
			}
		})
	}
}

func TestRevokeArgValidation(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"missing gateway", nil},
		{"missing mandate", []string{"--gateway", "http://localhost:8402"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := runRevoke(tc.args); err == nil {
				t.Fatalf("runRevoke(%v) = nil, want an error", tc.args)
			}
		})
	}
}
