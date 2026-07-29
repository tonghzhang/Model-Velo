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
progress_seconds=${PROGRESS_SECONDS:-10}
max_consecutive_errors=${MAX_CONSECUTIVE_ERRORS:-30}
status_file=${STATUS_FILE:-"$output_dir/monitor-status.json"}
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
if [[ ! "$progress_seconds" =~ ^[1-9][0-9]*$ ]]; then
  printf 'PROGRESS_SECONDS must be a positive integer\n' >&2
  exit 1
fi
if [[ ! "$max_consecutive_errors" =~ ^[1-9][0-9]*$ ]]; then
  printf 'MAX_CONSECUTIVE_ERRORS must be a positive integer\n' >&2
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

format_duration() {
  local total_seconds=$1
  printf '%02d:%02d:%02d' \
    "$((total_seconds / 3600))" \
    "$(((total_seconds % 3600) / 60))" \
    "$((total_seconds % 60))"
}

json_escape() {
  local value=$1
  value=${value//\\/\\\\}
  value=${value//\"/\\\"}
  value=${value//$'\n'/\\n}
  value=${value//$'\r'/\\r}
  value=${value//$'\t'/\\t}
  printf '%s' "$value"
}

monitor_started=$SECONDS
started_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)
deadline=$((monitor_started + duration_seconds))
next_progress=$monitor_started
snapshots=0
scrapes=0
errors=0
consecutive_errors=0
last_sample_at=
last_error=
stop_requested=false
termination_signal=
termination_exit_code=1
completed=false

write_status() {
  local state=$1
  local reason=${2:-}
  local elapsed=$((SECONDS - monitor_started))
  local remaining=$((deadline - SECONDS))
  local temporary_status="${status_file}.tmp.$$"
  if ((remaining < 0)); then
    remaining=0
  fi
  printf '{"state":"%s","pid":%d,"started_at":"%s","updated_at":"%s",' \
    "$state" \
    "$$" \
    "$started_at" \
    "$(date -u +%Y-%m-%dT%H:%M:%SZ)" >"$temporary_status"
  printf '"duration_seconds":%d,"elapsed_seconds":%d,"remaining_seconds":%d,' \
    "$duration_seconds" \
    "$elapsed" \
    "$remaining" >>"$temporary_status"
  printf '"snapshots":%d,"scrapes":%d,"errors":%d,"consecutive_errors":%d,' \
    "$snapshots" \
    "$scrapes" \
    "$errors" \
    "$consecutive_errors" >>"$temporary_status"
  printf '"last_sample_at":"%s","reason":"%s","last_error":"%s"}\n' \
    "$last_sample_at" \
    "$(json_escape "$reason")" \
    "$(json_escape "$last_error")" >>"$temporary_status"
  mv -- "$temporary_status" "$status_file"
}

print_progress() {
  local state=$1
  local elapsed=$((SECONDS - monitor_started))
  local remaining=$((deadline - SECONDS))
  if ((remaining < 0)); then
    remaining=0
  fi
  printf '[%s] prometheus state=%s elapsed=%s remaining=%s snapshots=%d scrapes=%d errors=%d last=%s\n' \
    "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    "$state" \
    "$(format_duration "$elapsed")" \
    "$(format_duration "$remaining")" \
    "$snapshots" \
    "$scrapes" \
    "$errors" \
    "${last_sample_at:-none}"
}

handle_signal() {
  termination_signal=$1
  termination_exit_code=$2
  stop_requested=true
}

finish_monitor() {
  local exit_code=$1
  local state=failed
  local reason="exit:$exit_code"
  set +e
  if [[ "$completed" == "true" ]]; then
    state=completed
    reason=duration_reached
  elif [[ -n "$termination_signal" ]]; then
    state=interrupted
    reason="signal:$termination_signal"
  fi
  write_status "$state" "$reason"
  print_progress "$state"
}

trap 'handle_signal INT 130' INT
trap 'handle_signal TERM 143' TERM
trap 'handle_signal HUP 129' HUP
trap 'finish_monitor $?' EXIT

{
  printf 'captured_at=%s\n' "$started_at"
  printf 'duration_seconds=%s\n' "$duration_seconds"
  printf 'interval_seconds=%s\n' "$interval_seconds"
  printf 'progress_seconds=%s\n' "$progress_seconds"
  printf 'max_consecutive_errors=%s\n' "$max_consecutive_errors"
  printf 'status_file=%s\n' "$status_file"
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
    last_error="scrape failed: $url"
    return 1
  fi
  printf '%s\n' "$payload" |
    awk '/^(model_velo_|go_|process_)[A-Za-z0-9_:]+({[^}]*})? [^ ]+/' >>"$output"
  return 0
}

write_status starting
print_progress starting
while ((SECONDS < deadline)) && [[ "$stop_requested" == "false" ]]; do
  iteration_started=$SECONDS
  timestamp=$(date -u +%Y-%m-%dT%H:%M:%S.%3NZ)
  snapshot_errors=0
  if ! capture "$gateway_url" "$gateway_output" "$timestamp"; then
    snapshot_errors=$((snapshot_errors + 1))
  fi
  if ! capture "$worker_url" "$worker_output" "$timestamp"; then
    snapshot_errors=$((snapshot_errors + 1))
  fi
  snapshots=$((snapshots + 1))
  scrapes=$((scrapes + 2))
  errors=$((errors + snapshot_errors))
  if ((snapshot_errors == 2)); then
    consecutive_errors=$((consecutive_errors + 1))
  else
    last_sample_at=$timestamp
    consecutive_errors=0
    if ((snapshot_errors == 0)); then
      last_error=
    fi
  fi

  write_status collecting
  if ((SECONDS >= next_progress)); then
    print_progress collecting
    next_progress=$((SECONDS + progress_seconds))
  fi
  if ((consecutive_errors >= max_consecutive_errors)); then
    printf 'both Prometheus scrapes failed %d consecutive times\n' \
      "$consecutive_errors" >&2
    exit 1
  fi

  iteration_elapsed=$((SECONDS - iteration_started))
  sleep_seconds=$((interval_seconds - iteration_elapsed))
  if ((sleep_seconds > 0)) && ((SECONDS < deadline)); then
    sleep "$sleep_seconds" &
    wait $! || true
  fi
done

if [[ "$stop_requested" == "true" ]]; then
  exit "$termination_exit_code"
fi
completed=true
printf 'wrote metrics under %s\n' "$output_dir"
