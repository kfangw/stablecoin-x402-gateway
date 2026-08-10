#!/bin/sh
# Sign a mandate that lets the agent pay the gateway's payee, and write it to the
# shared volume for the agent to attach. Signing is off-chain; the RPC is used
# only to read the chain id that binds the mandate.
set -eu

RPC="${RPC_URL:-http://anvil:8545}"
MANDATE_FILE="${MANDATE_FILE:-/shared/mandate.json}"
AGENT_ADDRESS="${AGENT_ADDRESS:?AGENT_ADDRESS (the mandated agent) is required}"
PAYTO="${PAYTO:?PAYTO (the allowed payee) is required}"

echo "delegator: signing a mandate for agent ${AGENT_ADDRESS}, payee ${PAYTO}"
delegator sign --rpc "${RPC}" \
	--agent "${AGENT_ADDRESS}" \
	--payees "${PAYTO}" \
	--max-amount 500 \
	--valid-for 3600 \
	--out "${MANDATE_FILE}"
echo "delegator: wrote mandate to ${MANDATE_FILE}"
