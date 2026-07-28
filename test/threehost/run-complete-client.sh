#!/usr/bin/env bash
set -euo pipefail

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repo_root=$(CDPATH= cd -- "$script_dir/../.." && pwd)
env_file=${1:-"$script_dir/benchmark.env"}

if [[ "$env_file" != "-" && ! -f "$env_file" ]]; then
  printf 'missing benchmark environment file: %s\n' "$env_file" >&2
  exit 1
fi
for command_name in curl git k6 python3; do
  if ! command -v "$command_name" >/dev/null 2>&1; then
    printf '%s is required on the client host\n' "$command_name" >&2
    exit 1
  fi
done

set -a
if [[ "$env_file" != "-" ]]; then
  # shellcheck disable=SC1090
  . "$env_file"
fi
set +a

: "${GATEWAY_URL:?GATEWAY_URL is required}"
: "${UPSTREAM_URL:?UPSTREAM_URL is required}"
: "${MODEL_VELO_API_KEY:?MODEL_VELO_API_KEY is required}"
UPSTREAM_FAIL_URL=${UPSTREAM_FAIL_URL:-}
UPSTREAM_FALLBACK_URL=${UPSTREAM_FALLBACK_URL:-}

REQUEST_TIMEOUT=${REQUEST_TIMEOUT:-30s}
COOLDOWN_SECONDS=${COOLDOWN_SECONDS:-3}
REPETITIONS=${REPETITIONS:-3}
WARMUP_RATE=${WARMUP_RATE:-50}
WARMUP_DURATION=${WARMUP_DURATION:-10s}
CAPACITY_VUS=${CAPACITY_VUS:-"1 2 4 8 16 32 64 128 256"}
CAPACITY_DURATION=${CAPACITY_DURATION:-20s}
RATE_SWEEP=${RATE_SWEEP:-"100 250 500 750 1000 1500 2000"}
RATE_DURATION=${RATE_DURATION:-30s}
PRE_ALLOCATED_VUS=${PRE_ALLOCATED_VUS:-256}
MAX_VUS=${MAX_VUS:-2048}
STREAM_REQUESTS=${STREAM_REQUESTS:-500}
STREAM_CONCURRENCY=${STREAM_CONCURRENCY:-20}
STREAM_MODEL=${STREAM_MODEL:-mock/typical}
STREAM_PROMPT_BYTES=${STREAM_PROMPT_BYTES:-200}
PAYLOAD_RATE=${PAYLOAD_RATE:-100}
PAYLOAD_DURATION=${PAYLOAD_DURATION:-30s}
CACHE_RATE=${CACHE_RATE:-500}
CACHE_DURATION=${CACHE_DURATION:-30s}
RAMP_STAGES=${RAMP_STAGES:-"50:1m,250:2m,500:3m,750:2m,250:1m"}
BURST_STAGES=${BURST_STAGES:-"250:2m,1000:1m,250:2m"}
PROFILE_PRE_ALLOCATED_VUS=${PROFILE_PRE_ALLOCATED_VUS:-512}
PROFILE_MAX_VUS=${PROFILE_MAX_VUS:-2048}
FAULT_RATE=${FAULT_RATE:-100}
FAULT_DURATION=${FAULT_DURATION:-2m}
FAULT_RECOVERY_RATE=${FAULT_RECOVERY_RATE:-100}
FAULT_RECOVERY_FAILURE_DURATION=${FAULT_RECOVERY_FAILURE_DURATION:-45s}
FAULT_RECOVERY_HEALTHY_DURATION=${FAULT_RECOVERY_HEALTHY_DURATION:-45s}
QUEUE_RATE=${QUEUE_RATE:-1000}
QUEUE_DURATION=${QUEUE_DURATION:-2m}
QUEUE_PRE_ALLOCATED_VUS=${QUEUE_PRE_ALLOCATED_VUS:-2048}
QUEUE_MAX_VUS=${QUEUE_MAX_VUS:-4096}
ENDURANCE_RATE=${ENDURANCE_RATE:-500}
ENDURANCE_DURATION=${ENDURANCE_DURATION:-30m}
ENDURANCE_PRE_ALLOCATED_VUS=${ENDURANCE_PRE_ALLOCATED_VUS:-512}
ENDURANCE_MAX_VUS=${ENDURANCE_MAX_VUS:-2048}
RATE_LIMIT_TEST_RATE=${RATE_LIMIT_TEST_RATE:-100}
RATE_LIMIT_TEST_DURATION=${RATE_LIMIT_TEST_DURATION:-2m}
RESULTS_ROOT=${RESULTS_ROOT:-"$repo_root/test-results/threehost"}
SAVE_RAW_METRICS=${SAVE_RAW_METRICS:-false}
TEST_PROFILE=${TEST_PROFILE:-complete}

