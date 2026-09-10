#!/usr/bin/env bash
set -euo pipefail

# Exercises the normal Compose environment as an external client would. It needs
# only Bash, curl and sed; no JSON parser, Go, Python or database client is required.

API_1="${API_1:-http://localhost:8080}"
API_2="${API_2:-http://localhost:8082}"
API_3="${API_3:-http://localhost:8083}"
KEYCLOAK_URL="${KEYCLOAK_URL:-http://localhost:8081}"

for command in curl sed; do
  command -v "$command" >/dev/null 2>&1 || {
    echo "Required command not found: $command" >&2
    exit 1
  }
done

uuid() {
  if command -v uuidgen >/dev/null 2>&1; then
    uuidgen | tr '[:upper:]' '[:lower:]'
  elif [[ -r /proc/sys/kernel/random/uuid ]]; then
    cat /proc/sys/kernel/random/uuid
  elif command -v openssl >/dev/null 2>&1; then
    openssl rand -hex 16 | sed -E 's/^(.{8})(.{4})(.{4})(.{4})(.{12})$/\1-\2-\3-\4-\5/'
  else
    echo "Could not generate a UUID; install uuidgen or openssl." >&2
    exit 1
  fi
}

json_string() {
  local field="$1"
  local value
  value="$(sed -nE "s/.*\"${field}\"[[:space:]]*:[[:space:]]*\"([^\"]+)\".*/\1/p" | head -n 1)"
  if [[ -z "$value" ]]; then
    echo "Response did not contain the JSON string field ${field}." >&2
    exit 1
  fi
  printf '%s' "$value"
}

json_boolean() {
  local field="$1"
  sed -nE "s/.*\"${field}\"[[:space:]]*:[[:space:]]*(true|false).*/\1/p" | head -n 1
}

token() {
  local client_id="$1"
  local client_secret="$2"
  local response
  response="$(curl --silent --show-error --fail \
    --user "${client_id}:${client_secret}" \
    --data 'grant_type=client_credentials' \
    "${KEYCLOAK_URL}/realms/gaming/protocol/openid-connect/token")"
  printf '%s' "$response" | json_string access_token
}

RESPONSE_BODY=''
RESPONSE_STATUS=''

api_call() {
  local method="$1"
  local url="$2"
  local access_token="$3"
  local payload="$4"
  shift 4

  local -a arguments=(--silent --show-error --request "$method" "$url" --write-out $'\n%{http_code}')
  if [[ -n "$access_token" ]]; then
    arguments+=(-H "Authorization: Bearer ${access_token}")
  fi
  if [[ -n "$payload" ]]; then
    arguments+=(-H 'Content-Type: application/json' --data "$payload")
  fi
  for header in "$@"; do
    arguments+=(-H "$header")
  done

  local response
  response="$(curl "${arguments[@]}")"
  RESPONSE_STATUS="${response##*$'\n'}"
  RESPONSE_BODY="${response%$'\n'*}"
}

expect_status() {
  local expected="$1"
  local label="$2"
  if [[ "$RESPONSE_STATUS" != "$expected" ]]; then
    echo "${label} returned HTTP ${RESPONSE_STATUS}; expected ${expected}." >&2
    echo "$RESPONSE_BODY" >&2
    exit 1
  fi
  printf '✓ %s\n' "$label"
}

expect_json_string() {
  local field="$1"
  local expected="$2"
  local label="$3"
  local actual
  actual="$(printf '%s' "$RESPONSE_BODY" | json_string "$field")"
  if [[ "$actual" != "$expected" ]]; then
    echo "${label}: field ${field} is ${actual}; expected ${expected}." >&2
    echo "$RESPONSE_BODY" >&2
    exit 1
  fi
}

echo 'Running the authenticated manual flow against the Docker Compose APIs...'

INTERNAL_TOKEN="$(token wallet-service wallet-service-local-secret)"
PROVIDER_A_TOKEN="$(token provider-a provider-a-local-secret)"
PROVIDER_B_TOKEN="$(token provider-b provider-b-local-secret)"
echo '✓ Keycloak issued service tokens'

api_call GET "${API_1}/health/live" '' ''
expect_status 200 'GET /health/live'
api_call GET "${API_2}/health/ready" '' ''
expect_status 200 'GET /health/ready'
api_call GET "${API_3}/metrics" '' ''
expect_status 200 'GET /metrics'

PLAYER_ID="$(uuid)"
CORRELATION_ID="manual-flow-$(uuid)"
api_call POST "${API_1}/wallets" "$INTERNAL_TOKEN" \
  "{\"playerId\":\"${PLAYER_ID}\",\"initialBalance\":{\"amount\":\"100.00\",\"currency\":\"BRL\"}}" \
  "X-Correlation-ID: ${CORRELATION_ID}"
expect_status 201 'POST /wallets'
WALLET_ID="$(printf '%s' "$RESPONSE_BODY" | json_string id)"
expect_json_string amount 100.00 'opening wallet balance'

