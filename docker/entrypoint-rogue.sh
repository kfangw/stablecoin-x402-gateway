#!/bin/sh
# Read the token address published by init and try to pay WITHOUT registering
# first. The gateway's identity policy must refuse this agent, so the expected
# outcome is a 402: the demo succeeds when the payment is turned away.
set -eu

RPC="${RPC_URL:-http://anvil:8545}"
ADDR_FILE="${TOKEN_ADDR_FILE:-/shared/token.addr}"

if [ ! -s "${ADDR_FILE}" ]; then
	echo "rogue: token address file ${ADDR_FILE} not found; is the stack up?" >&2
	exit 1
fi

TOKEN="$(cat "${ADDR_FILE}")"
echo "rogue: attempting payment without registering (expecting a 402)"
if agent get --rpc "${RPC}" --token "${TOKEN}" --max 1000 http://gateway:8402/premium/report; then
	echo "rogue: unexpected success; an unregistered agent should have been refused" >&2
	exit 1
fi
echo "rogue: refused as expected (unregistered agent)"
