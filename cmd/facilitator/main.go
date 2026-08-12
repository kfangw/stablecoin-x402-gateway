// Command facilitator exposes the x402 verification and settlement service
// over HTTP. It wraps a LocalFacilitator so a resource server (the gateway) can
// delegate both steps and never touch the chain itself. The endpoints follow
// the shape of the public x402 facilitator spec.
// Reference: https://github.com/coinbase/x402
//
// The settlement key moves here (FACILITATOR_KEY): this service, not the
// gateway, pays the gas for on-chain settlement.
package main

import (
	"context"
	"encoding/json"
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

	"github.com/kfangw/stablecoin-x402-gateway/dvp"
	"github.com/kfangw/stablecoin-x402-gateway/internal/nodeutil"
	"github.com/kfangw/stablecoin-x402-gateway/token"
	"github.com/kfangw/stablecoin-x402-gateway/x402"
)

const facilitatorKeyEnv = "FACILITATOR_KEY"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "facilitator:", err)
		os.Exit(1)
	}
}

func run() error {
	fs := flag.NewFlagSet("facilitator", flag.ExitOnError)
	rpc := fs.String("rpc", "http://localhost:8545", "RPC endpoint")
	tokenAddr := fs.String("token", "", "tKRW token address (required)")
	dvpAddr := fs.String("dvp", "", "DvP settlement contract address; when set, settle delivers the asset atomically")
	assetAmount := fs.Int64("asset-amount", 1, "units of the asset delivered per settlement in DvP mode")
	listen := fs.String("listen", ":8403", "listen address")
	fs.Parse(os.Args[1:])

	if *tokenAddr == "" || !common.IsHexAddress(*tokenAddr) {
		return fmt.Errorf("--token must be a valid address")
	}
	if *dvpAddr != "" && !common.IsHexAddress(*dvpAddr) {
		return fmt.Errorf("--dvp must be a valid address")
	}

	ctx := context.Background()
	client, chainID, err := nodeutil.Dial(ctx, *rpc)
	if err != nil {
		return err
	}
	defer client.Close()

	transactor, facAddr, err := nodeutil.TransactorFromEnv(facilitatorKeyEnv, chainID)
	if err != nil {
		return err
	}

	tok := token.Bind(common.HexToAddress(*tokenAddr), client)
	fac := &x402.LocalFacilitator{
		Token:      tok,
		Backend:    client,
		Transactor: transactor,
	}
	if *dvpAddr != "" {
		fac.DvP = dvp.Bind(common.HexToAddress(*dvpAddr), client)
		fac.AssetAmount = big.NewInt(*assetAmount)
		log.Printf("dvp settlement on: contract %s, asset amount %d", *dvpAddr, *assetAmount)
	}
	network := fmt.Sprintf("eip155:%s", chainID)

	mux := http.NewServeMux()
	mux.HandleFunc("/verify", verifyHandler(fac))
	mux.HandleFunc("/settle", settleHandler(fac))
	mux.HandleFunc("/supported", supportedHandler(network))

	server := &http.Server{Addr: *listen, Handler: mux}
	log.Printf("x402 facilitator on %s: token %s, settler %s, network %s",
		*listen, tok.Address.Hex(), facAddr.Hex(), network)

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
	return server.Shutdown(shutdownCtx)
}

func verifyHandler(fac *x402.LocalFacilitator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		req, err := decodeRequest(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		result, err := fac.Verify(r.Context(), req.PaymentPayload, req.PaymentRequirements)
		if err != nil {
			// Transport or internal failure: report 5xx, not an invalid payment.
			http.Error(w, fmt.Sprintf("verify: %v", err), http.StatusInternalServerError)
			return
		}
		log.Printf("verify: isValid=%v payer=%s reason=%q", result.IsValid, result.Payer, result.InvalidReason)
		writeJSON(w, result)
	}
}

func settleHandler(fac *x402.LocalFacilitator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		req, err := decodeRequest(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		resp, err := fac.Settle(r.Context(), req.PaymentPayload, req.PaymentRequirements)
		if err != nil {
			http.Error(w, fmt.Sprintf("settle: %v", err), http.StatusInternalServerError)
			return
		}
		log.Printf("settle: success=%v tx=%s payer=%s reason=%q", resp.Success, resp.Transaction, resp.Payer, resp.ErrorReason)
		writeJSON(w, resp)
	}
}

// supportedHandler advertises the scheme and network this facilitator serves,
// following the x402 discovery convention.
func supportedHandler(network string) http.HandlerFunc {
	type kind struct {
		Scheme  string `json:"scheme"`
		Network string `json:"network"`
	}
	body := map[string][]kind{"kinds": {{Scheme: x402.SchemeExact, Network: network}}}
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		writeJSON(w, body)
	}
}

func decodeRequest(r *http.Request) (*x402.FacilitatorRequest, error) {
	var req x402.FacilitatorRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return nil, fmt.Errorf("invalid request body: %w", err)
	}
	return &req, nil
}

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