RUN_CAPACITY=${RUN_CAPACITY:-true}
RUN_WARMUP=${RUN_WARMUP:-true}
RUN_RATE_SWEEP=${RUN_RATE_SWEEP:-true}
RUN_STREAM_DETAIL=${RUN_STREAM_DETAIL:-true}
RUN_PAYLOAD=${RUN_PAYLOAD:-true}
RUN_CACHE=${RUN_CACHE:-true}
RUN_RAMP=${RUN_RAMP:-true}
RUN_BURST=${RUN_BURST:-true}
RUN_FAULT=${RUN_FAULT:-true}
RUN_FAULT_RECOVERY=${RUN_FAULT_RECOVERY:-true}
RUN_QUEUE_OVERLOAD=${RUN_QUEUE_OVERLOAD:-true}
RUN_ENDURANCE=${RUN_ENDURANCE:-true}
RUN_RELIABILITY=${RUN_RELIABILITY:-true}
RUN_RATE_LIMIT=${RUN_RATE_LIMIT:-false}

for boolean_name in \
  RUN_CAPACITY \
  RUN_WARMUP \
  RUN_RATE_SWEEP \
  RUN_STREAM_DETAIL \
  RUN_PAYLOAD \
  RUN_CACHE \
  RUN_RAMP \
  RUN_BURST \
  RUN_FAULT \
  RUN_FAULT_RECOVERY \
  RUN_QUEUE_OVERLOAD \
  RUN_ENDURANCE \
  RUN_RELIABILITY \
  RUN_RATE_LIMIT \
  SAVE_RAW_METRICS; do
  value=${!boolean_name}
  if [[ "$value" != "true" && "$value" != "false" ]]; then
    printf '%s must be true or false\n' "$boolean_name" >&2
    exit 1
  fi
done
if [[ "$RUN_STREAM_DETAIL" == "true" ]] &&
  ! command -v go >/dev/null 2>&1; then
  printf 'go is required when RUN_STREAM_DETAIL=true\n' >&2
  exit 1
fi
for integer_name in \
  COOLDOWN_SECONDS \
  REPETITIONS \
  WARMUP_RATE \
  PRE_ALLOCATED_VUS \
  MAX_VUS \
  STREAM_REQUESTS \
  STREAM_CONCURRENCY \
  STREAM_PROMPT_BYTES \
  PAYLOAD_RATE \
  CACHE_RATE \
  PROFILE_PRE_ALLOCATED_VUS \
  PROFILE_MAX_VUS \
  FAULT_RATE \
  FAULT_RECOVERY_RATE \
  QUEUE_RATE \
  QUEUE_PRE_ALLOCATED_VUS \
  QUEUE_MAX_VUS \
  ENDURANCE_RATE \
  ENDURANCE_PRE_ALLOCATED_VUS \
  ENDURANCE_MAX_VUS \
  RATE_LIMIT_TEST_RATE; do
  value=${!integer_name}
  if [[ ! "$value" =~ ^[1-9][0-9]*$ ]] &&
    [[ "$integer_name" != "COOLDOWN_SECONDS" || ! "$value" =~ ^[0-9]+$ ]]; then
    printf '%s must be a positive integer\n' "$integer_name" >&2
    exit 1
  fi
done
if [[ "$MAX_VUS" -lt "$PRE_ALLOCATED_VUS" ]]; then
  printf 'MAX_VUS must be greater than or equal to PRE_ALLOCATED_VUS\n' >&2
  exit 1
