#!/usr/bin/env python3

import csv
import json
import math
import re
import statistics
import sys
from collections import defaultdict
from datetime import datetime
from pathlib import Path


def read_json(path):
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError):
        return {}


def number(value, default=0.0):
    try:
        return float(value)
    except (TypeError, ValueError):
        return default


def parse_timestamp(value):
    if not value:
        return None
    try:
        return datetime.fromisoformat(str(value).replace("Z", "+00:00"))
    except ValueError:
        return None


def case_window(cases):
    starts = [
        timestamp
        for timestamp in (parse_timestamp(case.get("started_at")) for case in cases)
        if timestamp is not None
    ]
    ends = [
        timestamp
        for timestamp in (parse_timestamp(case.get("ended_at")) for case in cases)
        if timestamp is not None
    ]
    return (min(starts) if starts else None, max(ends) if ends else None)


def in_window(timestamp, started_at, ended_at):
    if timestamp is None or started_at is None or ended_at is None:
        return True
    return started_at <= timestamp <= ended_at


def metric_value(metrics, name, field="value"):
    return number(metrics.get(name, {}).get(field))


def trend(metrics, name):
    value = metrics.get(name, {})
    return {
        "avg_ms": number(value.get("avg")),
        "p50_ms": number(value.get("med")),
        "p90_ms": number(value.get("p(90)")),
        "p95_ms": number(value.get("p(95)")),
        "p99_ms": number(value.get("p(99)")),
        "max_ms": number(value.get("max")),
    }


def read_cases(result_dir):
    manifest = result_dir / "cases.tsv"
    if manifest.exists():
        with manifest.open(encoding="utf-8", newline="") as handle:
            return list(csv.DictReader(handle, delimiter="\t"))

    cases = []
    for path in sorted(result_dir.glob("*-summary.json")):
        name = path.name.removesuffix("-summary.json")
        target = "gateway" if "gateway" in name else "direct"
        cases.append(
            {
                "case": name,
                "phase": "legacy",
                "target": target,
                "mode": "",
                "load": "",
                "model": "",
                "stream": str("stream" in name and "nonstream" not in name).lower(),
                "prompt_bytes": "",
                "cache_mode": "",
                "repetition": "1",
                "exit_code": "0",
            }
        )
    return cases


def read_k6_case(result_dir, case):
    path = result_dir / f"{case['case']}-summary.json"
    payload = read_json(path)
    metrics = payload.get("metrics", {})
    if not metrics:
        return {}

    success = None
    for name in ("chat_success", "fault_expected_outcome", "checks"):
        if name in metrics:
            success = metric_value(metrics, name)
            break
    upstream = read_json(result_dir / f"{case['case']}-upstream.json")
    return {
        **case,
        "exit_code": int(number(case.get("exit_code"))),
        "requests": int(metric_value(metrics, "http_reqs", "count")),
        "iterations": int(metric_value(metrics, "iterations", "count")),
        "throughput_rps": metric_value(metrics, "iterations", "rate"),
        "success_rate": success,
        "http_failure_rate": metric_value(metrics, "http_req_failed"),
        "dropped_iterations": int(metric_value(metrics, "dropped_iterations", "count")),
        "latency": trend(metrics, "http_req_duration"),
        "waiting": trend(metrics, "http_req_waiting"),
        "status_counts": {
            "200": int(metric_value(metrics, "chat_responses_200", "count")),
            "429": int(metric_value(metrics, "chat_responses_429", "count")),
            "5xx": int(metric_value(metrics, "chat_responses_5xx", "count")),
            "other": int(metric_value(metrics, "chat_responses_other", "count")),
        },
        "upstream": upstream,
    }


def read_stream_case(result_dir, case):
    payload = read_json(result_dir / f"{case['case']}-stream.json")
    if not payload:
        return {}
    return {
        **case,
        "exit_code": int(number(case.get("exit_code"))),
        "requests": int(number(payload.get("requests"))),
        "successes": int(number(payload.get("successes"))),
        "failures": int(number(payload.get("failures"))),
        "incomplete": int(number(payload.get("incomplete"))),
        "success_rate": number(payload.get("success_rate")),
        "throughput_rps": number(payload.get("throughput_rps")),
        "headers": payload.get("headers", {}),
        "first_event": payload.get("first_event", {}),
        "first_content": payload.get("first_content", {}),
        "total": payload.get("total", {}),
        "inter_chunk": payload.get("inter_chunk", {}),
        "events": int(number(payload.get("events"))),
        "content_chunks": int(number(payload.get("content_chunks"))),
        "response_bytes": int(number(payload.get("response_bytes"))),
        "status_counts": payload.get("status_counts", {}),
        "error_counts": payload.get("error_counts", {}),
        "upstream": read_json(result_dir / f"{case['case']}-upstream.json"),
    }


def median(values):
    present = [value for value in values if value is not None]
    if not present:
        return 0.0
    return statistics.median(present)


def aggregate_k6(rows, phase, target=None):
    groups = defaultdict(list)
    for row in rows:
        if row.get("phase") != phase:
            continue
        if target and row.get("target") != target:
            continue
        groups[(row.get("target"), row.get("load"))].append(row)

    output = []
    for (group_target, load), samples in groups.items():
        output.append(
            {
                "target": group_target,
                "load": int(load) if str(load).isdigit() else load,
                "trials": len(samples),
                "throughput_rps": median([sample["throughput_rps"] for sample in samples]),
                "success_rate": median([sample["success_rate"] for sample in samples]),
                "dropped_iterations": median(
                    [sample["dropped_iterations"] for sample in samples]
                ),
                "p50_ms": median([sample["latency"]["p50_ms"] for sample in samples]),
                "p95_ms": median([sample["latency"]["p95_ms"] for sample in samples]),
                "p99_ms": median([sample["latency"]["p99_ms"] for sample in samples]),
                "max_ms": max(sample["latency"]["max_ms"] for sample in samples),
                "upstream_requests": median(
                    [
                        number(sample.get("upstream", {}).get("requests"))
                        for sample in samples
                    ]
                ),
                "upstream_errors": median(
                    [
                        number(sample.get("upstream", {}).get("errors"))
                        for sample in samples
                    ]
                ),
                "upstream_max_active": max(
                    [
                        number(sample.get("upstream", {}).get("max_active"))
                        for sample in samples
                    ]
                    or [0]
                ),
            }
        )
    return sorted(
        output,
        key=lambda row: (
            str(row["target"]),
            row["load"] if isinstance(row["load"], int) else 0,
        ),
    )


