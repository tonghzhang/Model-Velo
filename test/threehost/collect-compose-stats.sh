#!/usr/bin/env bash
set -euo pipefail

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repo_root=$(CDPATH= cd -- "$script_dir/../.." && pwd)

compose_file=${COMPOSE_FILE:-"$repo_root/compose.yaml"}
services_csv=${SERVICES:-gateway,usage-worker,postgres,redis}
duration_seconds=${DURATION_SECONDS:-600}
interval_seconds=${INTERVAL_SECONDS:-1}
output_file=${OUTPUT_FILE:-"$repo_root/test-results/threehost/compose-stats.jsonl"}
progress_seconds=${PROGRESS_SECONDS:-10}
max_consecutive_errors=${MAX_CONSECUTIVE_ERRORS:-30}
status_file=${STATUS_FILE:-"${output_file%.jsonl}-status.json"}

for command_name in docker date git; do
  if ! command -v "$command_name" >/dev/null 2>&1; then
    printf '%s is required\n' "$command_name" >&2
    exit 1
  fi
done
if [[ ! -f "$compose_file" ]]; then
  printf 'compose file not found: %s\n' "$compose_file" >&2
  exit 1
fi
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

IFS=',' read -r -a services <<<"$services_csv"
declare -a service_names=()
for raw_service in "${services[@]}"; do
  service=$(printf '%s' "$raw_service" | tr -d '[:space:]')
  if [[ -z "$service" ]]; then
    continue
  fi
  service_names+=("$service")
done
if ((${#service_names[@]} == 0)); then
  printf 'SERVICES did not contain any service names\n' >&2
  exit 1
fi

declare -a container_ids=()
refresh_container_ids() {
  local service
  local container_id
  local -a refreshed_ids=()
  for service in "${service_names[@]}"; do
    container_id=$(docker compose -f "$compose_file" ps -q "$service") || return 1
    if [[ -z "$container_id" ]]; then
      return 1
    fi
    refreshed_ids+=("$container_id")
  done
  container_ids=("${refreshed_ids[@]}")
}
if ! refresh_container_ids; then
  printf 'one or more services are not running: %s\n' "$services_csv" >&2
  exit 1
fi

mkdir -p "$(dirname -- "$output_file")" "$(dirname -- "$status_file")"
metadata_file="${output_file%.jsonl}-metadata.txt"
if [[ -e "$output_file" || -e "$metadata_file" || -e "$status_file" ]]; then
  printf 'refusing to overwrite existing stats output: %s, %s, or %s\n' \
    "$output_file" \
    "$metadata_file" \
    "$status_file" >&2
  exit 1
fi

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
records=0
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
  printf '"snapshots":%d,"records":%d,"errors":%d,"consecutive_errors":%d,' \
    "$snapshots" \
    "$records" \
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
  printf '[%s] docker-stats state=%s elapsed=%s remaining=%s snapshots=%d records=%d errors=%d last=%s\n' \
    "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    "$state" \
    "$(format_duration "$elapsed")" \
    "$(format_duration "$remaining")" \
    "$snapshots" \
    "$records" \
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
  printf 'compose_file=%s\n' "$compose_file"
  printf 'services=%s\n' "$services_csv"
  printf 'duration_seconds=%s\n' "$duration_seconds"
  printf 'interval_seconds=%s\n' "$interval_seconds"
  printf 'progress_seconds=%s\n' "$progress_seconds"
  printf 'max_consecutive_errors=%s\n' "$max_consecutive_errors"
  printf 'status_file=%s\n' "$status_file"
  printf 'host=%s\n' "$(uname -a)"
  printf 'commit=%s\n' "$(git -C "$repo_root" rev-parse HEAD)"
  if [[ -n "$(git -C "$repo_root" status --porcelain)" ]]; then
    printf 'worktree=dirty\n'
  else
    printf 'worktree=clean\n'
  fi
  printf 'docker_server=%s\n' "$(docker version --format '{{.Server.Version}}')"
  for index in "${!container_ids[@]}"; do
    image_id=$(docker inspect \
      --format '{{.Image}}' \
      "${container_ids[$index]}")
    printf 'container_%s=%s image=%s\n' \
      "${service_names[$index]}" \
      "${container_ids[$index]}" \
      "$image_id"
  done
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
} >"$metadata_file"

: >"$output_file"
write_status starting
print_progress starting
while ((SECONDS < deadline)) && [[ "$stop_requested" == "false" ]]; do
  iteration_started=$SECONDS
  timestamp=$(date -u +%Y-%m-%dT%H:%M:%S.%3NZ)
  stats_output=
  if stats_output=$(docker stats \
      --no-stream \
      --format '{{json .}}' \
      "${container_ids[@]}" 2>&1); then
    snapshot_records=0
    while IFS= read -r stats; do
      if [[ -n "$stats" ]]; then
        printf '{"timestamp":"%s","stats":%s}\n' \
          "$timestamp" \
          "$stats" >>"$output_file"
        snapshot_records=$((snapshot_records + 1))
        records=$((records + 1))
      fi
    done <<<"$stats_output"
    if ((snapshot_records > 0)); then
      snapshots=$((snapshots + 1))
      consecutive_errors=0
      last_error=
      last_sample_at=$timestamp
    else
      errors=$((errors + 1))
      consecutive_errors=$((consecutive_errors + 1))
      last_error="docker stats returned no records"
    fi
  else
    errors=$((errors + 1))
    consecutive_errors=$((consecutive_errors + 1))
    last_error=$stats_output
    refresh_container_ids || true
  fi

  write_status collecting
  if ((SECONDS >= next_progress)); then
    print_progress collecting
    next_progress=$((SECONDS + progress_seconds))
  fi
  if ((consecutive_errors >= max_consecutive_errors)); then
    printf 'docker stats failed %d consecutive times: %s\n' \
      "$consecutive_errors" \
      "$last_error" >&2
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
printf 'wrote %s and %s\n' "$output_file" "$metadata_file"