fi
if [[ "$PROFILE_MAX_VUS" -lt "$PROFILE_PRE_ALLOCATED_VUS" ]]; then
  printf 'PROFILE_MAX_VUS must be greater than or equal to PROFILE_PRE_ALLOCATED_VUS\n' >&2
  exit 1
fi
if [[ "$ENDURANCE_MAX_VUS" -lt "$ENDURANCE_PRE_ALLOCATED_VUS" ]]; then
  printf 'ENDURANCE_MAX_VUS must be greater than or equal to ENDURANCE_PRE_ALLOCATED_VUS\n' >&2
  exit 1
fi
if [[ "$QUEUE_MAX_VUS" -lt "$QUEUE_PRE_ALLOCATED_VUS" ]]; then
  printf 'QUEUE_MAX_VUS must be greater than or equal to QUEUE_PRE_ALLOCATED_VUS\n' >&2
  exit 1
fi
if [[ "$RUN_FAULT_RECOVERY" == "true" ]] &&
  { [[ -z "$UPSTREAM_FAIL_URL" ]] || [[ -z "$UPSTREAM_FALLBACK_URL" ]]; }; then
  printf 'UPSTREAM_FAIL_URL and UPSTREAM_FALLBACK_URL are required when RUN_FAULT_RECOVERY=true\n' >&2
  exit 1
fi
for value in $CAPACITY_VUS $RATE_SWEEP; do
  if [[ ! "$value" =~ ^[1-9][0-9]*$ ]]; then
    printf 'capacity and rate sweep lists must contain positive integers\n' >&2
    exit 1
  fi
done

