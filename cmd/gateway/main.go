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
	rpc := fs.String("rpc", "http://localhost:8545", "RPC endpoint (local mode)")
	tokenAddr := fs.String("token", "", "tKRW token address (required)")
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
		cleanup func()
		err     error
	)
	if *facilitatorURL != "" {
		gw, err = remoteGateway(*facilitatorURL, *tokenAddr, *network, *payToFlag, *price)
	} else {
		gw, cleanup, err = localGateway(*rpc, *tokenAddr, *payToFlag, *price)
	}
	if err != nil {
		return err
	}
	if cleanup != nil {
		defer cleanup()
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
	mux := http.NewServeMux()
	mux.Handle("/premium/report", gw.Middleware(resource))

	server := &http.Server{Addr: *listen, Handler: mux}
	mode := "local"
	if *facilitatorURL != "" {
		mode = "remote facilitator " + *facilitatorURL
	}
	log.Printf("x402 gateway on %s (%s): token %s, price %s tKRW, payTo %s, network %s",
		*listen, mode, gw.Token.Address.Hex(), gw.Price, gw.PayTo.Hex(), gw.Network)

	return serve(server)
}

// localGateway dials the node, reads the chain ID, and settles on-chain itself.
func localGateway(rpc, tokenAddr, payToFlag string, price int64) (*x402.Gateway, func(), error) {
	ctx := context.Background()
	client, chainID, err := nodeutil.Dial(ctx, rpc)
	if err != nil {
		return nil, nil, err
	}

	transactor, gatewayAddr, err := nodeutil.TransactorFromEnv(gatewayKeyEnv, chainID)
	if err != nil {
		client.Close()
		return nil, nil, err
	}

	payTo := gatewayAddr
	if payToFlag != "" {
		if !common.IsHexAddress(payToFlag) {
			client.Close()
			return nil, nil, fmt.Errorf("--pay-to %q is not a valid address", payToFlag)
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
	return gw, func() { client.Close() }, nil
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
