// Command gateway runs the x402 payment gateway as a standalone HTTP server
// against a real RPC node. It protects one JSON resource (/premium/report) and
// settles payments on-chain, paying gas from the GATEWAY_KEY account. The same
// x402.Gateway backs the simulated demo in cmd/demo; here Commit is nil, so
// bind.WaitMined polls the node for the settlement receipt.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"math/big"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ethereum/go-ethereum/common"

	"github.com/kfangw/stablecoin-x402-gateway/internal/nodeutil"
	"github.com/kfangw/stablecoin-x402-gateway/token"
	"github.com/kfangw/stablecoin-x402-gateway/x402"
)

const gatewayKeyEnv = "GATEWAY_KEY"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "gateway:", err)
		os.Exit(1)
	}
}

func run() error {
	fs := flag.NewFlagSet("gateway", flag.ExitOnError)
	rpc := fs.String("rpc", "http://localhost:8545", "RPC endpoint")
	tokenAddr := fs.String("token", "", "tKRW token address (required)")
	listen := fs.String("listen", ":8402", "listen address")
	price := fs.Int64("price", 500, "resource price in tKRW")
	payToFlag := fs.String("pay-to", "", "payee address (default: the GATEWAY_KEY address)")
	fs.Parse(os.Args[1:])

	if *tokenAddr == "" || !common.IsHexAddress(*tokenAddr) {
		return fmt.Errorf("--token must be a valid address")
	}
	if *price <= 0 {
		return fmt.Errorf("--price must be positive")
	}

	ctx := context.Background()
	client, chainID, err := nodeutil.Dial(ctx, *rpc)
	if err != nil {
		return err
	}
	defer client.Close()

	facilitator, gatewayAddr, err := nodeutil.TransactorFromEnv(gatewayKeyEnv, chainID)
	if err != nil {
		return err
	}

	payTo := gatewayAddr
	if *payToFlag != "" {
		if !common.IsHexAddress(*payToFlag) {
			return fmt.Errorf("--pay-to %q is not a valid address", *payToFlag)
		}
		payTo = common.HexToAddress(*payToFlag)
	}

	tok := token.Bind(common.HexToAddress(*tokenAddr), client)
	gw := &x402.Gateway{
		Token:       tok,
		Backend:     client,
		Facilitator: facilitator,
		PayTo:       payTo,
		Price:       big.NewInt(*price),
		Network:     fmt.Sprintf("eip155:%s", chainID),
		Commit:      nil, // real node: WaitMined polls for the receipt
	}

	resource := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"report":"market report body","paid":true}`)
	})
	mux := http.NewServeMux()
	mux.Handle("/premium/report", gw.Middleware(resource))

	server := &http.Server{Addr: *listen, Handler: mux}

	log.Printf("x402 gateway on %s: token %s, price %s tKRW, payTo %s, network %s",
		*listen, tok.Address.Hex(), gw.Price, payTo.Hex(), gw.Network)

	errCh := make(chan error, 1)
	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	select {
	case err := <-errCh:
		return fmt.Errorf("serve: %w", err)
	case sig := <-stop:
		log.Printf("received %s, shutting down", sig)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	return nil
}