run_id=${RUN_ID:-$(date -u +%Y%m%dT%H%M%SZ)}
if [[ ! "$run_id" =~ ^[A-Za-z0-9._-]+$ || ${#run_id} -gt 48 ]]; then
  printf 'RUN_ID must contain at most 48 letters, digits, dots, dashes, or underscores\n' >&2
  exit 1
fi
results_dir="$RESULTS_ROOT/$run_id"
if [[ -e "$results_dir" ]]; then
  printf 'refusing to overwrite existing results directory: %s\n' "$results_dir" >&2
  exit 1
fi
mkdir -p "$results_dir"
printf 'case\tphase\ttarget\tmode\tload\tmodel\tstream\tprompt_bytes\tcache_mode\trepetition\texit_code\tstarted_at\tended_at\n' \
  >"$results_dir/cases.tsv"

request_prefix="bench-$run_id"
request_prefix=${request_prefix:0:48}
temporary_dir=$(mktemp -d)
streamload_bin="$temporary_dir/model-velo-streamload"
client_stats_pid=
stop_client_stats() {
  if [[ -z "$client_stats_pid" ]]; then
    return
  fi
  kill "$client_stats_pid" >/dev/null 2>&1 || true
  wait "$client_stats_pid" >/dev/null 2>&1 || true
  client_stats_pid=
}
cleanup() {
  stop_client_stats
  rm -rf -- "$temporary_dir"
}
summarize() {
  python3 "$script_dir/summarize.py" "$results_dir" || true
}
trap 'cleanup; summarize' EXIT

{
  printf 'run_id=%s\n' "$run_id"
  printf 'test_profile=%s\n' "$TEST_PROFILE"
  printf 'request_prefix=%s\n' "$request_prefix"
  printf 'commit=%s\n' "$(git -C "$repo_root" rev-parse HEAD)"
  printf 'started_at=%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  printf 'gateway_url=%s\n' "$GATEWAY_URL"
  printf 'upstream_url=%s\n' "$UPSTREAM_URL"
  printf 'upstream_fail_url=%s\n' "$UPSTREAM_FAIL_URL"
  printf 'upstream_fallback_url=%s\n' "$UPSTREAM_FALLBACK_URL"
  printf 'repetitions=%s\n' "$REPETITIONS"
  printf 'capacity_vus=%s\n' "$CAPACITY_VUS"
  printf 'rate_sweep=%s\n' "$RATE_SWEEP"
  printf 'stream_requests=%s\n' "$STREAM_REQUESTS"
  printf 'stream_concurrency=%s\n' "$STREAM_CONCURRENCY"
  printf 'ramp_stages=%s\n' "$RAMP_STAGES"
  printf 'burst_stages=%s\n' "$BURST_STAGES"
  printf 'queue_rate=%s\n' "$QUEUE_RATE"
  printf 'fault_recovery_rate=%s\n' "$FAULT_RECOVERY_RATE"
  printf 'fault_recovery_failure_duration=%s\n' "$FAULT_RECOVERY_FAILURE_DURATION"
  printf 'fault_recovery_healthy_duration=%s\n' "$FAULT_RECOVERY_HEALTHY_DURATION"
  printf 'queue_pre_allocated_vus=%s\n' "$QUEUE_PRE_ALLOCATED_VUS"
  printf 'queue_max_vus=%s\n' "$QUEUE_MAX_VUS"
  printf 'endurance_rate=%s\n' "$ENDURANCE_RATE"
  printf 'endurance_duration=%s\n' "$ENDURANCE_DURATION"
  printf 'k6_version=%s\n' "$(k6 version | tr '\n' ' ')"
  printf 'python_version=%s\n' "$(python3 --version 2>&1)"
  printf 'host=%s\n' "$(uname -a)"
  if [[ -n "$(git -C "$repo_root" status --porcelain)" ]]; then
    printf 'worktree=dirty\n'
  else
    printf 'worktree=clean\n'
  fi
  if [[ -f /etc/os-release ]]; then
    printf '\n[os-release]\n'
    cat /etc/os-release
  fi
  if command -v lscpu >/dev/null 2>&1; then
    printf '\n[cpu]\n'
    lscpu
  fi
  if command -v free >/dev/null 2>&1; then
    printf '\n[memory-bytes]\n'
    free -b
  fi
} >"$results_dir/client-metadata.txt"

OUTPUT_FILE="$results_dir/client-stats.jsonl" \
  bash "$script_dir/collect-host-stats.sh" &
client_stats_pid=$!

base_url() {
  printf '%s' "${1%/}"
}

case_prefix() {
  local value="$request_prefix-$1"
  printf '%s' "${value:0:64}"
}

reset_upstream() {
  local upstream_url
  for upstream_url in "$UPSTREAM_URL" "$UPSTREAM_FAIL_URL" "$UPSTREAM_FALLBACK_URL"; do
    if [[ -z "$upstream_url" ]]; then
      continue
    fi
    curl \
      --fail \
      --silent \
      --show-error \
      --max-time 10 \
      -X POST \
      "$(base_url "$upstream_url")/__admin/reset" \
      >/dev/null
  done
}

capture_upstream_url() {
  local case_name=$1
  local label=$2
  local upstream_url=$3
  if [[ -z "$upstream_url" ]]; then
    return
  fi
  curl \
    --fail \
    --silent \
    --show-error \
    --max-time 10 \
    "$(base_url "$upstream_url")/__admin/stats" \
    >"$results_dir/$case_name-$label-upstream.json" ||
    printf '{"capture_error":true}\n' >"$results_dir/$case_name-$label-upstream.json"
}

capture_upstream() {
  local case_name=$1
  capture_upstream_url "$case_name" main "$UPSTREAM_URL"
  cp "$results_dir/$case_name-main-upstream.json" \
    "$results_dir/$case_name-upstream.json"
  capture_upstream_url "$case_name" fail "$UPSTREAM_FAIL_URL"
  capture_upstream_url "$case_name" fallback "$UPSTREAM_FALLBACK_URL"
}

set_upstream_scenario() {
  local upstream_url=$1
  local scenario=$2
  curl \
    --fail \
    --silent \
    --show-error \
    --max-time 10 \
    -H 'Content-Type: application/json' \
    -d "{\"scenario\":\"$scenario\"}" \
    "$(base_url "$upstream_url")/__admin/scenario" \
    >/dev/null
}

record_case() {
  printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n' "$@" \
    >>"$results_dir/cases.tsv"
}

run_k6_case() {
  local case_name=$1
  local phase=$2
  local target=$3
  local mode=$4
  local load=$5
  local model=$6
  local stream=$7
  local prompt_bytes=$8
  local cache_mode=$9
  local repetition=${10}
  local script=${11}
  shift 11

  reset_upstream
  local -a args=(
    run
    --summary-export "$results_dir/$case_name-summary.json"
  )
  if [[ "$SAVE_RAW_METRICS" == "true" ]]; then
    args+=(--out "json=$results_dir/$case_name-metrics.json")
  fi

  printf '\n==> %s\n' "$case_name"
  local started_at
  local ended_at
  started_at=$(date -u +%Y-%m-%dT%H:%M:%S.%3NZ)
  set +e
  env \
    "REQUEST_PREFIX=$(case_prefix "$case_name")" \
    "$@" \
    k6 "${args[@]}" "$script" 2>&1 |
    tee "$results_dir/$case_name.log"
  local exit_code=${PIPESTATUS[0]}
  set -e
  ended_at=$(date -u +%Y-%m-%dT%H:%M:%S.%3NZ)
  printf '%s\n' "$exit_code" >"$results_dir/$case_name-exit-code.txt"
  capture_upstream "$case_name"
  record_case \
    "$case_name" \
    "$phase" \
    "$target" \
    "$mode" \
    "$load" \
    "$model" \
    "$stream" \
    "$prompt_bytes" \
    "$cache_mode" \
    "$repetition" \
    "$exit_code" \
    "$started_at" \
    "$ended_at"
  sleep "$COOLDOWN_SECONDS"
}

run_load() {
  local case_name=$1
  local phase=$2
  local target=$3
  local mode=$4
  local load=$5
  local model=$6
  local stream=$7
  local prompt_bytes=$8
  local cache_mode=$9
  local repetition=${10}
  local target_url=$UPSTREAM_URL
  local api_key=
  local duration=$CAPACITY_DURATION
  if [[ "$target" == "gateway" ]]; then
    target_url=$GATEWAY_URL
    api_key=$MODEL_VELO_API_KEY
  fi
  if [[ "$mode" == "rate" ]]; then
    duration=$RATE_DURATION
  fi

  local -a dimensions=(
    "TARGET_URL=$target_url"
    "TARGET_NAME=$target"
    "API_KEY=$api_key"
    "LOAD_MODE=$mode"
    "MODEL=$model"
    "STREAM=$stream"
    "DURATION=$duration"
    "RATE=$load"
    "VUS=$load"
    "PRE_ALLOCATED_VUS=$PRE_ALLOCATED_VUS"
    "MAX_VUS=$MAX_VUS"
    "PROMPT_BYTES=$prompt_bytes"
    "CACHE_MODE=$cache_mode"
    "REQUEST_TIMEOUT=$REQUEST_TIMEOUT"
    "MIN_SUCCESS_RATE=0"
    "MAX_FAILURE_RATE=1"
    "ALLOW_DROPPED_ITERATIONS=true"
  )
  run_k6_case \
    "$case_name" \
    "$phase" \
    "$target" \
    "$mode" \
    "$load" \
    "$model" \
    "$stream" \
    "$prompt_bytes" \
    "$cache_mode" \
    "$repetition" \
    "$repo_root/test/k6/load.js" \
    "${dimensions[@]}"
}

run_pair() {
  local phase=$1
  local mode=$2
  local load=$3
  local model=$4
  local stream=$5
  local prompt_bytes=$6
  local cache_mode=$7
  local repetition=$8
  local label=$9
  local -a order=(direct gateway)
  if ((repetition % 2 == 0)); then
    order=(gateway direct)
  fi
  for target in "${order[@]}"; do
    run_load \
      "$label-$target-r$repetition" \
      "$phase" \
      "$target" \
      "$mode" \
      "$load" \
      "$model" \
      "$stream" \
      "$prompt_bytes" \
      "$cache_mode" \
      "$repetition"
  done
}

run_stream_case() {
  local target=$1
  local repetition=$2
  local target_url="$(base_url "$UPSTREAM_URL")/v1/chat/completions"
  local api_key=
  if [[ "$target" == "gateway" ]]; then
    target_url="$(base_url "$GATEWAY_URL")/v1/chat/completions"
    api_key=$MODEL_VELO_API_KEY
  fi
  local case_name="stream-detail-c$(printf '%03d' "$STREAM_CONCURRENCY")-$target-r$repetition"

  reset_upstream
  printf '\n==> %s\n' "$case_name"
  local started_at
  local ended_at
  started_at=$(date -u +%Y-%m-%dT%H:%M:%S.%3NZ)
  set +e
  env "MODEL_VELO_API_KEY=$api_key" \
    "$streamload_bin" \
    -url "$target_url" \
    -model "$STREAM_MODEL" \
    -request-prefix "$(case_prefix "$case_name")" \
    -n "$STREAM_REQUESTS" \
    -c "$STREAM_CONCURRENCY" \
    -prompt-bytes "$STREAM_PROMPT_BYTES" \
    -timeout "$REQUEST_TIMEOUT" \
    -output "$results_dir/$case_name-stream.json" 2>&1 |
    tee "$results_dir/$case_name.log"
  local exit_code=${PIPESTATUS[0]}
  set -e
  ended_at=$(date -u +%Y-%m-%dT%H:%M:%S.%3NZ)
  printf '%s\n' "$exit_code" >"$results_dir/$case_name-exit-code.txt"
  capture_upstream "$case_name"
  record_case \
    "$case_name" \
    stream \
    "$target" \
    streamload \
    "$STREAM_CONCURRENCY" \
    "$STREAM_MODEL" \
    true \
    "$STREAM_PROMPT_BYTES" \
    bypass \
    "$repetition" \
    "$exit_code" \
    "$started_at" \
    "$ended_at"
  sleep "$COOLDOWN_SECONDS"
}

run_profile_case() {
  local case_name=$1
  local phase=$2
  local stages=$3
  run_k6_case \
    "$case_name" \
    "$phase" \
    gateway \
    rate-profile \
    "$stages" \
    mock/instant \
    false \
    200 \
    bypass \
    1 \
    "$repo_root/test/k6/profile.js" \
    "TARGET_URL=$GATEWAY_URL" \
    TARGET_NAME=gateway \
    "API_KEY=$MODEL_VELO_API_KEY" \
    PROFILE_MODE=rate \
    START_VALUE=1 \
    "STAGES=$stages" \
    "PRE_ALLOCATED_VUS=$PROFILE_PRE_ALLOCATED_VUS" \
    "MAX_VUS=$PROFILE_MAX_VUS" \
    MODEL=mock/instant \
    STREAM=false \
    PROMPT_BYTES=200 \
    CACHE_MODE=bypass \
    MIN_SUCCESS_RATE=0 \
    MAX_FAILURE_RATE=1 \
    "REQUEST_TIMEOUT=$REQUEST_TIMEOUT"
}

run_fault_case() {
  local case_name=$1
  local model=$2
  local statuses=$3
  run_k6_case \
    "$case_name" \
    fault \
    gateway \
    rate \
    "$FAULT_RATE" \
    "$model" \
    false \
    200 \
    bypass \
    1 \
    "$repo_root/test/k6/fault.js" \
    "TARGET_URL=$GATEWAY_URL" \
    TARGET_NAME=gateway \
    "API_KEY=$MODEL_VELO_API_KEY" \
    "MODEL=$model" \
    STREAM=false \
    "DURATION=$FAULT_DURATION" \
    "RATE=$FAULT_RATE" \
    "PRE_ALLOCATED_VUS=$PRE_ALLOCATED_VUS" \
    "MAX_VUS=$MAX_VUS" \
    "ALLOWED_STATUSES=$statuses" \
    MIN_EXPECTED_RATE=0.99 \
    "REQUEST_TIMEOUT=$REQUEST_TIMEOUT"
}

run_k6_case \
  smoke \
  smoke \
  mixed \
  iterations \
  1 \
  mock/instant \
  mixed \
  200 \
  bypass \
  1 \
  "$repo_root/test/k6/smoke.js" \
  "GATEWAY_URL=$GATEWAY_URL" \
  "UPSTREAM_URL=$UPSTREAM_URL" \
  "API_KEY=$MODEL_VELO_API_KEY" \
  "REQUEST_TIMEOUT=$REQUEST_TIMEOUT"
if [[ "$(cat "$results_dir/smoke-exit-code.txt")" != "0" ]]; then
  printf 'smoke test failed; refusing to run a meaningless benchmark\n' >&2
  exit 1
fi

if [[ "$RUN_STREAM_DETAIL" == "true" ]]; then
  (
    cd "$repo_root"
    go build -o "$streamload_bin" ./test/streamload
  )
fi

if [[ "$RUN_WARMUP" == "true" ]]; then
  original_rate_duration=$RATE_DURATION
  RATE_DURATION=$WARMUP_DURATION
  run_pair \
    warmup \
    rate \
    "$WARMUP_RATE" \
    mock/instant \
    false \
    200 \
    bypass \
    1 \
    "warmup-r$(printf '%04d' "$WARMUP_RATE")"
  RATE_DURATION=$original_rate_duration
fi

if [[ "$RUN_CAPACITY" == "true" ]]; then
  for ((repetition = 1; repetition <= REPETITIONS; repetition++)); do
    for vus in $CAPACITY_VUS; do
      run_pair \
        capacity \
        vus \
        "$vus" \
        mock/instant \
        false \
        200 \
        bypass \
        "$repetition" \
        "capacity-c$(printf '%03d' "$vus")"
    done
  done
fi

if [[ "$RUN_RATE_SWEEP" == "true" ]]; then
  for ((repetition = 1; repetition <= REPETITIONS; repetition++)); do
    for rate in $RATE_SWEEP; do
      run_load \
        "rate-r$(printf '%05d' "$rate")-gateway-r$repetition" \
        rate \
        gateway \
        rate \
        "$rate" \
        mock/instant \
        false \
        200 \
        bypass \
        "$repetition"
    done
  done
fi

if [[ "$RUN_STREAM_DETAIL" == "true" ]]; then
  for ((repetition = 1; repetition <= REPETITIONS; repetition++)); do
    if ((repetition % 2 == 0)); then
      run_stream_case gateway "$repetition"
      run_stream_case direct "$repetition"
    else
      run_stream_case direct "$repetition"
      run_stream_case gateway "$repetition"
    fi
  done
fi

if [[ "$RUN_PAYLOAD" == "true" ]]; then
  payload_profiles=(
    "small:200:mock/instant"
    "10k:10240:mock/payload-10k"
    "50k:51200:mock/payload-50k"
  )
  for ((repetition = 1; repetition <= REPETITIONS; repetition++)); do
    for profile in "${payload_profiles[@]}"; do
      IFS=: read -r name bytes model <<<"$profile"
      original_rate_duration=$RATE_DURATION
      RATE_DURATION=$PAYLOAD_DURATION
      run_pair \
        payload \
        rate \
        "$PAYLOAD_RATE" \
        "$model" \
        false \
        "$bytes" \
        bypass \
        "$repetition" \
        "payload-$name-r$(printf '%04d' "$PAYLOAD_RATE")"
      RATE_DURATION=$original_rate_duration
    done
  done
fi

if [[ "$RUN_CACHE" == "true" ]]; then
  original_rate_duration=$RATE_DURATION
  RATE_DURATION=$CACHE_DURATION
  for cache_mode in bypass unique shared; do
    run_load \
      "cache-$cache_mode-gateway-r1" \
      cache \
      gateway \
      rate \
      "$CACHE_RATE" \
      mock/instant \
      false \
      200 \
      "$cache_mode" \
      1
  done
  RATE_DURATION=$original_rate_duration
fi

if [[ "$RUN_RAMP" == "true" ]]; then
  run_profile_case ramp-gateway ramp "$RAMP_STAGES"
fi
if [[ "$RUN_BURST" == "true" ]]; then
  run_profile_case burst-gateway burst "$BURST_STAGES"
fi
if [[ "$RUN_FAULT" == "true" ]]; then
  run_fault_case fault-error10-gateway mock/error-rate-10 "200,502,503"
  run_fault_case fault-jitter-gateway mock/jitter "200"
  run_fault_case fault-spike5-gateway mock/spike-5 "200"
fi

if [[ "$RUN_FAULT_RECOVERY" == "true" ]]; then
  original_fault_rate=$FAULT_RATE
  original_fault_duration=$FAULT_DURATION
  FAULT_RATE=$FAULT_RECOVERY_RATE
  set_upstream_scenario "$UPSTREAM_FAIL_URL" mock/error-503
  FAULT_DURATION=$FAULT_RECOVERY_FAILURE_DURATION
  run_fault_case fault-fallback-5xx-gateway mock/fallback "200"
  set_upstream_scenario "$UPSTREAM_FAIL_URL" mock/instant
  FAULT_DURATION=$FAULT_RECOVERY_HEALTHY_DURATION
  run_fault_case fault-provider-recovery-gateway mock/fallback "200"
  set_upstream_scenario "$UPSTREAM_FAIL_URL" mock/error-503
  FAULT_RATE=$original_fault_rate
  FAULT_DURATION=$original_fault_duration
fi

if [[ "$RUN_QUEUE_OVERLOAD" == "true" ]]; then
  original_rate_duration=$RATE_DURATION
  original_pre_allocated_vus=$PRE_ALLOCATED_VUS
  original_max_vus=$MAX_VUS
  RATE_DURATION=$QUEUE_DURATION
  PRE_ALLOCATED_VUS=$QUEUE_PRE_ALLOCATED_VUS
  MAX_VUS=$QUEUE_MAX_VUS
  run_load \
    "queue-overload-r$(printf '%05d' "$QUEUE_RATE")-gateway-r1" \
    queue \
    gateway \
    rate \
    "$QUEUE_RATE" \
    mock/typical \
    false \
    200 \
    bypass \
    1
  RATE_DURATION=$original_rate_duration
  PRE_ALLOCATED_VUS=$original_pre_allocated_vus
  MAX_VUS=$original_max_vus
fi

if [[ "$RUN_ENDURANCE" == "true" ]]; then
  original_rate_duration=$RATE_DURATION
  original_pre_allocated_vus=$PRE_ALLOCATED_VUS
  original_max_vus=$MAX_VUS
  RATE_DURATION=$ENDURANCE_DURATION
  PRE_ALLOCATED_VUS=$ENDURANCE_PRE_ALLOCATED_VUS
  MAX_VUS=$ENDURANCE_MAX_VUS
  run_load \
    "endurance-r$(printf '%05d' "$ENDURANCE_RATE")-gateway-r1" \
    endurance \
    gateway \
    rate \
    "$ENDURANCE_RATE" \
    mock/instant \
    false \
    200 \
    bypass \
    1
  RATE_DURATION=$original_rate_duration
  PRE_ALLOCATED_VUS=$original_pre_allocated_vus
  MAX_VUS=$original_max_vus
fi

if [[ "$RUN_RELIABILITY" == "true" ]]; then
  run_k6_case \
    reliability \
    reliability \
    gateway \
    iterations \
    1 \
    mixed \
    mixed \
    200 \
    bypass \
    1 \
    "$repo_root/test/k6/reliability.js" \
    "GATEWAY_URL=$GATEWAY_URL" \
    "UPSTREAM_URL=$UPSTREAM_URL" \
    "API_KEY=$MODEL_VELO_API_KEY" \
    "REQUEST_TIMEOUT=$REQUEST_TIMEOUT"
fi

if [[ "$RUN_RATE_LIMIT" == "true" ]]; then
  run_k6_case \
    rate-limit-gateway \
    rate-limit \
    gateway \
    rate \
    "$RATE_LIMIT_TEST_RATE" \
    mock/instant \
    false \
    200 \
    bypass \
    1 \
    "$repo_root/test/k6/fault.js" \
    "TARGET_URL=$GATEWAY_URL" \
    TARGET_NAME=gateway \
    "API_KEY=$MODEL_VELO_API_KEY" \
    MODEL=mock/instant \
    STREAM=false \
    "DURATION=$RATE_LIMIT_TEST_DURATION" \
    "RATE=$RATE_LIMIT_TEST_RATE" \
    "PRE_ALLOCATED_VUS=$PRE_ALLOCATED_VUS" \
    "MAX_VUS=$MAX_VUS" \
    ALLOWED_STATUSES=200,429 \
    MIN_EXPECTED_RATE=0.99 \
    "REQUEST_TIMEOUT=$REQUEST_TIMEOUT"
fi

printf 'ended_at=%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  >>"$results_dir/client-metadata.txt"
stop_client_stats
python3 "$script_dir/summarize.py" "$results_dir"
trap - EXIT
cleanup
printf '\nresults: %s\n' "$results_dir"