def pair_comparisons(rows):
    by_name = {row["case"]: row for row in rows}
    groups = defaultdict(list)
    for direct in rows:
        if "-direct-" not in direct["case"]:
            continue
        gateway_name = direct["case"].replace("-direct-", "-gateway-")
        gateway = by_name.get(gateway_name)
        if not gateway:
            continue
        label = re.sub(r"-r[0-9]+$", "", direct["case"].replace("-direct-", "-pair-"))
        direct_rps = direct["throughput_rps"]
        ratio = gateway["throughput_rps"] / direct_rps if direct_rps > 0 else 0
        groups[label].append(
            {
                "phase": direct["phase"],
                "load": direct["load"],
                "throughput_ratio": ratio,
                "p50_delta_ms": gateway["latency"]["p50_ms"]
                - direct["latency"]["p50_ms"],
                "p95_delta_ms": gateway["latency"]["p95_ms"]
                - direct["latency"]["p95_ms"],
                "p99_delta_ms": gateway["latency"]["p99_ms"]
                - direct["latency"]["p99_ms"],
            }
        )

    comparisons = []
    for label, samples in sorted(groups.items()):
        comparisons.append(
            {
                "case": label,
                "phase": samples[0]["phase"],
                "load": samples[0]["load"],
                "trials": len(samples),
                "throughput_ratio": median(
                    [sample["throughput_ratio"] for sample in samples]
                ),
                "p50_delta_ms": median(
                    [sample["p50_delta_ms"] for sample in samples]
                ),
                "p95_delta_ms": median(
                    [sample["p95_delta_ms"] for sample in samples]
                ),
                "p99_delta_ms": median(
                    [sample["p99_delta_ms"] for sample in samples]
                ),
            }
        )
    return comparisons


def stream_comparisons(rows):
    by_name = {row["case"]: row for row in rows}
    groups = defaultdict(list)
    for direct in rows:
        if "-direct-" not in direct["case"]:
            continue
        gateway_name = direct["case"].replace("-direct-", "-gateway-")
        gateway = by_name.get(gateway_name)
        if not gateway:
            continue
        label = re.sub(r"-r[0-9]+$", "", direct["case"].replace("-direct-", "-pair-"))
        direct_rps = direct["throughput_rps"]
        groups[label].append(
            {
                "throughput_ratio": gateway["throughput_rps"] / direct_rps
                if direct_rps > 0
                else 0,
                "first_content_p50_delta_ms": number(
                    gateway["first_content"].get("p50_ms")
                )
                - number(direct["first_content"].get("p50_ms")),
                "first_content_p99_delta_ms": number(
                    gateway["first_content"].get("p99_ms")
                )
                - number(direct["first_content"].get("p99_ms")),
                "total_p99_delta_ms": number(gateway["total"].get("p99_ms"))
                - number(direct["total"].get("p99_ms")),
                "gap_p99_delta_ms": number(gateway["inter_chunk"].get("p99_ms"))
                - number(direct["inter_chunk"].get("p99_ms")),
            }
        )
    comparisons = []
    for label, samples in sorted(groups.items()):
        comparisons.append(
            {
                "case": label,
                "trials": len(samples),
                **{
                    key: median([sample[key] for sample in samples])
                    for key in samples[0]
                },
            }
        )
    return comparisons


def parse_bytes(value):
    match = re.fullmatch(r"\s*([0-9.]+)\s*([KMGT]?i?B)\s*", value)
    if not match:
        return 0.0
    amount = float(match.group(1))
    unit = match.group(2)
    powers = {
        "B": 0,
        "KB": 1,
        "KiB": 1,
        "MB": 2,
        "MiB": 2,
        "GB": 3,
        "GiB": 3,
        "TB": 4,
        "TiB": 4,
    }
    base = 1024 if "iB" in unit else 1000
    return amount * base ** powers.get(unit, 0)


def read_resources(result_dir, started_at=None, ended_at=None):
    samples = defaultdict(list)
    for path in result_dir.rglob("*stats*.jsonl"):
        if path.name == "client-stats.jsonl":
            continue
        try:
            lines = path.read_text(encoding="utf-8").splitlines()
        except OSError:
            continue
        for line in lines:
            try:
                payload = json.loads(line)
            except json.JSONDecodeError:
                continue
            if not in_window(
                parse_timestamp(payload.get("timestamp")), started_at, ended_at
            ):
                continue
            stats = payload.get("stats", {})
            name = stats.get("Name") or stats.get("Container") or stats.get("ID")
            if not name:
                continue
            memory = str(stats.get("MemUsage", "")).split("/", maxsplit=1)[0]
            samples[name].append(
                {
                    "cpu_pct": number(str(stats.get("CPUPerc", "")).rstrip("%")),
                    "memory_bytes": parse_bytes(memory),
                    "pids": number(stats.get("PIDs")),
                }
            )

    output = []
    for name, values in sorted(samples.items()):
        output.append(
            {
                "container": name,
                "samples": len(values),
                "cpu_avg_pct": statistics.fmean(
                    sample["cpu_pct"] for sample in values
                ),
                "cpu_max_pct": max(sample["cpu_pct"] for sample in values),
                "memory_avg_mb": statistics.fmean(
                    sample["memory_bytes"] for sample in values
                )
                / (1024 * 1024),
                "memory_max_mb": max(sample["memory_bytes"] for sample in values)
                / (1024 * 1024),
                "pids_max": max(sample["pids"] for sample in values),
            }
        )
    return output