api_call GET "${API_2}/wallets/${WALLET_ID}" "$INTERNAL_TOKEN" ''
expect_status 200 'GET /wallets/{walletId}'
api_call GET "${API_3}/wallets/${WALLET_ID}/ledger?limit=100" "$INTERNAL_TOKEN" ''
expect_status 200 'GET /wallets/{walletId}/ledger'

ROUND_ID="manual-round-$(uuid)"
BET_EXTERNAL_ID="manual-bet-$(uuid)"
BET_PAYLOAD="{\"providerId\":\"provider-a\",\"externalTransactionId\":\"${BET_EXTERNAL_ID}\",\"playerId\":\"${PLAYER_ID}\",\"walletId\":\"${WALLET_ID}\",\"roundId\":\"${ROUND_ID}\",\"gameId\":\"fortune-chimp\",\"kind\":\"BET\",\"money\":{\"amount\":\"25.00\",\"currency\":\"BRL\"}}"
BET_KEY="provider-a:${BET_EXTERNAL_ID}"
api_call POST "${API_2}/wagering/transactions" "$PROVIDER_A_TOKEN" "$BET_PAYLOAD" \
  "Idempotency-Key: ${BET_KEY}" "X-Correlation-ID: manual-bet-$(uuid)"
expect_status 200 'POST BET'
BET_TRANSACTION_ID="$(printf '%s' "$RESPONSE_BODY" | json_string transactionId)"
expect_json_string status PROCESSED 'BET status'
expect_json_string amount 75.00 'BET balance'

api_call POST "${API_3}/wagering/transactions" "$PROVIDER_A_TOKEN" "$BET_PAYLOAD" \
  "Idempotency-Key: ${BET_KEY}" "X-Correlation-ID: manual-bet-replay-$(uuid)"
expect_status 200 'POST BET replay'
if [[ "$(printf '%s' "$RESPONSE_BODY" | json_boolean idempotentReplay)" != true ]]; then
  echo "BET replay did not return idempotentReplay=true." >&2
  echo "$RESPONSE_BODY" >&2
  exit 1
fi
echo '✓ BET replay is idempotent'

WIN_EXTERNAL_ID="manual-win-$(uuid)"
api_call POST "${API_1}/wagering/transactions" "$PROVIDER_A_TOKEN" \
  "{\"providerId\":\"provider-a\",\"externalTransactionId\":\"${WIN_EXTERNAL_ID}\",\"playerId\":\"${PLAYER_ID}\",\"walletId\":\"${WALLET_ID}\",\"roundId\":\"${ROUND_ID}\",\"gameId\":\"fortune-chimp\",\"kind\":\"WIN\",\"money\":{\"amount\":\"40.00\",\"currency\":\"BRL\"},\"referenceExternalTransactionId\":\"${BET_EXTERNAL_ID}\"}" \
  "Idempotency-Key: provider-a:${WIN_EXTERNAL_ID}" "X-Correlation-ID: manual-win-$(uuid)"
expect_status 200 'POST WIN'
WIN_TRANSACTION_ID="$(printf '%s' "$RESPONSE_BODY" | json_string transactionId)"
expect_json_string status PROCESSED 'WIN status'
expect_json_string amount 115.00 'WIN balance'

api_call GET "${API_2}/wagering/transactions/${WIN_TRANSACTION_ID}" "$INTERNAL_TOKEN" ''
expect_status 200 'GET /wagering/transactions/{transactionId}'
api_call GET "${API_3}/providers/provider-a/wagering/transactions/${WIN_EXTERNAL_ID}" "$PROVIDER_A_TOKEN" ''
expect_status 200 'GET provider transaction'
api_call GET "${API_1}/providers/provider-a/wagering/transactions/${WIN_EXTERNAL_ID}" "$PROVIDER_B_TOKEN" ''
expect_status 403 'provider B isolation'

api_call GET "${API_2}/wallets/${WALLET_ID}/ledger?limit=100" "$INTERNAL_TOKEN" ''
expect_status 200 'GET ledger after BET and WIN'
for amount in 100.00 25.00 40.00; do
  if [[ "$RESPONSE_BODY" != *"\"amount\":\"${amount}\""* ]]; then
    echo "Ledger does not contain the expected ${amount} BRL entry." >&2
    echo "$RESPONSE_BODY" >&2
    exit 1
  fi
done
echo '✓ Ledger contains opening, debit and credit entries'

api_call POST "${API_3}/wallets/${WALLET_ID}/reconciliation" "$INTERNAL_TOKEN" '' \
  "X-Correlation-ID: manual-reconciliation-$(uuid)"
expect_status 200 'POST /wallets/{walletId}/reconciliation'
if [[ "$(printf '%s' "$RESPONSE_BODY" | json_boolean consistent)" != true ]]; then
  echo "Wallet reconciliation is inconsistent." >&2
  echo "$RESPONSE_BODY" >&2
  exit 1
fi

printf '\nManual flow succeeded.\nWallet: %s\nBET transaction: %s\nWIN transaction: %s\nFinal balance: 115.00 BRL\n' \
  "$WALLET_ID" "$BET_TRANSACTION_ID" "$WIN_TRANSACTION_ID"
