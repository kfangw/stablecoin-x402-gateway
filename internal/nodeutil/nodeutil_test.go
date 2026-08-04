package nodeutil

import (
	"context"
	"math/big"
	"strings"
	"testing"
)

func TestTransactorFromEnvMissing(t *testing.T) {
	// The variable is deliberately left unset.
	if _, _, err := TransactorFromEnv("NODEUTIL_TEST_KEY_MISSING", big.NewInt(1)); err == nil {
		t.Fatal("want error for unset environment variable")
	}
}

func TestTransactorFromEnvInvalidHex(t *testing.T) {
	t.Setenv("NODEUTIL_TEST_KEY", "not-a-valid-hex-key")
	if _, _, err := TransactorFromEnv("NODEUTIL_TEST_KEY", big.NewInt(1)); err == nil {
		t.Fatal("want error for invalid hex key")
	}
}

func TestTransactorFromEnvValid(t *testing.T) {
	// A well-known development key (anvil default account #0). 0x prefix is optional.
	const key = "0xac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80"
	const wantAddr = "0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266"

	t.Setenv("NODEUTIL_TEST_KEY", key)
	opts, addr, err := TransactorFromEnv("NODEUTIL_TEST_KEY", big.NewInt(31337))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if opts == nil || opts.From != addr {
		t.Fatalf("transactor From %s != derived address %s", opts.From, addr)
	}
	if !strings.EqualFold(addr.Hex(), wantAddr) {
		t.Fatalf("address = %s, want %s", addr.Hex(), wantAddr)
	}
}

func TestDialBadURL(t *testing.T) {
	// An unsupported scheme fails before any network I/O, so this needs no node.
	if _, _, err := Dial(context.Background(), "bogus://unreachable"); err == nil {
		t.Fatal("want error for unsupported RPC URL scheme")
	}
}