def read_client_resource(result_dir):
    paths = list(result_dir.rglob("client-stats.jsonl"))
    if not paths:
        return {}
    samples = []
    for line in paths[0].read_text(
        encoding="utf-8", errors="replace"
    ).splitlines():
        try:
            sample = json.loads(line)
        except json.JSONDecodeError:
            continue
        if not isinstance(sample, dict):
            continue
        samples.append(sample)
    if not samples:
        return {}

    cpu = [number(sample.get("cpu_pct")) for sample in samples]
    cpu_peak_sample = max(samples, key=lambda sample: number(sample.get("cpu_pct")))
    used_memory = [
        max(
            0,
            number(sample.get("memory_total_bytes"))
            - number(sample.get("memory_available_bytes")),
        )
        for sample in samples
    ]
    return {
        "samples": len(samples),
        "cpu_avg_pct": statistics.fmean(cpu),
        "cpu_max_pct": max(cpu),
        "cpu_peak_at": cpu_peak_sample.get("timestamp", ""),
        "memory_used_avg_mb": statistics.fmean(used_memory) / (1024 * 1024),
        "memory_used_max_mb": max(used_memory) / (1024 * 1024),
        "load_1m_max": max(number(sample.get("load_1m")) for sample in samples),
        "loadgen_rss_max_mb": max(
            number(sample.get("loadgen_rss_bytes")) for sample in samples
        )
        / (1024 * 1024),
        "loadgen_processes_max": int(
            max(number(sample.get("loadgen_processes")) for sample in samples)
        ),
    }


PROMETHEUS_LINE = re.compile(
    r"^((?:model_velo_|go_|process_)[A-Za-z0-9_:]+(?:\{[^}]*\})?)\s+"
    r"(-?(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)(?:[eE][+-]?[0-9]+)?)$"
)
PROMETHEUS_SERIES = re.compile(
    r"^((?:model_velo_|go_|process_)[A-Za-z0-9_:]+)(?:\{(.*)\})?$"
)
PROMETHEUS_LABEL = re.compile(r'([A-Za-z_][A-Za-z0-9_]*)="((?:\\.|[^"])*)"')


def read_prometheus(result_dir, started_at=None, ended_at=None):
    series = {}
    points = defaultdict(list)
    for path in result_dir.rglob("*.promlog"):
        snapshot_at = None
        for line in path.read_text(encoding="utf-8", errors="replace").splitlines():
            if line.startswith("# snapshot "):
                snapshot_at = parse_timestamp(line.removeprefix("# snapshot ").strip())
                continue
            if not in_window(snapshot_at, started_at, ended_at):
                continue
            match = PROMETHEUS_LINE.match(line.strip())
            if not match:
                continue
            key = f"{path.name}:{match.group(1)}"
            value = number(match.group(2))
            current = series.setdefault(
                key,
                {
                    "source": path.name,
                    "series": match.group(1),
                    "samples": 0,
                    "first": value,
                    "min": value,
                    "max": value,
                },
            )
            current["samples"] += 1
            current["last"] = value
            current["min"] = min(current["min"], value)
            current["max"] = max(current["max"], value)
            if snapshot_at is not None:
                points[key].append((snapshot_at, value))
    output = sorted(series.values(), key=lambda item: (item["source"], item["series"]))
    for item in output:
        item["delta"] = max(0.0, item.get("last", 0.0) - item["first"])
    return output, points


def prometheus_identity(key):
    _, separator, series = key.partition(":")
    if not separator:
        return "", {}
    match = PROMETHEUS_SERIES.match(series)
    if not match:
        return "", {}
    labels = {}
    for label in PROMETHEUS_LABEL.finditer(match.group(2) or ""):
        try:
            labels[label.group(1)] = json.loads(f'"{label.group(2)}"')
        except json.JSONDecodeError:
            labels[label.group(1)] = label.group(2)
    return match.group(1), labels


def window_delta(samples, started_at, ended_at):
    if not samples or started_at is None or ended_at is None:
        return 0.0
    before = None
    after = None
    first_inside = None
    last_inside = None
    for timestamp, value in samples:
        if timestamp <= started_at:
            before = value
        if started_at <= timestamp <= ended_at:
            if first_inside is None:
                first_inside = value
            last_inside = value
        if timestamp >= ended_at:
            after = value
            break
    first = before if before is not None else first_inside
    last = after if after is not None else last_inside
    if first is None or last is None:
        return 0.0
    return max(0.0, last - first)


def window_max(samples, started_at, ended_at):
    values = [
        value
        for timestamp, value in samples
        if started_at is not None
        and ended_at is not None
        and started_at <= timestamp <= ended_at
    ]
    return max(values) if values else 0.0


def prometheus_counter(
    points, metric, started_at, ended_at, labels=None, source=None
):
    total = 0.0
    labels = labels or {}
    for key, samples in points.items():
        if source and not key.startswith(f"{source}:"):
            continue
        name, series_labels = prometheus_identity(key)
        if name != metric:
            continue
        if any(series_labels.get(name) != value for name, value in labels.items()):
            continue
        total += window_delta(samples, started_at, ended_at)
    return total


def prometheus_gauge_max(
    points, metric, started_at, ended_at, labels=None, source=None
):
    maximum = 0.0
    labels = labels or {}
    for key, samples in points.items():
        if source and not key.startswith(f"{source}:"):
            continue
        name, series_labels = prometheus_identity(key)
        if name != metric:
            continue
        if any(series_labels.get(name) != value for name, value in labels.items()):
            continue
        maximum = max(maximum, window_max(samples, started_at, ended_at))
    return maximum


