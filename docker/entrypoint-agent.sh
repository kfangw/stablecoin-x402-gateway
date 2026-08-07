#!/bin/sh
# Register the agent in the identity registry, then read the token address
# published by init and pay for the protected resource.
set -eu

RPC="${RPC_URL:-http://anvil:8545}"
ADDR_FILE="${TOKEN_ADDR_FILE:-/shared/token.addr}"
REGISTRY_FILE="${REGISTRY_ADDR_FILE:-/shared/registry.addr}"

if [ ! -s "${ADDR_FILE}" ]; then
	echo "agent: token address file ${ADDR_FILE} not found; is the stack up?" >&2
	exit 1
fi

TOKEN="$(cat "${ADDR_FILE}")"

# The gateway rejects unregistered agents, so register first. Registration is a
# one-time setup transaction the agent sends and pays gas for; re-running it
# just updates the stored agent-card URL.
if [ -s "${REGISTRY_FILE}" ]; then
	REGISTRY="$(cat "${REGISTRY_FILE}")"
	echo "agent: registering in ${REGISTRY}"
	agent register --rpc "${RPC}" --registry "${REGISTRY}" --card "https://cards.example/demo-agent"
fi

exec agent get --rpc "${RPC}" --token "${TOKEN}" --max 1000 http://gateway:8402/premium/report
