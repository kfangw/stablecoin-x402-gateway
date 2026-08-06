#!/bin/sh
# Wait for the init service to publish the token address, then start the gateway
# in remote mode: it delegates verification and settlement to the facilitator
# and holds neither an RPC connection nor a key.
set -eu

ADDR_FILE="${TOKEN_ADDR_FILE:-/shared/token.addr}"
FACILITATOR_URL="${FACILITATOR_URL:-http://facilitator:8403}"
NETWORK="${NETWORK:-eip155:31337}"
PAYTO="${PAYTO:?PAYTO (payee address) is required}"

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
echo "gateway: starting against token ${TOKEN}, facilitator ${FACILITATOR_URL}"

# Optional durability: JOURNAL_PATH persists settlements, KAFKA_BROKERS publishes
# them through the outbox. Both stay off when their variables are unset.
set -- --token "${TOKEN}" --listen :8402 --price 500 \
	--facilitator-url "${FACILITATOR_URL}" --network "${NETWORK}" --pay-to "${PAYTO}"
if [ -n "${JOURNAL_PATH:-}" ]; then
	set -- "$@" --journal "${JOURNAL_PATH}"
fi
if [ -n "${KAFKA_BROKERS:-}" ]; then
	set -- "$@" --kafka-brokers "${KAFKA_BROKERS}"
fi
exec gateway "$@"