def histogram_quantile(buckets, quantile):
    if not buckets:
        return 0.0
    ordered = sorted(buckets.items(), key=lambda item: item[0])
    count = ordered[-1][1]
    if count <= 0:
        return 0.0
    target = count * quantile
    previous_bound = 0.0
    previous_count = 0.0
    for bound, cumulative in ordered:
        if cumulative < target:
            if math.isfinite(bound):
                previous_bound = bound
            previous_count = cumulative
            continue
        if not math.isfinite(bound):
            return previous_bound
        bucket_count = cumulative - previous_count
        if bucket_count <= 0:
            return bound
        fraction = (target - previous_count) / bucket_count
        return previous_bound + (bound - previous_bound) * fraction
    return ordered[-1][0]


STAGE_ORDER = {
    stage: index
    for index, stage in enumerate(
        (
            "authentication",
            "usage_begin",
            "authorization",
            "rate_limit",
            "route_plan",
            "quota_reserve",
            "cache_lookup",
            "provider_queue",
            "provider_call",
            "reliability",
            "cache_store",
            "quota_settle",
            "usage_finalize",
        )
    )
}


def case_stage_metrics(points, started_at, ended_at):
    stages = defaultdict(
        lambda: {"buckets": defaultdict(float), "sum_seconds": 0.0, "count": 0.0}
    )
    for key, samples in points.items():
        metric, labels = prometheus_identity(key)
        stage = labels.get("stage", "")
        if not stage:
            continue
        delta = window_delta(samples, started_at, ended_at)
        if metric == "model_velo_request_stage_duration_seconds_bucket":
            raw_bound = labels.get("le", "")
            bound = math.inf if raw_bound == "+Inf" else number(raw_bound, math.nan)
            if not math.isnan(bound):
                stages[stage]["buckets"][bound] += delta
        elif metric == "model_velo_request_stage_duration_seconds_sum":
            stages[stage]["sum_seconds"] += delta
        elif metric == "model_velo_request_stage_duration_seconds_count":
            stages[stage]["count"] += delta

    output = []
    for stage, values in stages.items():
        count = values["count"]
        if count <= 0:
            continue
        output.append(
            {
                "stage": stage,
                "count": int(count),
                "avg_ms": values["sum_seconds"] / count * 1000,
                "p50_ms": histogram_quantile(values["buckets"], 0.50) * 1000,
                "p95_ms": histogram_quantile(values["buckets"], 0.95) * 1000,
                "p99_ms": histogram_quantile(values["buckets"], 0.99) * 1000,
            }
        )
    return sorted(
        output,
        key=lambda item: (STAGE_ORDER.get(item["stage"], len(STAGE_ORDER)), item["stage"]),
    )


def case_error_counts(points, started_at, ended_at):
    counts = defaultdict(float)
    for key, samples in points.items():
        metric, labels = prometheus_identity(key)
        if metric != "model_velo_http_errors_total":
            continue
        count = window_delta(samples, started_at, ended_at)
        if count > 0:
            counts[(labels.get("status", ""), labels.get("code", ""))] += count
    return [
        {"status": status, "code": code, "count": int(count)}
        for (status, code), count in sorted(
            counts.items(), key=lambda item: (-item[1], item[0])
        )
    ]


def build_performance_diagnostics(points, rows):
    diagnostics = []
    for row in rows:
        if row.get("target") != "gateway":
            continue
        started_at = parse_timestamp(row.get("started_at"))
        ended_at = parse_timestamp(row.get("ended_at"))
        if started_at is None or ended_at is None:
            continue
        duration_seconds = max(0.0, (ended_at - started_at).total_seconds())
        stages = case_stage_metrics(points, started_at, ended_at)
        diagnostics.append(
            {
                "case": row.get("case", ""),
                "phase": row.get("phase", ""),
                "load": row.get("load", ""),
                "duration_seconds": duration_seconds,
                "stages": stages,
                "errors": case_error_counts(points, started_at, ended_at),
                "postgres": {
                    "waits": int(
                        prometheus_counter(
                            points,
                            "model_velo_postgres_waits_total",
                            started_at,
                            ended_at,
                        )
                    ),
                    "wait_ms": prometheus_counter(
                        points,
                        "model_velo_postgres_wait_duration_seconds_total",
                        started_at,
                        ended_at,
                    )
                    * 1000,
                    "in_use_max": int(
                        prometheus_gauge_max(
                            points,
                            "model_velo_postgres_connections",
                            started_at,
                            ended_at,
                            {"state": "in_use"},
                        )
                    ),
                    "open_max": int(
                        prometheus_gauge_max(
                            points,
                            "model_velo_postgres_connections",
                            started_at,
                            ended_at,
                            {"state": "open"},
                        )
                    ),
                },
                "redis": {
                    "waits": int(
                        prometheus_counter(
                            points,
                            "model_velo_redis_pool_events_total",
                            started_at,
                            ended_at,
                            {"event": "wait"},
                        )
                    ),
                    "timeouts": int(
                        prometheus_counter(
                            points,
                            "model_velo_redis_pool_events_total",
                            started_at,
                            ended_at,
                            {"event": "timeout"},
                        )
                    ),
                    "wait_ms": prometheus_counter(
                        points,
                        "model_velo_redis_pool_wait_duration_seconds_total",
                        started_at,
                        ended_at,
                    )
                    * 1000,
                    "pending_max": int(
                        prometheus_gauge_max(
                            points,
                            "model_velo_redis_pool_connections",
                            started_at,
                            ended_at,
                            {"state": "pending"},
                        )
                    ),
                    "total_max": int(
                        prometheus_gauge_max(
                            points,
                            "model_velo_redis_pool_connections",
                            started_at,
                            ended_at,
                            {"state": "total"},
                        )
                    ),
                },
                "runtime": {
                    "process_cpu_avg_pct": (
                        prometheus_counter(
                            points,
                            "process_cpu_seconds_total",
                            started_at,
                            ended_at,
                            source="gateway-metrics.promlog",
                        )
                        / duration_seconds
                        * 100
                        if duration_seconds > 0
                        else 0.0
                    ),
                    "rss_max_mb": prometheus_gauge_max(
                        points,
                        "process_resident_memory_bytes",
                        started_at,
                        ended_at,
                        source="gateway-metrics.promlog",
                    )
                    / (1024 * 1024),
                    "heap_max_mb": prometheus_gauge_max(
                        points,
                        "go_memstats_heap_alloc_bytes",
                        started_at,
                        ended_at,
                        source="gateway-metrics.promlog",
                    )
                    / (1024 * 1024),
                    "goroutines_max": int(
                        prometheus_gauge_max(
                            points,
                            "go_goroutines",
                            started_at,
                            ended_at,
                            source="gateway-metrics.promlog",
                        )
                    ),
                    "gc_cycles": int(
                        prometheus_counter(
                            points,
                            "go_gc_duration_seconds_count",
                            started_at,
                            ended_at,
                            source="gateway-metrics.promlog",
                        )
                    ),
                },
            }
        )
    return diagnostics


