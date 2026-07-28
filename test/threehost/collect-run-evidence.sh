#!/usr/bin/env bash
set -euo pipefail

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repo_root=$(CDPATH= cd -- "$script_dir/../.." && pwd)
env_file=${ENV_FILE:-"$repo_root/.env"}
run_id=${1:-}

if [[ ! "$run_id" =~ ^[A-Za-z0-9._-]+$ || ${#run_id} -gt 48 ]]; then
  printf 'usage: %s <RUN_ID>\n' "$0" >&2
  exit 1
fi
if [[ ! -f "$env_file" ]]; then
  printf 'environment file not found: %s\n' "$env_file" >&2
  exit 1
fi
for command_name in curl docker git; do
  if ! command -v "$command_name" >/dev/null 2>&1; then
    printf '%s is required\n' "$command_name" >&2
    exit 1
  fi
done

set -a
# shellcheck disable=SC1090
. "$env_file"
set +a

compose_file=${COMPOSE_FILE:-"$repo_root/compose.yaml"}
output_dir=${OUTPUT_DIR:-"$repo_root/test-results/threehost/$run_id/gateway-evidence"}
request_prefix="bench-$run_id"
request_prefix=${request_prefix:0:48}
postgres_user=${MODEL_VELO_POSTGRES_USER:-model_velo}
postgres_database=${MODEL_VELO_POSTGRES_DB:-model_velo}
environment=${MODEL_VELO_ENVIRONMENT:-development}
usage_stream="model-velo:usage:v1:$environment"
usage_group=${MODEL_VELO_USAGE_GROUP:-model-velo-usage-workers}
drain_timeout_seconds=${DRAIN_TIMEOUT_SECONDS:-240}

if [[ ! "$drain_timeout_seconds" =~ ^[1-9][0-9]*$ ]]; then
  printf 'DRAIN_TIMEOUT_SECONDS must be a positive integer\n' >&2
  exit 1
fi

if [[ -e "$output_dir" ]]; then
  printf 'refusing to overwrite evidence output directory: %s\n' "$output_dir" >&2
  exit 1
fi
mkdir -p "$output_dir"

{
  printf 'run_id=%s\n' "$run_id"
  printf 'request_prefix=%s\n' "$request_prefix"
  printf 'captured_at=%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  printf 'commit=%s\n' "$(git -C "$repo_root" rev-parse HEAD)"
  printf 'compose_file=%s\n' "$compose_file"
  printf 'host=%s\n' "$(uname -a)"
  if [[ -n "$(git -C "$repo_root" status --porcelain)" ]]; then
    printf 'worktree=dirty\n'
  else
    printf 'worktree=clean\n'
  fi
} >"$output_dir/evidence-metadata.txt"

safe_setting_names=(
  MODEL_VELO_ENVIRONMENT
  MODEL_VELO_POSTGRES_CONNECT_TIMEOUT
  MODEL_VELO_POSTGRES_MAX_OPEN_CONNS
  MODEL_VELO_POSTGRES_MAX_IDLE_CONNS
  MODEL_VELO_POSTGRES_MAX_CONN_IDLE_TIME
  MODEL_VELO_POSTGRES_MAX_CONN_LIFETIME
  MODEL_VELO_REDIS_DB
  MODEL_VELO_REDIS_POOL_SIZE
  MODEL_VELO_REDIS_MIN_IDLE_CONNS
  MODEL_VELO_REDIS_POOL_TIMEOUT
  MODEL_VELO_REDIS_DIAL_TIMEOUT
  MODEL_VELO_REDIS_READ_TIMEOUT
  MODEL_VELO_REDIS_WRITE_TIMEOUT
  MODEL_VELO_REDIS_STARTUP_POLICY
  MODEL_VELO_RATE_LIMIT_REQUESTS
  MODEL_VELO_RATE_LIMIT_WINDOW
  MODEL_VELO_RATE_LIMIT_FAILURE_POLICY
  MODEL_VELO_CACHE_TTL
  MODEL_VELO_CACHE_ROUTE_VERSION
  MODEL_VELO_BREAKER_FAILURE_THRESHOLD
  MODEL_VELO_BREAKER_OPEN_DURATION
  MODEL_VELO_BREAKER_HALF_OPEN_PROBES
  MODEL_VELO_QUEUE_MAX_IN_FLIGHT
  MODEL_VELO_QUEUE_MAX_WAITING
  MODEL_VELO_QUEUE_WAIT_TIMEOUT
  MODEL_VELO_RETRY_MAX_ATTEMPTS
  MODEL_VELO_RETRY_INITIAL_BACKOFF
  MODEL_VELO_RETRY_MAX_BACKOFF
  MODEL_VELO_RETRY_BACKOFF_MULTIPLIER
  MODEL_VELO_RETRY_JITTER_RATIO
  MODEL_VELO_REQUEST_TIMEOUT
  MODEL_VELO_ATTEMPT_TIMEOUT
  MODEL_VELO_USAGE_ENFORCE_STREAM
  MODEL_VELO_USAGE_EMIT_TIMEOUT
  MODEL_VELO_USAGE_GROUP
  MODEL_VELO_USAGE_CONSUMER
  MODEL_VELO_USAGE_BATCH_SIZE
  MODEL_VELO_USAGE_READ_BLOCK
  MODEL_VELO_USAGE_CLAIM_IDLE
  MODEL_VELO_USAGE_PENDING_TIMEOUT
  MODEL_VELO_USAGE_MAX_DELIVERIES
  MODEL_VELO_USAGE_RETRY_BACKOFF
  MODEL_VELO_USAGE_STREAM_MAX_LEN
  MODEL_VELO_USAGE_DEAD_LETTER_MAX_LEN
  MODEL_VELO_USAGE_WORKER_TIMEOUT
  MODEL_VELO_USAGE_MAINTENANCE_INTERVAL
  MODEL_VELO_USAGE_MAINTENANCE_BATCH_SIZE
  MODEL_VELO_LOG_LEVEL
  MODEL_VELO_OTEL_SAMPLE_RATIO
)
for setting_name in "${safe_setting_names[@]}"; do
  printf '%s=%s\n' "$setting_name" "${!setting_name:-}"
done >"$output_dir/runtime-settings.txt"

docker compose -f "$compose_file" ps --format json \
  >"$output_dir/compose-ps.json"
docker compose -f "$compose_file" images --format json \
  >"$output_dir/compose-images.json"
for service in gateway usage-worker postgres redis; do
  docker compose -f "$compose_file" logs \
    --no-color \
    --timestamps \
    --tail 10000 \
    "$service" \
    >"$output_dir/$service.log" 2>&1 || true
done

prefix_length=${#request_prefix}
redis_command() {
  docker compose -f "$compose_file" exec -T redis \
    sh -c \
    'REDISCLI_AUTH="$MODEL_VELO_REDIS_PASSWORD" redis-cli --no-auth-warning "$@"' \
    redis-cli "$@"
}

drain_deadline=$((SECONDS + drain_timeout_seconds))
drain_state=timeout
remaining_outbox=-1
stream_length=-1
while ((SECONDS < drain_deadline)); do
  remaining_outbox=$(docker compose -f "$compose_file" exec -T postgres \
    psql \
    -U "$postgres_user" \
    -d "$postgres_database" \
    -At \
    -c "SELECT count(*) FROM usage_outbox WHERE left(request_id, $prefix_length) = '$request_prefix';")
  stream_length=$(redis_command --raw XLEN "$usage_stream")
  if [[ "$remaining_outbox" == "0" ]]; then
    drain_state=complete
    break
  fi
  sleep 1
done
{
  printf 'state=%s\n' "$drain_state"
  printf 'timeout_seconds=%s\n' "$drain_timeout_seconds"
  printf 'remaining_outbox=%s\n' "$remaining_outbox"
  printf 'stream_length=%s\n' "$stream_length"
} >"$output_dir/usage-drain.txt"

docker compose -f "$compose_file" exec -T postgres \
  psql \
  -U "$postgres_user" \
  -d "$postgres_database" \
  --csv \
  -c "SELECT count(*) AS events, count(DISTINCT request_id) AS request_ids, min(started_at) AS first_started_at, max(ended_at) AS last_ended_at FROM usage_events WHERE left(request_id, $prefix_length) = '$request_prefix';" \
  >"$output_dir/usage-overview.csv"
docker compose -f "$compose_file" exec -T postgres \
  psql \
  -U "$postgres_user" \
  -d "$postgres_database" \
  --csv \
  -c "SELECT status, stream, count(*) AS events FROM usage_events WHERE left(request_id, $prefix_length) = '$request_prefix' GROUP BY status, stream ORDER BY status, stream;" \
  >"$output_dir/usage-status.csv"
docker compose -f "$compose_file" exec -T postgres \
  psql \
  -U "$postgres_user" \
  -d "$postgres_database" \
  --csv \
  -c "SELECT state, count(*) AS events FROM usage_outbox WHERE left(request_id, $prefix_length) = '$request_prefix' GROUP BY state ORDER BY state;" \
  >"$output_dir/usage-outbox.csv"
docker compose -f "$compose_file" exec -T postgres \
  psql \
  -U "$postgres_user" \
  -d "$postgres_database" \
  --csv \
  -c "SELECT requested_model, status, stream, cache_status, error_category, error_code, count(*) AS events, sum(attempts) AS attempts, sum(retries) AS retries, sum(fallbacks) AS fallbacks, round(avg(latency_ms)::numeric, 2) AS latency_avg_ms, percentile_cont(0.95) WITHIN GROUP (ORDER BY latency_ms) AS latency_p95_ms, percentile_cont(0.99) WITHIN GROUP (ORDER BY latency_ms) AS latency_p99_ms, round(avg(first_token_ms)::numeric, 2) AS first_token_avg_ms, percentile_cont(0.95) WITHIN GROUP (ORDER BY first_token_ms) AS first_token_p95_ms, percentile_cont(0.99) WITHIN GROUP (ORDER BY first_token_ms) AS first_token_p99_ms FROM usage_events WHERE left(request_id, $prefix_length) = '$request_prefix' GROUP BY requested_model, status, stream, cache_status, error_category, error_code ORDER BY requested_model, status, stream, cache_status, error_category, error_code;" \
  >"$output_dir/usage-diagnostics.csv"

{
  printf 'stream=%s\n' "$usage_stream"
  printf 'stream_length='
  redis_command XLEN "$usage_stream"
  printf 'dead_letter_length='
  redis_command XLEN "$usage_stream:dead-letter"
  printf 'group=%s\n' "$usage_group"
  printf 'pending=\n'
  redis_command XPENDING "$usage_stream" "$usage_group" || true
} >"$output_dir/redis-usage.txt"

bind_address=${MODEL_VELO_HTTP_BIND:-127.0.0.1}
if [[ "$bind_address" == "0.0.0.0" || "$bind_address" == "[::]" ]]; then
  bind_address=127.0.0.1
fi
metrics_token=${MODEL_VELO_METRICS_TOKEN:-}
declare -a curl_args=(--fail --silent --show-error --max-time 5)
if [[ -n "$metrics_token" ]]; then
  curl_args+=(-H "Authorization: Bearer $metrics_token")
fi
curl "${curl_args[@]}" \
  "http://$bind_address:${MODEL_VELO_HTTP_PORT:-8080}/metrics" \
  >"$output_dir/gateway-final.prom"
curl "${curl_args[@]}" \
  "http://127.0.0.1:${MODEL_VELO_WORKER_METRICS_PORT:-9091}/metrics" \
  >"$output_dir/worker-final.prom"

printf 'wrote evidence under %s\n' "$output_dir"
