#!/usr/bin/env bash
set -euo pipefail

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repo_root=$(CDPATH= cd -- "$script_dir/../.." && pwd)
input_file=${1:-"$script_dir/gateway.env"}
output_file=${2:-"$repo_root/.env"}

if [[ ! -f "$input_file" ]]; then
  printf 'missing input file: %s\n' "$input_file" >&2
  exit 1
fi
if [[ -e "$output_file" && "${FORCE:-false}" != "true" ]]; then
  printf 'refusing to overwrite %s; set FORCE=true only if replacement is intentional\n' \
    "$output_file" >&2
  exit 1
fi
if ! command -v openssl >/dev/null 2>&1; then
  printf 'openssl is required to generate benchmark-only secrets\n' >&2
  exit 1
fi

set -a
# shellcheck disable=SC1090
. "$input_file"
set +a

: "${UPSTREAM_MAIN_URL:?UPSTREAM_MAIN_URL is required}"
: "${UPSTREAM_FAIL_URL:?UPSTREAM_FAIL_URL is required}"
: "${UPSTREAM_FALLBACK_URL:?UPSTREAM_FALLBACK_URL is required}"
GATEWAY_BIND=${GATEWAY_BIND:-0.0.0.0}
GATEWAY_PORT=${GATEWAY_PORT:-8080}