def read_usage_evidence(result_dir):
    evidence = {}
    drain_paths = list(result_dir.rglob("usage-drain.txt"))
    if drain_paths:
        drain = {}
        for line in drain_paths[0].read_text(
            encoding="utf-8", errors="replace"
        ).splitlines():
            key, separator, value = line.partition("=")
            if separator:
                drain[key] = value
        evidence["drain"] = drain
    overview_paths = list(result_dir.rglob("usage-overview.csv"))
    if overview_paths:
        with overview_paths[0].open(encoding="utf-8", newline="") as handle:
            rows = list(csv.DictReader(handle))
            if rows:
                evidence["overview"] = rows[0]
    status_paths = list(result_dir.rglob("usage-status.csv"))
    if status_paths:
        with status_paths[0].open(encoding="utf-8", newline="") as handle:
            evidence["status"] = list(csv.DictReader(handle))
    outbox_paths = list(result_dir.rglob("usage-outbox.csv"))
    if outbox_paths:
        with outbox_paths[0].open(encoding="utf-8", newline="") as handle:
            evidence["outbox"] = list(csv.DictReader(handle))
    diagnostic_paths = list(result_dir.rglob("usage-diagnostics.csv"))
    if diagnostic_paths:
        with diagnostic_paths[0].open(encoding="utf-8", newline="") as handle:
            evidence["diagnostics"] = list(csv.DictReader(handle))
    return evidence


def find_series(prometheus, prefix):
    return [item for item in prometheus if item["series"].startswith(prefix)]


def reconcile_usage(k6_rows, stream_rows, usage):
    excluded_phases = {"smoke", "reliability", "rate-limit"}
    expected = sum(
        int(number(row.get("requests")))
        for row in k6_rows + stream_rows
        if row.get("target") == "gateway"
        and row.get("phase") not in excluded_phases
    )
    observed = int(number(usage.get("overview", {}).get("events")))
    ratio = float(observed) / expected if expected > 0 else 0.0
    return {
        "expected_gateway_requests": expected,
        "observed_usage_events": observed,
        "difference": observed - expected,
        "observed_ratio": ratio,
    }


def capacity_findings(capacity, rate, comparisons, stream_pairs):
    findings = []
    gateway_capacity = [
        row for row in capacity if row["target"] == "gateway" and row["success_rate"] >= 0.999
    ]
    if gateway_capacity:
        peak = max(gateway_capacity, key=lambda row: row["throughput_rps"])
        findings.append(
            f"Closed-loop peak observed at c={peak['load']}: "
            f"{peak['throughput_rps']:.1f} RPS, P99 {peak['p99_ms']:.2f} ms."
        )
    sustainable = []
    for row in rate:
        target = number(row["load"])
        achieved = row["throughput_rps"]
        if (
            row["success_rate"] >= 0.999
            and row["dropped_iterations"] == 0
            and achieved >= target * 0.99
        ):
            sustainable.append(row)
    if sustainable:
        best = max(sustainable, key=lambda row: number(row["load"]))
        findings.append(
            f"Highest open-loop SLO point: {best['load']} RPS with "
            f"P99 {best['p99_ms']:.2f} ms."
        )
    elif rate:
        findings.append("No rate-sweep point met 99.9% success with zero drops.")
    low_load = [
        row
        for row in comparisons
        if row["phase"] == "capacity" and str(row["load"]) == "1"
    ]
    if low_load:
        findings.append(
            f"Low-load gateway P50 overhead: {low_load[0]['p50_delta_ms']:.2f} ms; "
            f"P99 overhead: {low_load[0]['p99_delta_ms']:.2f} ms."
        )
    if stream_pairs:
        findings.append(
            f"SSE first-content P50 overhead: "
            f"{stream_pairs[0]['first_content_p50_delta_ms']:.2f} ms; "
            f"inter-chunk P99 overhead: {stream_pairs[0]['gap_p99_delta_ms']:.2f} ms."
        )
    return findings


