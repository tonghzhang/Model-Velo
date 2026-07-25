#!/usr/bin/env bash
set -euo pipefail

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repo_root=$(CDPATH= cd -- "$script_dir/../.." && pwd)
env_file=${1:-"$script_dir/client.env"}

if [[ ! -f "$env_file" ]]; then
  printf 'missing client environment file: %s\n' "$env_file" >&2
  exit 1
fi
for command_name in k6 curl git; do
  if ! command -v "$command_name" >/dev/null 2>&1; then
    printf '%s is required on the client host\n' "$command_name" >&2
    exit 1
  fi
done

set -a
# shellcheck disable=SC1090
. "$env_file"
set +a

: "${GATEWAY_URL:?GATEWAY_URL is required}"
: "${UPSTREAM_URL:?UPSTREAM_URL is required}"
: "${MODEL_VELO_API_KEY:?MODEL_VELO_API_KEY is required}"

DURATION=${DURATION:-60s}
RATE=${RATE:-50}
VUS=${VUS:-20}
PRE_ALLOCATED_VUS=${PRE_ALLOCATED_VUS:-$RATE}
MAX_VUS=${MAX_VUS:-$PRE_ALLOCATED_VUS}
NON_STREAM_MODEL=${NON_STREAM_MODEL:-mock/instant}
STREAM_MODEL=${STREAM_MODEL:-mock/typical}
REQUEST_TIMEOUT=${REQUEST_TIMEOUT:-20s}
COOLDOWN_SECONDS=${COOLDOWN_SECONDS:-5}
REPETITIONS=${REPETITIONS:-1}
WARMUP_DURATION=${WARMUP_DURATION:-10s}
WARMUP_RATE=${WARMUP_RATE:-5}
RUN_WARMUP=${RUN_WARMUP:-true}
RUN_RATE=${RUN_RATE:-true}
RUN_VUS=${RUN_VUS:-true}
RUN_STREAM=${RUN_STREAM:-true}
RUN_RELIABILITY=${RUN_RELIABILITY:-true}
SAVE_RAW_METRICS=${SAVE_RAW_METRICS:-false}
RESULTS_ROOT=${RESULTS_ROOT:-"$repo_root/test-results/threehost"}

for boolean_name in \
  RUN_WARMUP \
  RUN_RATE \
  RUN_VUS \
  RUN_STREAM \
  RUN_RELIABILITY \
  SAVE_RAW_METRICS; do
  boolean_value=${!boolean_name}
  if [[ "$boolean_value" != "true" && "$boolean_value" != "false" ]]; then
    printf '%s must be true or false\n' "$boolean_name" >&2
    exit 1
  fi
done
if [[ "$RUN_RATE" != "true" && "$RUN_VUS" != "true" ]]; then
  printf 'at least one of RUN_RATE or RUN_VUS must be true\n' >&2
  exit 1
fi
if [[ ! "$REPETITIONS" =~ ^[1-9][0-9]*$ ]]; then
  printf 'REPETITIONS must be a positive integer\n' >&2
  exit 1
fi
if [[ ! "$COOLDOWN_SECONDS" =~ ^[0-9]+$ ]]; then
  printf 'COOLDOWN_SECONDS must be a non-negative integer\n' >&2
  exit 1
fi

run_id=${RUN_ID:-$(date -u +%Y%m%dT%H%M%SZ)}
if [[ ! "$run_id" =~ ^[A-Za-z0-9._-]+$ ]]; then
  printf 'RUN_ID may contain only letters, digits, dot, dash, or underscore\n' >&2
  exit 1
fi
results_dir="$RESULTS_ROOT/$run_id"
if [[ -e "$results_dir" ]]; then
  printf 'refusing to overwrite existing results directory: %s\n' \
    "$results_dir" >&2
  exit 1
fi
mkdir -p "$results_dir"

