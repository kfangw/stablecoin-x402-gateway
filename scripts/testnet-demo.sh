#!/usr/bin/env bash
# testnet-demo.sh runs the full scenario against a real RPC node using only
# environment-injected keys and addresses. No addresses are hardcoded: the ERC-
# 8004 identity registry is taken from IDENTITY_REGISTRY when set, and otherwise
# a local one is deployed. The flow is: deploy the contracts, back issuance with
# a reserve, provision eligibility and the asset, start the gateway, register the
# agent, sign a mandate, buy a resource (pay, deliver, receipt), audit the
# receipt offline and on chain, and reconcile with the reserve invariant.
#
# Required environment:
#   RPC_URL         RPC endpoint
#   ISSUER_KEY      issuer private key (0x hex)
#   GATEWAY_KEY     gateway/seller key;   GATEWAY_ADDR   its address
#   AGENT_KEY       agent key;            AGENT_ADDR     its address
#   DELEGATOR_KEY   delegator key;        DELEGATOR_ADDR its address
#   RECEIPT_KEY     receipt-signing key;  RECEIPT_ADDR   its address
# Optional:
#   IDENTITY_REGISTRY  a deployed ERC-8004 registry (self-deployed if unset)
set -euo pipefail

for v in RPC_URL ISSUER_KEY GATEWAY_KEY GATEWAY_ADDR AGENT_KEY AGENT_ADDR \
         DELEGATOR_KEY DELEGATOR_ADDR RECEIPT_KEY RECEIPT_ADDR; do
  if [ -z "${!v:-}" ]; then
    echo "error: environment variable $v is required" >&2
    exit 1
  fi
done

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"
WORK="$(mktemp -d)"
RESERVE="$WORK/reserve.jsonl"
JOURNAL="$WORK/journal.log"
MANDATE="$WORK/mandate.json"
RECEIPT="$WORK/receipt.json"
GATEWAY_URL="http://localhost:8402"

issuer() { ISSUER_KEY="$ISSUER_KEY" go run ./cmd/issuer "$@"; }

echo "== deploying contracts on $RPC_URL =="
TOKEN=$(issuer deploy --rpc "$RPC_URL" | tail -n1)
ELIG=$(issuer deploy-eligibility --rpc "$RPC_URL" | tail -n1)
ASSET=$(issuer deploy-asset --rpc "$RPC_URL" --registry "$ELIG" | tail -n1)
echo "token=$TOKEN eligibility=$ELIG asset=$ASSET"

if [ -n "${IDENTITY_REGISTRY:-}" ]; then
  REGISTRY="$IDENTITY_REGISTRY"
  echo "using injected identity registry $REGISTRY"
else
  REGISTRY=$(issuer deploy-registry --rpc "$RPC_URL" | tail -n1)
  echo "self-deployed identity registry $REGISTRY"
fi

echo "== reserve-backed issuance =="
issuer reserve-add --rpc "$RPC_URL" --reserve "$RESERVE" --amount 1000000 --reason "testnet backing"
issuer mint --rpc "$RPC_URL" --token "$TOKEN" --to "$AGENT_ADDR" --amount 100000 --reserve "$RESERVE"

echo "== eligibility and asset provisioning =="
issuer set-eligible --rpc "$RPC_URL" --registry "$ELIG" --account "$GATEWAY_ADDR"
issuer set-eligible --rpc "$RPC_URL" --registry "$ELIG" --account "$AGENT_ADDR"
issuer mint-asset --rpc "$RPC_URL" --asset "$ASSET" --to "$GATEWAY_ADDR" --amount 100

echo "== starting the gateway (local mode, two-transaction delivery, receipts) =="
GATEWAY_KEY="$GATEWAY_KEY" RECEIPT_KEY="$RECEIPT_KEY" go run ./cmd/gateway \
  --rpc "$RPC_URL" --token "$TOKEN" \
  --identity-registry "$REGISTRY" --eligibility-registry "$ELIG" \
  --asset "$ASSET" --receipt-key RECEIPT_KEY --journal "$JOURNAL" \
  --require-bound-nonce --listen :8402 &
GW_PID=$!
trap 'kill "$GW_PID" 2>/dev/null || true; rm -rf "$WORK"' EXIT

for _ in $(seq 1 30); do
  if curl -sf "$GATEWAY_URL/healthz" >/dev/null 2>&1; then break; fi
  sleep 1
done

echo "== register the agent =="
AGENT_KEY="$AGENT_KEY" go run ./cmd/agent register --rpc "$RPC_URL" \
  --registry "$REGISTRY" --card "https://cards.example/testnet-agent"

echo "== delegator signs a mandate =="
DELEGATOR_KEY="$DELEGATOR_KEY" go run ./cmd/delegator sign --rpc "$RPC_URL" \
  --agent "$AGENT_ADDR" --max-amount 1000 --payees "$GATEWAY_ADDR" --out "$MANDATE"

echo "== discover and buy (pays, delivers the asset, returns a signed receipt) =="
AGENT_KEY="$AGENT_KEY" go run ./cmd/agent discover "$GATEWAY_URL/resources"
AGENT_KEY="$AGENT_KEY" go run ./cmd/agent get --rpc "$RPC_URL" --token "$TOKEN" \
  --mandate "$MANDATE" --receipt-out "$RECEIPT" "$GATEWAY_URL/premium/report"

echo "== audit the receipt, offline checks plus on-chain settlement and delivery =="
go run ./cmd/audit --receipt "$RECEIPT" --mandate "$MANDATE" --journal "$JOURNAL" \
  --gateway "$RECEIPT_ADDR" --rpc "$RPC_URL"

echo "== reconcile the ledger against the chain, with the reserve invariant =="
issuer reconcile --rpc "$RPC_URL" --token "$TOKEN" --asset "$ASSET" --reserve "$RESERVE"

echo "testnet demo complete"