def actionable_findings(resources, client_resource, prometheus, rows):
    findings = []
    if client_resource.get("cpu_max_pct", 0) >= 90:
        peak_at = parse_timestamp(client_resource.get("cpu_peak_at"))
        peak_case = next(
            (
                row
                for row in rows
                if peak_at is not None
                and (
                    parse_timestamp(row.get("started_at")) or peak_at
                ) <= peak_at
                <= (parse_timestamp(row.get("ended_at")) or peak_at)
            ),
            {},
        )
        if peak_case.get("target") == "direct":
            findings.append(
                f"Client host CPU reached {client_resource['cpu_max_pct']:.1f}% "
                f"during direct baseline {peak_case.get('case')}; "
                "that direct-throughput point may be load-generator limited."
            )
        else:
            findings.append(
                f"Client host CPU reached {client_resource['cpu_max_pct']:.1f}%"
                + (
                    f" during {peak_case.get('case')}."
                    if peak_case.get("case")
                    else "."
                )
            )
    for resource in resources:
        name = resource["container"].lower()
        if "gateway" in name and resource["cpu_max_pct"] >= 90:
            findings.append(
                f"Gateway CPU reached {resource['cpu_max_pct']:.1f}%; inspect CPU hot paths."
            )
        if "usage-worker" in name and resource["cpu_max_pct"] >= 90:
            findings.append(
                f"Usage worker CPU reached {resource['cpu_max_pct']:.1f}%; inspect batching."
            )
    queue_waiting = find_series(prometheus, "model_velo_provider_queue_waiting")
    if queue_waiting and max(item["max"] for item in queue_waiting) > 0:
        findings.append("Provider queue waiting became non-zero during the run.")
    worker_pending = find_series(prometheus, "model_velo_usage_worker_pending")
    if worker_pending and max(item["max"] for item in worker_pending) > 0:
        findings.append("Usage consumer-group pending entries accumulated during load.")
    fault_rows = [
        row
        for row in rows
        if row.get("phase") == "fault"
        and number(row.get("requests")) > 0
        and number(row.get("upstream", {}).get("requests")) > 0
    ]
    if fault_rows:
        amplified = max(
            fault_rows,
            key=lambda row: number(row["upstream"].get("requests"))
            / number(row["requests"]),
        )
        ratio = number(amplified["upstream"].get("requests")) / number(
            amplified["requests"]
        )
        findings.append(
            f"Highest fault-case upstream amplification was {ratio:.2f}x "
            f"in {amplified['case']}."
        )
    queue_rows = [row for row in rows if row.get("phase") == "queue"]
    if queue_rows:
        queue = queue_rows[0]
        rejected = queue["status_counts"]["5xx"] + queue["status_counts"]["other"]
        findings.append(
            f"Queue overload produced {rejected} non-200 responses; "
            f"upstream max active was "
            f"{number(queue.get('upstream', {}).get('max_active')):.0f}."
        )
    failed_cases = [row["case"] for row in rows if row.get("exit_code", 0) != 0]
    if failed_cases:
        findings.append(f"{len(failed_cases)} cases exited non-zero: {', '.join(failed_cases[:8])}.")
    return findings


def performance_findings(diagnostics):
    findings = []
    diagnosed = [item for item in diagnostics if item["stages"]]
    if not diagnosed:
        return findings

    pre_provider_names = {
        "authentication",
        "usage_begin",
        "authorization",
        "rate_limit",
        "route_plan",
        "quota_reserve",
    }
    representative = max(
        diagnosed,
        key=lambda item: (
            number(item.get("load")),
            item.get("duration_seconds", 0),
        ),
    )
    pre_provider = [
        stage
        for stage in representative["stages"]
        if stage["stage"] in pre_provider_names
    ]
    if pre_provider:
        slowest = max(pre_provider, key=lambda stage: stage["p99_ms"])
        findings.append(
            f"{representative['case']} slowest pre-provider stage was "
            f"{slowest['stage']} at P99 {slowest['p99_ms']:.2f} ms."
        )

    postgres_waits = sum(item["postgres"]["waits"] for item in diagnosed)
    postgres_wait_ms = sum(item["postgres"]["wait_ms"] for item in diagnosed)
    redis_waits = sum(item["redis"]["waits"] for item in diagnosed)
    redis_timeouts = sum(item["redis"]["timeouts"] for item in diagnosed)
    findings.append(
        f"Diagnosed cases recorded {postgres_waits} PostgreSQL pool waits "
        f"({postgres_wait_ms:.1f} ms cumulative) and {redis_waits} Redis pool waits "
        f"with {redis_timeouts} timeouts."
    )

    errors = defaultdict(int)
    for item in diagnosed:
        for error in item["errors"]:
            errors[error["code"]] += error["count"]
    if errors:
        code, count = max(errors.items(), key=lambda item: item[1])
        findings.append(
            f"Most frequent classified gateway error was {code}: {count} responses."
        )
    return findings


def fmt(value, digits=2):
    if value is None:
        return "-"
    if isinstance(value, str):
        return value
    if not math.isfinite(number(value)):
        return "-"
    return f"{number(value):.{digits}f}"


def markdown_table(headers, rows):
    output = [
        "| " + " | ".join(headers) + " |",
        "| " + " | ".join("---" for _ in headers) + " |",
    ]
    output.extend("| " + " | ".join(map(str, row)) + " |" for row in rows)
    return "\n".join(output)


