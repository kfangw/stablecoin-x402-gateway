#!/bin/sh
# Read the token address published by init and pay for the protected resource.
set -eu

RPC="${RPC_URL:-http://anvil:8545}"
ADDR_FILE="${TOKEN_ADDR_FILE:-/shared/token.addr}"

if [ ! -s "${ADDR_FILE}" ]; then
	echo "agent: token address file ${ADDR_FILE} not found; is the stack up?" >&2
	exit 1
fi

TOKEN="$(cat "${ADDR_FILE}")"
exec agent --rpc "${RPC}" --token "${TOKEN}" --max 1000 http://gateway:8402/premium/report
