// Command gateway runs the x402 payment gateway as a standalone HTTP server.
// It protects one JSON resource (/premium/report) and delegates verification
// and settlement to a facilitator.
//
// In the default (local) mode it dials an RPC node and settles on-chain itself,
// paying gas from the GATEWAY_KEY account. With --facilitator-url it delegates
// to a remote facilitator and needs neither an RPC connection nor a key; it
// still builds the 402 payment terms, so --token, --network and --pay-to are
// required in that mode.
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
	"strings"
	"syscall"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"

	"github.com/kfangw/stablecoin-x402-gateway/asset"
	"github.com/kfangw/stablecoin-x402-gateway/dvp"
	"github.com/kfangw/stablecoin-x402-gateway/eligibility"
	"github.com/kfangw/stablecoin-x402-gateway/internal/nodeutil"
	"github.com/kfangw/stablecoin-x402-gateway/registry"
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
	rpc := fs.String("rpc", "http://localhost:8545", "RPC endpoint: local mode settlement, or read-only identity lookups in remote mode")
	tokenAddr := fs.String("token", "", "tKRW token address (required)")
	identityRegistry := fs.String("identity-registry", "", "identity registry address; when set, unregistered agents are rejected")
	eligibilityRegistry := fs.String("eligibility-registry", "", "eligibility registry address; when set, ineligible payers are rejected before settlement")
	requireMandate := fs.Bool("require-mandate", false, "require a delegator-signed mandate with each payment")
	askOnExceed := fs.Bool("ask-on-exceed", false, "answer over-limit payments with confirmation_required instead of rejecting (requires --require-mandate)")
	deferAbove := fs.Int64("defer-above", 0, "defer delivery for payments at or above this amount until confirmed (requires --require-mandate)")
	confirmDepth := fs.Uint64("confirm-depth", 0, "blocks a deferred settlement must be deep before delivery")
	acceptTable := fs.String("accept-table", "", "accept decision-table JSON file to add to the policy chain")
	assetAddr := fs.String("asset", "", "RWA asset address; when set, the gateway delivers the asset after settlement (two-transaction flow, local mode)")
	assetAmount := fs.Int64("asset-amount", 1, "units of the asset delivered per payment")
	dvpAddr := fs.String("dvp", "", "DvP settlement contract address; when set, payment and delivery settle atomically (local mode)")
	sessions := fs.Bool("sessions", false, "enable payment sessions: one authorization covering many requests, settled at close (local mode)")
	requireBoundNonce := fs.Bool("require-bound-nonce", false, "reject payments whose authorization nonce is not bound to the resource")
	listen := fs.String("listen", ":8402", "listen address")
	price := fs.Int64("price", 500, "resource price in tKRW")
	payToFlag := fs.String("pay-to", "", "payee address (default: the GATEWAY_KEY address; required with --facilitator-url)")
	facilitatorURL := fs.String("facilitator-url", "", "delegate verify/settle to this facilitator (no RPC or key needed)")
	network := fs.String("network", "", "x402 network string, e.g. eip155:31337 (required with --facilitator-url)")
	journalPath := fs.String("journal", "", "durable settlement journal file (default: disabled, settlements kept in memory)")
	kafkaBrokers := fs.String("kafka-brokers", "", "comma-separated Kafka brokers to publish settlements to (requires --journal)")
	kafkaTopic := fs.String("kafka-topic", "settlements", "Kafka topic for settlement events")
	fs.Parse(os.Args[1:])

	if *tokenAddr == "" || !common.IsHexAddress(*tokenAddr) {
		return fmt.Errorf("--token must be a valid address")
	}
	if *price <= 0 {
		return fmt.Errorf("--price must be positive")
	}
	if *kafkaBrokers != "" && *journalPath == "" {
		return fmt.Errorf("--kafka-brokers requires --journal (the outbox publishes from the journal)")
	}

	var (
		gw      *x402.Gateway
		client  *ethclient.Client // set in local mode; nil with a remote facilitator
		cleanup func()
		err     error
	)
	if *facilitatorURL != "" {
		gw, err = remoteGateway(*facilitatorURL, *tokenAddr, *network, *payToFlag, *price)
	} else {
		gw, client, cleanup, err = localGateway(*rpc, *tokenAddr, *payToFlag, *price)
	}
	if err != nil {
		return err
	}
	if cleanup != nil {
		defer cleanup()
	}
	gw.RequireBoundNonce = *requireBoundNonce

	// Optional identity policy: reject payments from unregistered agents. The
	// lookup is read-only, so the remote-mode gateway stays keyless; it only
	// gains a read-only RPC connection for the registry.
	if *identityRegistry != "" {
		stop, err := attachIdentity(gw, client, *identityRegistry, *rpc)
		if err != nil {
			return err
		}
		if stop != nil {
			defer stop()
		}
	}

	// Optional eligibility policy: reject payers that are not eligible to receive
	// the asset, before settlement. It stacks after any identity policy and before
	// the mandate policy. The lookup is read-only, so a remote-mode gateway stays
	// keyless.
	if *eligibilityRegistry != "" {
		stop, err := attachEligibility(gw, client, *eligibilityRegistry, *rpc)
		if err != nil {
			return err
		}
		if stop != nil {
			defer stop()
		}
	}

	// Optional mandate policy: require a delegator-signed mandate with each
	// payment. It stacks after any identity policy in the chain.
	if *requireMandate {
		if err := attachMandate(gw, *askOnExceed, *deferAbove); err != nil {
			return err
		}
		gw.ConfirmDepth = *confirmDepth
	} else if *askOnExceed || *deferAbove > 0 {
		return fmt.Errorf("--ask-on-exceed and --defer-above require --require-mandate")
	}

	// Optional asset delivery. Either the two-transaction flow (--asset) or the
	// atomic DvP flow (--dvp), never both. Both need chain access, so they are
	// local-mode only; a remote facilitator does its own delivery.
	if *assetAddr != "" && *dvpAddr != "" {
		return fmt.Errorf("--asset and --dvp are mutually exclusive")
	}
	if (*assetAddr != "" || *dvpAddr != "") && *facilitatorURL != "" {
		return fmt.Errorf("--asset and --dvp are local-mode only; configure delivery on the facilitator service in remote mode")
	}
	if *dvpAddr != "" {
		if err := attachDvP(gw, client, *dvpAddr, *assetAmount); err != nil {
			return err
		}
	} else if *assetAddr != "" {
		if err := attachDelivery(gw, client, *assetAddr, *assetAmount); err != nil {
			return err
		}
	}

	// Optional payment sessions. They settle the whole budget at close and refund
	// the unused part, so they need the gateway key to move funds: local mode only.
	if *sessions {
		if err := attachSessions(gw, client); err != nil {
			return err
		}
	}

	// Optional accept decision table, appended to the policy chain.
	if *acceptTable != "" {
		if err := attachAcceptTable(gw, *acceptTable); err != nil {
			return err
		}
	}

	// Optional durability: journal every settlement before answering, and (if
	// brokers are given) publish from the journal through an outbox. Both are
	// off by default, so the demo and existing runs are unchanged.
	if *journalPath != "" {
		stopOutbox, err := attachJournal(gw, *journalPath, *kafkaBrokers, *kafkaTopic)
		if err != nil {
			return err
		}
		defer stopOutbox()
	}

	resource := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"report":"market report body","paid":true}`)
	})
	gw.ResourcePath = "/premium/report"
	mux := http.NewServeMux()
	mux.Handle("/premium/report", gw.Middleware(resource))
	mux.HandleFunc("/resources", gw.DiscoveryHandler())
	if *requireMandate {
		mux.HandleFunc("/mandates/revoke", revokeHandler(gw))
	}

	server := &http.Server{Addr: *listen, Handler: mux}
	mode := "local"
	if *facilitatorURL != "" {
		mode = "remote facilitator " + *facilitatorURL
	}
	log.Printf("x402 gateway on %s (%s): token %s, price %s tKRW, payTo %s, network %s",
		*listen, mode, gw.Token.Address.Hex(), gw.Price, gw.PayTo.Hex(), gw.Network)

	return serve(server)
}

// revokeHandler accepts a delegator's signed mandate revocation. On a valid
// signature it records the revocation and answers 204; a bad body or signature
// is a 400.
func revokeHandler(gw *x402.Gateway) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var rev x402.RevocationJSON
		if err := json.NewDecoder(r.Body).Decode(&rev); err != nil {
			http.Error(w, "invalid revocation body", http.StatusBadRequest)
			return
		}
		if err := gw.RevokeMandate(rev); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// localGateway dials the node, reads the chain ID, and settles on-chain itself.
// It returns the client so the caller can reuse it for read-only lookups.
func localGateway(rpc, tokenAddr, payToFlag string, price int64) (*x402.Gateway, *ethclient.Client, func(), error) {
	ctx := context.Background()
	client, chainID, err := nodeutil.Dial(ctx, rpc)
	if err != nil {
		return nil, nil, nil, err
	}

	transactor, gatewayAddr, err := nodeutil.TransactorFromEnv(gatewayKeyEnv, chainID)
	if err != nil {
		client.Close()
		return nil, nil, nil, err
	}

	payTo := gatewayAddr
	if payToFlag != "" {
		if !common.IsHexAddress(payToFlag) {
			client.Close()
			return nil, nil, nil, fmt.Errorf("--pay-to %q is not a valid address", payToFlag)
		}
		payTo = common.HexToAddress(payToFlag)
	}

	gw := &x402.Gateway{
		Token:      token.Bind(common.HexToAddress(tokenAddr), client),
		Backend:    client,
		Transactor: transactor,
		PayTo:      payTo,
		Price:      big.NewInt(price),
		Network:    fmt.Sprintf("eip155:%s", chainID),
		Commit:     nil, // real node: WaitMined polls for the receipt
	}
	return gw, client, func() { client.Close() }, nil
}

// attachIdentity stacks an identity policy after the default verify policy, so
// unregistered agents are rejected. In local mode it reuses the settlement
// client; in remote mode (client nil) it dials the RPC read-only for lookups
// and returns a stop function to close that connection. The gateway still holds
// no key in remote mode.
func attachIdentity(gw *x402.Gateway, client *ethclient.Client, registryAddr, rpc string) (func(), error) {
	if !common.IsHexAddress(registryAddr) {
		return nil, fmt.Errorf("--identity-registry must be a valid address")
	}
	var (
		backend bind.ContractBackend = client
		stop    func()
	)
	if client == nil {
		roClient, _, err := nodeutil.Dial(context.Background(), rpc)
		if err != nil {
			return nil, fmt.Errorf("identity lookups need a reachable --rpc: %w", err)
		}
		backend = roClient
		stop = func() { roClient.Close() }
	}
	reg := registry.Bind(common.HexToAddress(registryAddr), backend)
	gw.Policy = x402.Chain{x402.AlwaysVerify{}, x402.IdentityPolicy{Registry: reg}}
	log.Printf("identity policy on: registry %s", registryAddr)
	return stop, nil
}

// attachEligibility stacks an eligibility policy after the current chain, so
// ineligible payers are rejected before settlement. In local mode it reuses the
// settlement client; in remote mode (client nil) it dials the RPC read-only for
// lookups and returns a stop function to close that connection.
func attachEligibility(gw *x402.Gateway, client *ethclient.Client, registryAddr, rpc string) (func(), error) {
	if !common.IsHexAddress(registryAddr) {
		return nil, fmt.Errorf("--eligibility-registry must be a valid address")
	}
	var (
		backend bind.ContractBackend = client
		stop    func()
	)
	if client == nil {
		roClient, _, err := nodeutil.Dial(context.Background(), rpc)
		if err != nil {
			return nil, fmt.Errorf("eligibility lookups need a reachable --rpc: %w", err)
		}
		backend = roClient
		stop = func() { roClient.Close() }
	}
	reg := eligibility.Bind(common.HexToAddress(registryAddr), backend)
	base := x402.Chain{x402.AlwaysVerify{}}
	if c, ok := gw.Policy.(x402.Chain); ok {
		base = c
	}
	gw.Policy = append(base, x402.EligibilityPolicy{Registry: reg})
	log.Printf("eligibility policy on: registry %s", registryAddr)
	return stop, nil
}

// attachDelivery sets the two-transaction asset deliverer: after settlement the
// gateway transfers the asset from its own (payee) account to the payer. It
// requires the gateway key, so it is local-mode only.
func attachDelivery(gw *x402.Gateway, client *ethclient.Client, assetAddr string, amount int64) error {
	if client == nil {
		return fmt.Errorf("--asset requires local mode (no --facilitator-url)")
	}
	if !common.IsHexAddress(assetAddr) {
		return fmt.Errorf("--asset must be a valid address")
	}
	gw.Deliverer = &x402.AssetDeliverer{
		Asset:      asset.Bind(common.HexToAddress(assetAddr), client),
		Transactor: gw.Transactor,
		Amount:     big.NewInt(amount),
		Backend:    client,
	}
	log.Printf("asset delivery on: asset %s, amount %d (two-transaction flow)", assetAddr, amount)
	return nil
}

// attachDvP switches the gateway to atomic delivery-versus-payment: a single
// settleAndDeliver transaction covers payment and delivery. It replaces the
// gateway's facilitator, so the separate deliverer is not used.
func attachDvP(gw *x402.Gateway, client *ethclient.Client, dvpAddr string, amount int64) error {
	if client == nil {
		return fmt.Errorf("--dvp requires local mode (no --facilitator-url)")
	}
	if !common.IsHexAddress(dvpAddr) {
		return fmt.Errorf("--dvp must be a valid address")
	}
	d := dvp.Bind(common.HexToAddress(dvpAddr), client)
	gw.DvPAddress = d.Address
	gw.Facilitator = &x402.LocalFacilitator{
		Token:       gw.Token,
		Backend:     client,
		Transactor:  gw.Transactor,
		DvP:         d,
		AssetAmount: big.NewInt(amount),
	}
	log.Printf("dvp settlement on: contract %s, asset amount %d (atomic flow, receive-authorization)", dvpAddr, amount)
	return nil
}

// attachSessions turns on payment sessions and builds the refunder that returns
// the unused budget at close. It reuses the gateway's own key and token binding,
// so it requires local mode.
func attachSessions(gw *x402.Gateway, client *ethclient.Client) error {
	if client == nil {
		return fmt.Errorf("--sessions requires local mode (no --facilitator-url)")
	}
	gw.SessionsEnabled = true
	if gw.Refunder == nil {
		gw.Refunder = &x402.Refunder{Token: gw.Token, Transactor: gw.Transactor, Backend: client}
	}
	log.Printf("payment sessions on: budget settled at close, unused amount refunded")
	return nil
}

// attachMandate appends a mandate policy to the gateway's chain, so a payment
// must carry a delegator-signed mandate scoped to its chain id.
func attachMandate(gw *x402.Gateway, askOnExceed bool, deferAbove int64) error {
	chainID, err := chainIDFromNetwork(gw.Network)
	if err != nil {
		return err
	}
	base := x402.Chain{x402.AlwaysVerify{}}
	if c, ok := gw.Policy.(x402.Chain); ok {
		base = c
	}
	mp := x402.NewMandatePolicy(chainID)
	mp.AskOnExceed = askOnExceed
	if deferAbove > 0 {
		mp.DeferAbove = big.NewInt(deferAbove)
	}
	gw.Policy = append(base, mp)
	log.Printf("mandate policy on: chain id %s, ask-on-exceed %v, defer-above %d", chainID, askOnExceed, deferAbove)
	return nil
}

// attachAcceptTable loads an accept decision table and appends it to the chain.
func attachAcceptTable(gw *x402.Gateway, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read accept table: %w", err)
	}
	tp, err := x402.LoadTablePolicy(data)
	if err != nil {
		return err
	}
	base := x402.Chain{x402.AlwaysVerify{}}
	if c, ok := gw.Policy.(x402.Chain); ok {
		base = c
	}
	gw.Policy = append(base, tp)
	log.Printf("accept table on: %s", path)
	return nil
}

// chainIDFromNetwork parses the numeric chain id from an eip155:<id> network.
func chainIDFromNetwork(network string) (*big.Int, error) {
	_, id, ok := strings.Cut(network, ":")
	if !ok {
		return nil, fmt.Errorf("cannot read chain id from network %q", network)
	}
	chainID, ok := new(big.Int).SetString(id, 10)
	if !ok {
		return nil, fmt.Errorf("cannot read chain id from network %q", network)
	}
	return chainID, nil
}

// remoteGateway delegates to a facilitator and touches neither RPC nor a key.
// The payment terms still need the token address, network and payee.
func remoteGateway(facilitatorURL, tokenAddr, network, payToFlag string, price int64) (*x402.Gateway, error) {
	if network == "" {
		return nil, fmt.Errorf("--network is required with --facilitator-url")
	}
	if payToFlag == "" || !common.IsHexAddress(payToFlag) {
		return nil, fmt.Errorf("--pay-to must be a valid address with --facilitator-url")
	}
	gw := &x402.Gateway{
		Token:       token.At(common.HexToAddress(tokenAddr)),
		PayTo:       common.HexToAddress(payToFlag),
		Price:       big.NewInt(price),
		Network:     network,
		Facilitator: &x402.RemoteFacilitator{BaseURL: facilitatorURL},
	}
	return gw, nil
}

// attachJournal opens the settlement journal, restores the gateway's
// settlements from it, and, when Kafka brokers are configured, starts an outbox
// that publishes journaled settlements. It returns a stop function that cancels
// the outbox and closes the sink and journal.
func attachJournal(gw *x402.Gateway, path, brokers, topic string) (func(), error) {
	journal, err := x402.Open(path)
	if err != nil {
		return nil, err
	}
	gw.AttachJournal(journal)

	if brokers == "" {
		log.Printf("settlement journal at %s (publishing disabled)", path)
		return func() { journal.Close() }, nil
	}

	sink, err := x402.NewKafkaSink(strings.Split(brokers, ","), topic)
	if err != nil {
		journal.Close()
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		(&x402.Outbox{Journal: journal, Sink: sink}).Run(ctx)
		close(done)
	}()
	log.Printf("settlement journal at %s, publishing to Kafka %s topic %q", path, brokers, topic)
	return func() {
		cancel()
		<-done
		sink.Close()
		journal.Close()
	}, nil
}

func serve(server *http.Server) error {
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
