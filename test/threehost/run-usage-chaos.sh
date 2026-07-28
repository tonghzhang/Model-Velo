#!/usr/bin/env bash
set -euo pipefail

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repo_root=$(CDPATH= cd -- "$script_dir/../.." && pwd)
run_id=${1:-}
env_file=${ENV_FILE:-"$repo_root/.env"}
compose_file=${COMPOSE_FILE:-"$repo_root/compose.yaml"}
output_dir=${OUTPUT_DIR:-"$repo_root/test-results/threehost/$run_id/usage-chaos"}

if [[ ! "$run_id" =~ ^[A-Za-z0-9._-]+$ || ${#run_id} -gt 48 ]]; then
  printf 'usage: MODEL_VELO_BENCH_API_KEY=... %s <RUN_ID>\n' "$0" >&2
  exit 1
fi
MODEL_VELO_BENCH_API_KEY=${MODEL_VELO_BENCH_API_KEY:-}
if [[ -z "$MODEL_VELO_BENCH_API_KEY" && -t 0 ]]; then
  read -r -s -p 'Benchmark API key: ' MODEL_VELO_BENCH_API_KEY
  printf '\n' >&2
fi
: "${MODEL_VELO_BENCH_API_KEY:?MODEL_VELO_BENCH_API_KEY is required}"
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
if [[ -e "$output_dir" ]]; then
  printf 'refusing to overwrite usage chaos output: %s\n' "$output_dir" >&2
  exit 1
fi

set -a
# shellcheck disable=SC1090
. "$env_file"
set +a

worker_outage_requests=${WORKER_OUTAGE_REQUESTS:-500}
redis_outage_requests=${REDIS_OUTAGE_REQUESTS:-20}
request_concurrency=${REQUEST_CONCURRENCY:-20}
recovery_timeout_seconds=${RECOVERY_TIMEOUT_SECONDS:-180}
gateway_url=${GATEWAY_LOCAL_URL:-http://127.0.0.1:${MODEL_VELO_HTTP_PORT:-8080}}
postgres_user=${MODEL_VELO_POSTGRES_USER:-model_velo}
postgres_database=${MODEL_VELO_POSTGRES_DB:-model_velo}
environment=${MODEL_VELO_ENVIRONMENT:-development}
usage_stream="model-velo:usage:v1:$environment"
usage_group=${MODEL_VELO_USAGE_GROUP:-model-velo-usage-workers}
prefix="usage-chaos-$run_id"
prefix=${prefix:0:64}

for integer_name in \
  WORKER_OUTAGE_REQUESTS \
  REDIS_OUTAGE_REQUESTS \
  REQUEST_CONCURRENCY \
  RECOVERY_TIMEOUT_SECONDS; do
  value=${!integer_name}
  if [[ ! "$value" =~ ^[1-9][0-9]*$ ]]; then
    printf '%s must be a positive integer\n' "$integer_name" >&2
    exit 1
  fi
done

mkdir -p "$output_dir"
services_restored=false
restore_services() {
  if [[ "$services_restored" == "true" ]]; then
    return
  fi
  docker compose -f "$compose_file" start redis gateway usage-worker \
    >"$output_dir/restore.log" 2>&1 || true
  services_restored=true
}
trap restore_services EXIT

sql_scalar() {
  local query=$1
  docker compose -f "$compose_file" exec -T postgres \
    psql -U "$postgres_user" -d "$postgres_database" -At -c "$query"
}

redis_command() {
  docker compose -f "$compose_file" exec -T redis \
    sh -c \
    'REDISCLI_AUTH="$MODEL_VELO_REDIS_PASSWORD" redis-cli --no-auth-warning "$@"' \
    redis-cli "$@"
}

event_count() {
  local request_prefix=$1
  local prefix_length=${#request_prefix}
  sql_scalar \
    "SELECT count(*) FROM usage_events WHERE left(request_id, $prefix_length) = '$request_prefix';"
}

outbox_count() {
  local request_prefix=$1
  local prefix_length=${#request_prefix}
  sql_scalar \
    "SELECT count(*) FROM usage_outbox WHERE left(request_id, $prefix_length) = '$request_prefix';"
}

wait_for_value() {
  local description=$1
  local expected=$2
  shift 2
  local deadline=$((SECONDS + recovery_timeout_seconds))
  local actual=
  while ((SECONDS < deadline)); do
    actual=$("$@" 2>/dev/null || true)
    if [[ "$actual" == "$expected" ]]; then
      printf '%s\n' "$actual"
      return
    fi
    sleep 1
  done
  printf 'timed out waiting for %s: got %s, want %s\n' \
    "$description" "${actual:-unavailable}" "$expected" >&2
  return 1
}

wait_for_redis() {
  local deadline=$((SECONDS + recovery_timeout_seconds))
  while ((SECONDS < deadline)); do
    if [[ "$(redis_command --raw PING 2>/dev/null || true)" == "PONG" ]]; then
      return
    fi
    sleep 1
  done
  printf 'timed out waiting for Redis recovery\n' >&2
  return 1
}

send_requests() {
  local phase=$1
  local count=$2
  local status_file="$output_dir/$phase-statuses.txt"
  : >"$status_file"
  local active=0
  local index
  for ((index = 1; index <= count; index++)); do
    (
      curl \
        --silent \
        --show-error \
        --max-time 20 \
        -o /dev/null \
        -w '%{http_code}\n' \
        -H "Authorization: Bearer $MODEL_VELO_BENCH_API_KEY" \
        -H 'Content-Type: application/json' \
        -H 'Cache-Control: no-store' \
        -H "X-Request-ID: $prefix-$phase-$index" \
        -d '{"model":"mock/instant","messages":[{"role":"user","content":"usage chaos"}]}' \
        "${gateway_url%/}/v1/chat/completions" \
        >>"$status_file" 2>>"$output_dir/$phase-curl-errors.log" || printf '000\n' >>"$status_file"
    ) &
    active=$((active + 1))
    if ((active >= request_concurrency)); then
      wait
      active=0
    fi
  done
  wait
  sort "$status_file" | uniq -c >"$output_dir/$phase-status-counts.txt"
}

{
  printf 'run_id=%s\n' "$run_id"
  printf 'request_prefix=%s\n' "$prefix"
  printf 'commit=%s\n' "$(git -C "$repo_root" rev-parse HEAD)"
  printf 'started_at=%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  printf 'worker_outage_requests=%s\n' "$worker_outage_requests"
  printf 'redis_outage_requests=%s\n' "$redis_outage_requests"
  printf 'request_concurrency=%s\n' "$request_concurrency"
} >"$output_dir/metadata.txt"

worker_prefix="$prefix-worker"
docker compose -f "$compose_file" stop usage-worker \
  >"$output_dir/worker-stop.log" 2>&1
send_requests worker "$worker_outage_requests"
worker_backlog=$(outbox_count "$worker_prefix")
docker compose -f "$compose_file" start usage-worker \
  >"$output_dir/worker-start.log" 2>&1
wait_for_value \
  "worker-outage events" \
  "$worker_outage_requests" \
  event_count "$worker_prefix" \
  >"$output_dir/worker-recovered-events.txt"
wait_for_value "worker-outage outbox drain" 0 outbox_count "$worker_prefix" \
  >"$output_dir/worker-final-outbox.txt"

redis_prefix="$prefix-redis"
docker compose -f "$compose_file" stop redis \
  >"$output_dir/redis-stop.log" 2>&1
send_requests redis "$redis_outage_requests"
redis_backlog=$(outbox_count "$redis_prefix")
docker compose -f "$compose_file" start redis \
  >"$output_dir/redis-start.log" 2>&1
wait_for_redis
wait_for_value \
  "Redis-outage events" \
  "$redis_outage_requests" \
  event_count "$redis_prefix" \
  >"$output_dir/redis-recovered-events.txt"
wait_for_value "Redis-outage outbox drain" 0 outbox_count "$redis_prefix" \
  >"$output_dir/redis-final-outbox.txt"

duplicate_request_id="$prefix-duplicate-1"
docker compose -f "$compose_file" stop usage-worker \
  >"$output_dir/duplicate-worker-stop.log" 2>&1
send_requests duplicate 1
wait_for_value "duplicate test ready outbox" 1 outbox_count "$prefix-duplicate" \
  >"$output_dir/duplicate-ready-outbox.txt"
duplicate_event_id=$(sql_scalar \
  "SELECT event_id FROM usage_outbox WHERE request_id = '$duplicate_request_id';")
duplicate_payload=$(sql_scalar \
  "SELECT payload::text FROM usage_outbox WHERE request_id = '$duplicate_request_id';")
for _ in 1 2; do
  printf '%s' "$duplicate_payload" |
    docker compose -f "$compose_file" exec -T redis \
      sh -c \
      'REDISCLI_AUTH="$MODEL_VELO_REDIS_PASSWORD" redis-cli --no-auth-warning -x XADD "$1" "*" event_id "$2" schema_version 2 payload' \
      redis-cli "$usage_stream" "$duplicate_event_id" \
      >>"$output_dir/duplicate-xadd-ids.txt"
done
docker compose -f "$compose_file" start usage-worker \
  >"$output_dir/duplicate-worker-start.log" 2>&1
wait_for_value \
  "idempotent duplicate storage" \
  1 \
  sql_scalar "SELECT count(*) FROM usage_events WHERE event_id = '$duplicate_event_id';" \
  >"$output_dir/duplicate-stored-events.txt"
wait_for_value "duplicate outbox drain" 0 outbox_count "$prefix-duplicate" \
  >"$output_dir/duplicate-final-outbox.txt"
wait_for_value "usage stream drain" 0 redis_command --raw XLEN "$usage_stream" \
  >"$output_dir/pre-poison-stream-length.txt"

dead_letter_before=$(redis_command --raw XLEN "$usage_stream:dead-letter")
docker compose -f "$compose_file" stop usage-worker \
  >"$output_dir/poison-worker-stop.log" 2>&1
poison_entry_id=$(redis_command --raw XADD "$usage_stream" '*' payload '{')
printf '%s\n' "$poison_entry_id" >"$output_dir/poison-entry-id.txt"
redis_command XREADGROUP \
  GROUP "$usage_group" usage-chaos \
  COUNT 1 STREAMS "$usage_stream" '>' \
  >"$output_dir/poison-initial-delivery.txt"
for _ in 1 2 3 4 5; do
  redis_command XCLAIM \
    "$usage_stream" "$usage_group" usage-chaos 0 "$poison_entry_id" JUSTID \
    >>"$output_dir/poison-claims.txt"
done
redis_command XCLAIM \
  "$usage_stream" "$usage_group" usage-chaos 0 "$poison_entry_id" \
  IDLE 31000 JUSTID \
  >>"$output_dir/poison-claims.txt"
docker compose -f "$compose_file" start usage-worker \
  >"$output_dir/poison-worker-start.log" 2>&1
dead_letter_after=$((dead_letter_before + 1))
wait_for_value \
  "dead-letter growth" \
  "$dead_letter_after" \
  redis_command --raw XLEN "$usage_stream:dead-letter" \
  >"$output_dir/dead-letter-final-length.txt"

{
  printf 'phase,requests,outbox_while_dependency_down,stored_after_recovery\n'
  printf 'worker_outage,%s,%s,%s\n' \
    "$worker_outage_requests" \
    "$worker_backlog" \
    "$(event_count "$worker_prefix")"
  printf 'redis_outage,%s,%s,%s\n' \
    "$redis_outage_requests" \
    "$redis_backlog" \
    "$(event_count "$redis_prefix")"
  printf 'duplicate_delivery,3,0,%s\n' \
    "$(sql_scalar "SELECT count(*) FROM usage_events WHERE event_id = '$duplicate_event_id';")"
  printf 'poison_dead_letter,1,0,%s\n' "$((dead_letter_after - dead_letter_before))"
} >"$output_dir/summary.csv"

printf 'ended_at=%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  >>"$output_dir/metadata.txt"
docker compose -f "$compose_file" ps --format json >"$output_dir/compose-ps-final.json"
services_restored=true
trap - EXIT
printf 'usage chaos evidence: %s\n' "$output_dir"