for upstream_url in \
  "$UPSTREAM_MAIN_URL" \
  "$UPSTREAM_FAIL_URL" \
  "$UPSTREAM_FALLBACK_URL"; do
  if [[ ! "$upstream_url" =~ ^https?://[][A-Za-z0-9._:-]+$ ]]; then
    printf 'upstream URL must be an http(s) origin without path: %s\n' \
      "$upstream_url" >&2
    exit 1
  fi
done
if [[ ! "$GATEWAY_BIND" =~ ^[][A-Za-z0-9.:-]+$ ]]; then
  printf 'GATEWAY_BIND is invalid: %s\n' "$GATEWAY_BIND" >&2
  exit 1
fi
if [[ ! "$GATEWAY_PORT" =~ ^[0-9]+$ ]] ||
  ((GATEWAY_PORT < 1 || GATEWAY_PORT > 65535)); then
  printf 'GATEWAY_PORT must be from 1 to 65535\n' >&2
  exit 1
fi

postgres_password=$(openssl rand -hex 24)
redis_password=$(openssl rand -hex 24)
api_key_pepper=$(openssl rand -base64 48 | tr -d '\n')
admin_key_pepper=$(openssl rand -base64 48 | tr -d '\n')
control_master_key=$(openssl rand -base64 32 | tr -d '\n')
metrics_token=$(openssl rand -hex 32)

provider_keys_json='{"providers":[{"provider_id":"mock-main","keys":[{"id":"main-a","secret":"not-a-real-key-a"},{"id":"main-b","secret":"not-a-real-key-b"}]},{"provider_id":"mock-fail","keys":[{"id":"fail-a","secret":"not-a-real-key"}]},{"provider_id":"mock-fallback","keys":[{"id":"fallback-a","secret":"not-a-real-key"}]}]}'
routing_json=$(printf '{"providers":[{"id":"mock-main","type":"openai-compatible","vendor":"custom","base_url":"%s","models":["*"],"model_capabilities":{"*":["text"]}},{"id":"mock-fail","type":"openai-compatible","vendor":"custom","base_url":"%s","models":["*"],"model_capabilities":{"*":["text"]}},{"id":"mock-fallback","type":"openai-compatible","vendor":"custom","base_url":"%s","models":["*"],"model_capabilities":{"*":["text"]}}],"routes":[{"model":"mock/fallback","candidates":[{"provider":"mock-fail","upstream_model":"mock/instant"},{"provider":"mock-fallback","upstream_model":"mock/instant"}]},{"model":"*","candidates":[{"provider":"mock-main","upstream_model":""}]}]}' \
  "$UPSTREAM_MAIN_URL" \
  "$UPSTREAM_FAIL_URL" \
  "$UPSTREAM_FALLBACK_URL")

umask 077
{
  printf 'MODEL_VELO_HTTP_BIND=%s\n' "$GATEWAY_BIND"
  printf 'MODEL_VELO_HTTP_PORT=%s\n' "$GATEWAY_PORT"
  printf 'MODEL_VELO_HTTP_ADDR=:8080\n'
  printf 'MODEL_VELO_ENVIRONMENT=threehost\n'
  printf 'MODEL_VELO_SHUTDOWN_TIMEOUT=10s\n'
  printf '\n'
  printf 'MODEL_VELO_POSTGRES_DB=model_velo\n'
  printf 'MODEL_VELO_POSTGRES_USER=model_velo\n'
  printf 'MODEL_VELO_POSTGRES_PASSWORD=%s\n' "$postgres_password"
  printf 'MODEL_VELO_POSTGRES_MAX_OPEN_CONNS=50\n'
  printf 'MODEL_VELO_AUTH_CACHE_ENABLED=true\n'
  printf 'MODEL_VELO_AUTH_CACHE_L1_MAX_ENTRIES=10000\n'
  printf 'MODEL_VELO_AUTH_CACHE_L1_TTL=15s\n'
  printf 'MODEL_VELO_AUTH_CACHE_L2_TTL=30s\n'
  printf 'MODEL_VELO_AUTH_CACHE_KEY_PREFIX=model-velo:threehost:auth:v1\n'
  printf 'MODEL_VELO_AUTH_CACHE_INVALIDATION_CHANNEL=model-velo:threehost:auth:v1:invalidate\n'
  printf 'MODEL_VELO_POSTGRES_MAX_IDLE_CONNS=10\n'
  printf 'MODEL_VELO_API_KEY_PEPPER=%s\n' "$api_key_pepper"
  printf 'MODEL_VELO_ADMIN_KEY_PEPPER=%s\n' "$admin_key_pepper"
  printf 'MODEL_VELO_CONTROL_MASTER_KEY=%s\n' "$control_master_key"
  printf '\n'
  printf 'MODEL_VELO_REDIS_PASSWORD=%s\n' "$redis_password"
  printf 'MODEL_VELO_REDIS_DB=0\n'
  printf 'MODEL_VELO_REDIS_POOL_SIZE=100\n'
  printf 'MODEL_VELO_REDIS_MIN_IDLE_CONNS=10\n'
  printf 'MODEL_VELO_REDIS_STARTUP_POLICY=required\n'
  printf 'MODEL_VELO_RATE_LIMIT_REQUESTS=1000000\n'
  printf 'MODEL_VELO_RATE_LIMIT_WINDOW=1m\n'
  printf 'MODEL_VELO_RATE_LIMIT_FAILURE_POLICY=fail-closed\n'
  printf 'MODEL_VELO_CACHE_TTL=5m\n'
  printf 'MODEL_VELO_CACHE_ROUTE_VERSION=threehost-v1\n'
  printf '\n'
  printf 'MODEL_VELO_PROVIDER_KEYS_JSON=%s\n' "$provider_keys_json"
  printf 'MODEL_VELO_ROUTING_JSON=%s\n' "$routing_json"
  printf 'MODEL_VELO_BREAKER_FAILURE_THRESHOLD=5\n'
  printf 'MODEL_VELO_BREAKER_OPEN_DURATION=30s\n'
  printf 'MODEL_VELO_BREAKER_HALF_OPEN_PROBES=1\n'
  printf 'MODEL_VELO_QUEUE_MAX_IN_FLIGHT=256\n'
  printf 'MODEL_VELO_QUEUE_MAX_WAITING=2048\n'
  printf 'MODEL_VELO_QUEUE_WAIT_TIMEOUT=2s\n'
  printf 'MODEL_VELO_RETRY_MAX_ATTEMPTS=3\n'
  printf 'MODEL_VELO_RETRY_INITIAL_BACKOFF=100ms\n'
  printf 'MODEL_VELO_RETRY_MAX_BACKOFF=1s\n'
  printf 'MODEL_VELO_RETRY_BACKOFF_MULTIPLIER=2\n'
  printf 'MODEL_VELO_RETRY_JITTER_RATIO=0\n'
  printf 'MODEL_VELO_REQUEST_TIMEOUT=15s\n'
  printf 'MODEL_VELO_ATTEMPT_TIMEOUT=10s\n'
  printf '\n'
  printf 'MODEL_VELO_USAGE_ENFORCE_STREAM=true\n'
  printf 'MODEL_VELO_USAGE_PRICING_JSON=[]\n'
  printf 'MODEL_VELO_LOG_FORMAT=json\n'
  printf 'MODEL_VELO_LOG_LEVEL=info\n'
  printf 'MODEL_VELO_SERVICE_NAME=model-velo\n'
  printf 'MODEL_VELO_METRICS_TOKEN=%s\n' "$metrics_token"
  printf 'MODEL_VELO_WORKER_METRICS_ADDR=:9091\n'
  printf 'MODEL_VELO_OTEL_SAMPLE_RATIO=0\n'
} >"$output_file"
chmod 600 "$output_file"

printf 'wrote %s with mode 600\n' "$output_file"