def write_markdown(path, summary):
    lines = ["# Model-Velo three-host benchmark summary", ""]
    for finding in summary["findings"]:
        lines.append(f"- {finding}")
    lines.append("")

    capacity = summary["capacity"]
    if capacity:
        lines.extend(
            [
                "## Closed-loop capacity",
                "",
                markdown_table(
                    ["target", "concurrency", "trials", "RPS", "success", "P95 ms", "P99 ms"],
                    [
                        [
                            row["target"],
                            row["load"],
                            row["trials"],
                            fmt(row["throughput_rps"], 1),
                            fmt(row["success_rate"] * 100, 3) + "%",
                            fmt(row["p95_ms"]),
                            fmt(row["p99_ms"]),
                        ]
                        for row in capacity
                    ],
                ),
                "",
            ]
        )

    rate = summary["rate_sweep"]
    if rate:
        lines.extend(
            [
                "## Open-loop rate sweep",
                "",
                markdown_table(
                    ["target RPS", "trials", "achieved RPS", "success", "drops", "P95 ms", "P99 ms"],
                    [
                        [
                            row["load"],
                            row["trials"],
                            fmt(row["throughput_rps"], 1),
                            fmt(row["success_rate"] * 100, 3) + "%",
                            fmt(row["dropped_iterations"], 0),
                            fmt(row["p95_ms"]),
                            fmt(row["p99_ms"]),
                        ]
                        for row in rate
                    ],
                ),
                "",
            ]
        )

    streams = summary["stream"]
    if streams:
        lines.extend(
            [
                "## SSE detail",
                "",
                markdown_table(
                    ["case", "success", "RPS", "first content P50", "P99", "total P99", "gap P99"],
                    [
                        [
                            row["case"],
                            fmt(row["success_rate"] * 100, 3) + "%",
                            fmt(row["throughput_rps"], 1),
                            fmt(row["first_content"].get("p50_ms")),
                            fmt(row["first_content"].get("p99_ms")),
                            fmt(row["total"].get("p99_ms")),
                            fmt(row["inter_chunk"].get("p99_ms")),
                        ]
                        for row in streams
                    ],
                ),
                "",
            ]
        )

    comparisons = summary["comparisons"]
    if comparisons:
        lines.extend(
            [
                "## Direct versus gateway",
                "",
                markdown_table(
                    ["case", "trials", "RPS ratio", "P50 delta ms", "P95 delta ms", "P99 delta ms"],
                    [
                        [
                            row["case"],
                            row["trials"],
                            fmt(row["throughput_ratio"] * 100, 1) + "%",
                            fmt(row["p50_delta_ms"]),
                            fmt(row["p95_delta_ms"]),
                            fmt(row["p99_delta_ms"]),
                        ]
                        for row in comparisons
                    ],
                ),
                "",
            ]
        )

    diagnostics = summary["diagnostic_cases"]
    if diagnostics:
        lines.extend(
            [
                "## Diagnostic cases",
                "",
                markdown_table(
                    [
                        "case",
                        "phase",
                        "RPS",
                        "success",
                        "P99 ms",
                        "200",
                        "429",
                        "5xx",
                        "upstream calls",
                    ],
                    [
                        [
                            row["case"],
                            row["phase"],
                            fmt(row["throughput_rps"], 1),
                            fmt(row["success_rate"] * 100, 3) + "%",
                            fmt(row["latency"]["p99_ms"]),
                            row["status_counts"]["200"],
                            row["status_counts"]["429"],
                            row["status_counts"]["5xx"],
                            int(number(row.get("upstream", {}).get("requests"))),
                        ]
                        for row in diagnostics
                    ],
                ),
                "",
            ]
        )

    performance = summary.get("performance_diagnostics", [])
    stage_rows = [
        [
            item["case"],
            stage["stage"],
            stage["count"],
            fmt(stage["avg_ms"], 3),
            fmt(stage["p50_ms"], 3),
            fmt(stage["p95_ms"], 3),
            fmt(stage["p99_ms"], 3),
        ]
        for item in performance
        for stage in item["stages"]
    ]
    if stage_rows:
        lines.extend(
            [
                "## Hot-path stage timing",
                "",
                "The `reliability` row contains queue, provider calls, retries, and "
                "fallbacks; it overlaps the `provider_queue` and `provider_call` rows.",
                "",
                markdown_table(
                    ["case", "stage", "count", "avg ms", "P50 ms", "P95 ms", "P99 ms"],
                    stage_rows,
                ),
                "",
                "### Dependency pools and Go runtime",
                "",
                markdown_table(
                    [
                        "case",
                        "PG waits",
                        "PG wait ms",
                        "PG in-use max",
                        "Redis waits",
                        "Redis wait ms",
                        "Redis pending max",
                        "process CPU avg",
                        "RSS max MB",
                        "goroutines max",
                    ],
                    [
                        [
                            item["case"],
                            item["postgres"]["waits"],
                            fmt(item["postgres"]["wait_ms"], 2),
                            item["postgres"]["in_use_max"],
                            item["redis"]["waits"],
                            fmt(item["redis"]["wait_ms"], 2),
                            item["redis"]["pending_max"],
                            fmt(item["runtime"]["process_cpu_avg_pct"], 1) + "%",
                            fmt(item["runtime"]["rss_max_mb"], 1),
                            item["runtime"]["goroutines_max"],
                        ]
                        for item in performance
                    ],
                ),
                "",
            ]
        )
        errors = [
            [item["case"], error["status"], error["code"], error["count"]]
            for item in performance
            for error in item["errors"]
        ]
        if errors:
            lines.extend(
                [
                    "### Gateway error codes",
                    "",
                    markdown_table(
                        ["case", "HTTP status", "error code", "count"],
                        errors,
                    ),
                    "",
                ]
            )

    resources = summary["resources"]
    if resources:
        lines.extend(
            [
                "## Container resources",
                "",
                markdown_table(
                    ["container", "samples", "CPU avg", "CPU max", "RSS avg MB", "RSS max MB"],
                    [
                        [
                            row["container"],
                            row["samples"],
                            fmt(row["cpu_avg_pct"]) + "%",
                            fmt(row["cpu_max_pct"]) + "%",
                            fmt(row["memory_avg_mb"]),
                            fmt(row["memory_max_mb"]),
                        ]
                        for row in resources
                    ],
                ),
                "",
            ]
        )

    client = summary["client_resource"]
    if client:
        lines.extend(
            [
                "## Client host resource",
                "",
                markdown_table(
                    [
                        "samples",
                        "CPU avg",
                        "CPU max",
                        "memory used max MB",
                        "load 1m max",
                        "loadgen RSS max MB",
                    ],
                    [
                        [
                            client["samples"],
                            fmt(client["cpu_avg_pct"]) + "%",
                            fmt(client["cpu_max_pct"]) + "%",
                            fmt(client["memory_used_max_mb"]),
                            fmt(client["load_1m_max"]),
                            fmt(client["loadgen_rss_max_mb"]),
                        ]
                    ],
                ),
                "",
            ]
        )

    usage_reconciliation = summary["usage_reconciliation"]
    if usage_reconciliation["expected_gateway_requests"] > 0:
        lines.extend(
            [
                "## Usage reconciliation",
                "",
                markdown_table(
                    ["expected requests", "stored events", "difference", "ratio"],
                    [
                        [
                            usage_reconciliation["expected_gateway_requests"],
                            usage_reconciliation["observed_usage_events"],
                            usage_reconciliation["difference"],
                            fmt(
                                usage_reconciliation["observed_ratio"] * 100,
                                3,
                            )
                            + "%",
                        ]
                    ],
                ),
                "",
            ]
        )

    lines.extend(["## Evidence", ""])
    for warning in summary["warnings"]:
        lines.append(f"- {warning}")
    path.write_text("\n".join(lines) + "\n", encoding="utf-8")


