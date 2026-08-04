#!/bin/sh
# One-shot initialization: deploy the tKRW token, mint the agent's starting
# balance, then publish the token address to the shared volume so the gateway
# and agent services can pick it up.
set -eu

RPC="${RPC_URL:-http://anvil:8545}"
ADDR_FILE="${TOKEN_ADDR_FILE:-/shared/token.addr}"

echo "init: deploying tKRW on ${RPC}"
# issuer deploy prints the token address on its final stdout line.
TOKEN="$(issuer deploy --rpc "${RPC}" | tail -n 1)"
echo "init: token deployed at ${TOKEN}"

echo "init: minting 100000 tKRW to agent ${AGENT_ADDRESS}"
issuer mint --rpc "${RPC}" --token "${TOKEN}" --to "${AGENT_ADDRESS}" --amount 100000

printf '%s' "${TOKEN}" > "${ADDR_FILE}"
echo "init: wrote token address to ${ADDR_FILE}"
