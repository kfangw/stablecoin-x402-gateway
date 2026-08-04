#!/bin/sh
# Wait for the init service to publish the token address, then start the gateway.
set -eu

RPC="${RPC_URL:-http://anvil:8545}"
ADDR_FILE="${TOKEN_ADDR_FILE:-/shared/token.addr}"

i=0
while [ ! -s "${ADDR_FILE}" ]; do
	i=$((i + 1))
	if [ "${i}" -gt 60 ]; then
		echo "gateway: timed out waiting for ${ADDR_FILE}" >&2
		exit 1
	fi
	sleep 1
done

TOKEN="$(cat "${ADDR_FILE}")"
echo "gateway: starting against token ${TOKEN}"
exec gateway --rpc "${RPC}" --token "${TOKEN}" --listen :8402 --price 500