def main():
    if len(sys.argv) != 2:
        raise SystemExit(f"usage: {Path(sys.argv[0]).name} <result-directory>")
    result_dir = Path(sys.argv[1]).resolve()
    if not result_dir.is_dir():
        raise SystemExit(f"result directory not found: {result_dir}")

    cases = read_cases(result_dir)
    k6_rows = []
    stream_rows = []
    for case in cases:
        if case.get("mode") == "streamload":
            row = read_stream_case(result_dir, case)
            if row:
                stream_rows.append(row)
            continue
        row = read_k6_case(result_dir, case)
        if row:
            k6_rows.append(row)

    capacity = aggregate_k6(k6_rows, "capacity")
    rate_sweep = aggregate_k6(k6_rows, "rate", "gateway")
    comparisons = pair_comparisons(k6_rows)
    stream_pairs = stream_comparisons(stream_rows)
    diagnostic_cases = [
        row
        for row in k6_rows
        if row.get("phase")
        in {
            "cache",
            "ramp",
            "burst",
            "fault",
            "queue",
            "endurance",
            "rate-limit",
            "reliability",
        }
    ]
    started_at, ended_at = case_window(cases)
    resources = read_resources(result_dir, started_at, ended_at)
    client_resource = read_client_resource(result_dir)
    prometheus, prometheus_points = read_prometheus(
        result_dir, started_at, ended_at
    )
    performance_diagnostics = build_performance_diagnostics(
        prometheus_points, k6_rows
    )
    usage = read_usage_evidence(result_dir)
    usage_reconciliation = reconcile_usage(k6_rows, stream_rows, usage)

    warnings = []
    stats_paths = [
        path
        for path in result_dir.rglob("*stats*.jsonl")
        if path.name != "client-stats.jsonl"
    ]
    prometheus_paths = list(result_dir.rglob("*.promlog"))
    if stats_paths and not resources:
        warnings.append(
            "Docker stats files exist, but their timestamps do not overlap "
            "the benchmark case window."
        )
    elif not resources:
        warnings.append("Missing gateway/upstream Docker stats JSONL files.")
    if not client_resource:
        warnings.append("Missing client host resource samples.")
    if prometheus_paths and not prometheus:
        warnings.append(
            "Prometheus capture exists, but its timestamps do not overlap "
            "the benchmark case window."
        )
    elif not prometheus:
        warnings.append("Missing Prometheus time-series capture.")
    elif not any(item["stages"] for item in performance_diagnostics):
        warnings.append(
            "Prometheus capture does not contain per-stage request histograms; "
            "confirm the gateway image includes diagnostic metrics."
        )
    if not usage:
        warnings.append("Missing post-run Usage/PostgreSQL/Redis evidence.")
    elif usage.get("drain", {}).get("state") not in {"", "complete", None}:
        warnings.append("Usage drain timed out before post-run evidence was captured.")
    elif (
        usage_reconciliation["expected_gateway_requests"] > 0
        and usage_reconciliation["difference"] != 0
    ):
        warnings.append(
            "Stored Usage event count does not match measured gateway requests "
            "after excluding smoke, reliability, and rate-limit cases."
        )
    metadata = (result_dir / "client-metadata.txt").read_text(
        encoding="utf-8", errors="replace"
    ) if (result_dir / "client-metadata.txt").exists() else ""
    if "worktree=dirty" in metadata:
        warnings.append("Client repository worktree was dirty.")
    repetitions = [
        int(case["repetition"])
        for case in cases
        if str(case.get("repetition", "")).isdigit()
    ]
    if repetitions and max(repetitions) < 3:
        warnings.append("Fewer than three trials were recorded.")

    findings = capacity_findings(capacity, rate_sweep, comparisons, stream_pairs)
    findings.extend(
        actionable_findings(
            resources,
            client_resource,
            prometheus,
            k6_rows + stream_rows,
        )
    )
    findings.extend(performance_findings(performance_diagnostics))
    if not findings:
        if k6_rows or stream_rows:
            findings.append(
                "Cases were loaded, but no capacity or diagnostic phase was available "
                "for automatic diagnosis."
            )
        else:
            findings.append(
                "No complete benchmark cases were available for automatic diagnosis."
            )

    summary = {
        "result_directory": str(result_dir),
        "cases": k6_rows,
        "stream": stream_rows,
        "stream_comparisons": stream_pairs,
        "comparisons": comparisons,
        "diagnostic_cases": diagnostic_cases,
        "capacity": capacity,
        "rate_sweep": rate_sweep,
        "resources": resources,
        "client_resource": client_resource,
        "prometheus": prometheus,
        "performance_diagnostics": performance_diagnostics,
        "usage_evidence": usage,
        "usage_reconciliation": usage_reconciliation,
        "findings": findings,
        "warnings": warnings,
    }
    (result_dir / "summary.json").write_text(
        json.dumps(summary, ensure_ascii=False, indent=2) + "\n",
        encoding="utf-8",
    )
    write_markdown(result_dir / "summary.md", summary)
    from render_html import render

    render(result_dir)
    print(
        f"wrote {result_dir / 'summary.json'}, "
        f"{result_dir / 'summary.md'}, and {result_dir / 'summary.html'}"
    )


if __name__ == "__main__":
    main()
