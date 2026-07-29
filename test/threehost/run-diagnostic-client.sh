#!/usr/bin/env bash
set -euo pipefail

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
env_file=${1:-"$script_dir/benchmark.env"}

if [[ ! -f "$env_file" ]]; then
  printf 'missing benchmark environment file: %s\n' "$env_file" >&2
  exit 1
fi

set -a
# shellcheck disable=SC1090
. "$env_file"
set +a

export REPETITIONS=1
export TEST_PROFILE=diagnostic
export WARMUP_RATE=100
export WARMUP_DURATION=15s
export RATE_SWEEP="500 750 1000 1500"
export RATE_DURATION=2m
export PRE_ALLOCATED_VUS=512
export MAX_VUS=4096
export ENDURANCE_RATE=500
export ENDURANCE_DURATION=10m
export ENDURANCE_PRE_ALLOCATED_VUS=512
export ENDURANCE_MAX_VUS=2048

export RUN_CAPACITY=false
export RUN_WARMUP=true
export RUN_RATE_SWEEP=true
export RUN_STREAM_DETAIL=false
export RUN_PAYLOAD=false
export RUN_CACHE=false
export RUN_RAMP=false
export RUN_BURST=false
export RUN_FAULT=false
export RUN_QUEUE_OVERLOAD=false
export RUN_ENDURANCE=true
export RUN_RELIABILITY=false
export RUN_RATE_LIMIT=false
export SAVE_RAW_METRICS=false

exec bash "$script_dir/run-complete-client.sh" -
