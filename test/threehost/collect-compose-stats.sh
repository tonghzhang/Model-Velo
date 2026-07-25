#!/usr/bin/env bash
set -euo pipefail

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repo_root=$(CDPATH= cd -- "$script_dir/../.." && pwd)

compose_file=${COMPOSE_FILE:-"$repo_root/compose.yaml"}
services_csv=${SERVICES:-gateway,usage-worker}
duration_seconds=${DURATION_SECONDS:-600}
interval_seconds=${INTERVAL_SECONDS:-1}
output_file=${OUTPUT_FILE:-"$repo_root/test-results/threehost/compose-stats.jsonl"}

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

IFS=',' read -r -a services <<<"$services_csv"
declare -a container_ids=()
declare -a service_names=()
for raw_service in "${services[@]}"; do
  service=$(printf '%s' "$raw_service" | tr -d '[:space:]')
  if [[ -z "$service" ]]; then
    continue
  fi
  container_id=$(docker compose -f "$compose_file" ps -q "$service")
  if [[ -z "$container_id" ]]; then
    printf 'service is not running: %s\n' "$service" >&2
    exit 1
  fi
  service_names+=("$service")
  container_ids+=("$container_id")
done
if ((${#container_ids[@]} == 0)); then
  printf 'SERVICES did not contain any service names\n' >&2
  exit 1
fi

mkdir -p "$(dirname -- "$output_file")"
metadata_file="${output_file%.jsonl}-metadata.txt"
if [[ -e "$output_file" || -e "$metadata_file" ]]; then
  printf 'refusing to overwrite existing stats output: %s or %s\n' \
    "$output_file" \
    "$metadata_file" >&2
  exit 1
fi
{
  printf 'captured_at=%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  printf 'compose_file=%s\n' "$compose_file"
  printf 'services=%s\n' "$services_csv"
  printf 'duration_seconds=%s\n' "$duration_seconds"
  printf 'interval_seconds=%s\n' "$interval_seconds"
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

deadline=$((SECONDS + duration_seconds))
: >"$output_file"
while ((SECONDS < deadline)); do
  timestamp=$(date -u +%Y-%m-%dT%H:%M:%S.%3NZ)
  while IFS= read -r stats; do
    if [[ -n "$stats" ]]; then
      printf '{"timestamp":"%s","stats":%s}\n' \
        "$timestamp" \
        "$stats" >>"$output_file"
    fi
  done < <(
    docker stats \
      --no-stream \
      --format '{{json .}}' \
      "${container_ids[@]}"
  )
  sleep "$interval_seconds"
done

printf 'wrote %s and %s\n' "$output_file" "$metadata_file"