{
  printf 'run_id=%s\n' "$run_id"
  printf 'commit=%s\n' "$(git -C "$repo_root" rev-parse HEAD)"
  printf 'started_at=%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  printf 'gateway_url=%s\n' "$GATEWAY_URL"
  printf 'upstream_url=%s\n' "$UPSTREAM_URL"
  printf 'duration=%s\n' "$DURATION"
  printf 'rate=%s\n' "$RATE"
  printf 'vus=%s\n' "$VUS"
  printf 'pre_allocated_vus=%s\n' "$PRE_ALLOCATED_VUS"
  printf 'max_vus=%s\n' "$MAX_VUS"
  printf 'non_stream_model=%s\n' "$NON_STREAM_MODEL"
  printf 'stream_model=%s\n' "$STREAM_MODEL"
  printf 'repetitions=%s\n' "$REPETITIONS"
  printf 'warmup_duration=%s\n' "$WARMUP_DURATION"
  printf 'warmup_rate=%s\n' "$WARMUP_RATE"
  printf 'k6_version=%s\n' "$(k6 version | tr '\n' ' ')"
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

run_k6() {
  local case_name=$1
  local script=$2
  shift 2

  local -a k6_args=(
    run
    --summary-export "$results_dir/$case_name-summary.json"
  )
  if [[ "$SAVE_RAW_METRICS" == "true" ]]; then
    k6_args+=(--out "json=$results_dir/$case_name-metrics.json")
  fi

  printf '\n==> %s\n' "$case_name"
  env "$@" \
    k6 "${k6_args[@]}" "$script" |
    tee "$results_dir/$case_name.log"
}

run_load_pair() {
  local mode=$1
  local model=$2
  local stream=$3
  local suffix=$4
  local repetition=$5
  local common=(
    "LOAD_MODE=$mode"
    "MODEL=$model"
    "STREAM=$stream"
    "DURATION=$DURATION"
    "RATE=$RATE"
    "VUS=$VUS"
    "PRE_ALLOCATED_VUS=$PRE_ALLOCATED_VUS"
    "MAX_VUS=$MAX_VUS"
    "REQUEST_TIMEOUT=$REQUEST_TIMEOUT"
  )

  run_k6 \
    "${mode}-${suffix}-direct-r${repetition}" \
    "$repo_root/test/k6/load.js" \
    "TARGET_URL=$UPSTREAM_URL" \
    "TARGET_NAME=upstream" \
    "${common[@]}"
  sleep "$COOLDOWN_SECONDS"
  run_k6 \
    "${mode}-${suffix}-gateway-r${repetition}" \
    "$repo_root/test/k6/load.js" \
    "TARGET_URL=$GATEWAY_URL" \
    "TARGET_NAME=gateway" \
    "API_KEY=$MODEL_VELO_API_KEY" \
    "${common[@]}"
  sleep "$COOLDOWN_SECONDS"
}

run_k6 \
  smoke \
  "$repo_root/test/k6/smoke.js" \
  "GATEWAY_URL=$GATEWAY_URL" \
  "UPSTREAM_URL=$UPSTREAM_URL" \
  "API_KEY=$MODEL_VELO_API_KEY" \
  "REQUEST_TIMEOUT=$REQUEST_TIMEOUT"

if [[ "$RUN_WARMUP" == "true" ]]; then
  run_k6 \
    warmup-direct \
    "$repo_root/test/k6/load.js" \
    "TARGET_URL=$UPSTREAM_URL" \
    "TARGET_NAME=upstream" \
    "LOAD_MODE=rate" \
    "MODEL=$NON_STREAM_MODEL" \
    "STREAM=false" \
    "DURATION=$WARMUP_DURATION" \
    "RATE=$WARMUP_RATE" \
    "PRE_ALLOCATED_VUS=$PRE_ALLOCATED_VUS" \
    "MAX_VUS=$MAX_VUS" \
    "REQUEST_TIMEOUT=$REQUEST_TIMEOUT"
  run_k6 \
    warmup-gateway \
    "$repo_root/test/k6/load.js" \
    "TARGET_URL=$GATEWAY_URL" \
    "TARGET_NAME=gateway" \
    "API_KEY=$MODEL_VELO_API_KEY" \
    "LOAD_MODE=rate" \
    "MODEL=$NON_STREAM_MODEL" \
    "STREAM=false" \
    "DURATION=$WARMUP_DURATION" \
    "RATE=$WARMUP_RATE" \
    "PRE_ALLOCATED_VUS=$PRE_ALLOCATED_VUS" \
    "MAX_VUS=$MAX_VUS" \
    "REQUEST_TIMEOUT=$REQUEST_TIMEOUT"
  sleep "$COOLDOWN_SECONDS"
fi

for ((repetition = 1; repetition <= REPETITIONS; repetition++)); do
  if [[ "$RUN_RATE" == "true" ]]; then
    run_load_pair rate "$NON_STREAM_MODEL" false nonstream "$repetition"
    if [[ "$RUN_STREAM" == "true" ]]; then
      run_load_pair rate "$STREAM_MODEL" true stream "$repetition"
    fi
  fi
  if [[ "$RUN_VUS" == "true" ]]; then
    run_load_pair vus "$NON_STREAM_MODEL" false nonstream "$repetition"
    if [[ "$RUN_STREAM" == "true" ]]; then
      run_load_pair vus "$STREAM_MODEL" true stream "$repetition"
    fi
  fi
done

if [[ "$RUN_RELIABILITY" == "true" ]]; then
  run_k6 \
    reliability \
    "$repo_root/test/k6/reliability.js" \
    "GATEWAY_URL=$GATEWAY_URL" \
    "UPSTREAM_URL=$UPSTREAM_URL" \
    "API_KEY=$MODEL_VELO_API_KEY" \
    "REQUEST_TIMEOUT=$REQUEST_TIMEOUT"
fi

printf '\nresults: %s\n' "$results_dir"
