#!/usr/bin/env bash
set -euo pipefail

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repo_root=$(CDPATH= cd -- "$script_dir/../.." && pwd)
env_file=${ENV_FILE:-"$repo_root/.env"}

if [[ ! -f "$env_file" ]]; then
  printf 'environment file not found: %s\n' "$env_file" >&2
  exit 1
fi
for command_name in curl date git; do
  if ! command -v "$command_name" >/dev/null 2>&1; then
    printf '%s is required\n' "$command_name" >&2
    exit 1
  fi
done

set -a
# shellcheck disable=SC1090
. "$env_file"
set +a

duration_seconds=${DURATION_SECONDS:-7200}
interval_seconds=${INTERVAL_SECONDS:-1}
output_dir=${OUTPUT_DIR:-"$repo_root/test-results/threehost/metrics"}
bind_address=${MODEL_VELO_HTTP_BIND:-127.0.0.1}
if [[ "$bind_address" == "0.0.0.0" || "$bind_address" == "[::]" ]]; then
  bind_address=127.0.0.1
fi
gateway_url=${GATEWAY_METRICS_URL:-"http://$bind_address:${MODEL_VELO_HTTP_PORT:-8080}/metrics"}
worker_url=${WORKER_METRICS_URL:-"http://127.0.0.1:${MODEL_VELO_WORKER_METRICS_PORT:-9091}/metrics"}
metrics_token=${MODEL_VELO_METRICS_TOKEN:-}

if [[ ! "$duration_seconds" =~ ^[1-9][0-9]*$ ]]; then
  printf 'DURATION_SECONDS must be a positive integer\n' >&2
  exit 1
fi
if [[ ! "$interval_seconds" =~ ^[1-9][0-9]*$ ]]; then
  printf 'INTERVAL_SECONDS must be a positive integer\n' >&2
  exit 1
fi
if [[ -e "$output_dir" ]]; then
  printf 'refusing to overwrite metrics output directory: %s\n' "$output_dir" >&2
  exit 1
fi
mkdir -p "$output_dir"

gateway_output="$output_dir/gateway-metrics.promlog"
worker_output="$output_dir/worker-metrics.promlog"
metadata_output="$output_dir/prometheus-metadata.txt"
: >"$gateway_output"
: >"$worker_output"

{
  printf 'captured_at=%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  printf 'duration_seconds=%s\n' "$duration_seconds"
  printf 'interval_seconds=%s\n' "$interval_seconds"
  printf 'gateway_url=%s\n' "$gateway_url"
  printf 'worker_url=%s\n' "$worker_url"
  printf 'commit=%s\n' "$(git -C "$repo_root" rev-parse HEAD)"
  printf 'host=%s\n' "$(uname -a)"
} >"$metadata_output"

declare -a curl_args=(--fail --silent --show-error --max-time 2)
if [[ -n "$metrics_token" ]]; then
  curl_args+=(-H "Authorization: Bearer $metrics_token")
fi

capture() {
  local url=$1
  local output=$2
  local timestamp=$3
  local payload
  printf '# snapshot %s\n' "$timestamp" >>"$output"
  if ! payload=$(curl "${curl_args[@]}" "$url"); then
    printf '# scrape_error\n' >>"$output"
    return
  fi
  printf '%s\n' "$payload" |
    awk '/^model_velo_[A-Za-z0-9_:]+({[^}]*})? [^ ]+/' >>"$output"
}

deadline=$((SECONDS + duration_seconds))
while ((SECONDS < deadline)); do
  timestamp=$(date -u +%Y-%m-%dT%H:%M:%S.%3NZ)
  capture "$gateway_url" "$gateway_output" "$timestamp"
  capture "$worker_url" "$worker_output" "$timestamp"
  sleep "$interval_seconds"
done

printf 'wrote metrics under %s\n' "$output_dir"
