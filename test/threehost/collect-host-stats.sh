#!/usr/bin/env bash
set -euo pipefail

interval_seconds=${INTERVAL_SECONDS:-1}
output_file=${OUTPUT_FILE:-client-stats.jsonl}

for command_name in awk date; do
  if ! command -v "$command_name" >/dev/null 2>&1; then
    printf '%s is required\n' "$command_name" >&2
    exit 1
  fi
done
if [[ ! "$interval_seconds" =~ ^[1-9][0-9]*$ ]]; then
  printf 'INTERVAL_SECONDS must be a positive integer\n' >&2
  exit 1
fi
if [[ ! -r /proc/stat || ! -r /proc/meminfo || ! -r /proc/loadavg ]]; then
  printf 'Linux /proc statistics are required\n' >&2
  exit 1
fi
if [[ -e "$output_file" ]]; then
  printf 'refusing to overwrite host stats output: %s\n' "$output_file" >&2
  exit 1
fi

mkdir -p "$(dirname -- "$output_file")"
: >"$output_file"

stop=false
trap 'stop=true' INT TERM
previous_total=0
previous_idle=0

while [[ "$stop" == "false" ]]; do
  read -r _ user nice system idle iowait irq softirq steal _ </proc/stat
  total=$((user + nice + system + idle + iowait + irq + softirq + steal))
  idle_total=$((idle + iowait))
  cpu_pct=0
  if ((previous_total > 0 && total > previous_total)); then
    cpu_pct=$(awk \
      -v total="$((total - previous_total))" \
      -v idle="$((idle_total - previous_idle))" \
      'BEGIN { printf "%.3f", 100 * (total - idle) / total }')
  fi
  previous_total=$total
  previous_idle=$idle_total

  memory_total_kb=0
  memory_available_kb=0
  while read -r key value _; do
    case "$key" in
      MemTotal:)
        memory_total_kb=$value
        ;;
      MemAvailable:)
        memory_available_kb=$value
        ;;
    esac
  done </proc/meminfo
  read -r load_1m load_5m load_15m _ </proc/loadavg
  loadgen_processes=0
  loadgen_rss_kb=0
  for status_file in /proc/[0-9]*/status; do
    process_name=
    process_rss_kb=0
    while read -r key value _; do
      case "$key" in
        Name:)
          process_name=$value
          ;;
        VmRSS:)
          process_rss_kb=$value
          ;;
      esac
    done <"$status_file" 2>/dev/null || continue
    if [[ "$process_name" == "k6" ||
      "$process_name" == model-velo-str* ]]; then
      loadgen_processes=$((loadgen_processes + 1))
      loadgen_rss_kb=$((loadgen_rss_kb + process_rss_kb))
    fi
  done

  printf '{"timestamp":"%s","cpu_pct":%s,"memory_total_bytes":%d,"memory_available_bytes":%d,"load_1m":%s,"load_5m":%s,"load_15m":%s,"loadgen_processes":%d,"loadgen_rss_bytes":%d}\n' \
    "$(date -u +%Y-%m-%dT%H:%M:%S.%3NZ)" \
    "$cpu_pct" \
    "$((memory_total_kb * 1024))" \
    "$((memory_available_kb * 1024))" \
    "$load_1m" \
    "$load_5m" \
    "$load_15m" \
    "$loadgen_processes" \
    "$((loadgen_rss_kb * 1024))" \
    >>"$output_file"

  sleep "$interval_seconds" &
  wait $! || true
done
