#!/usr/bin/env python3

import json
import re
import sys
from datetime import datetime, timezone
from pathlib import Path


PLACEHOLDER = "__MODEL_VELO_REPORT_DATA__"


def read_text(path):
    try:
        return path.read_text(encoding="utf-8", errors="replace")
    except OSError:
        return ""


def read_json(path):
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError):
        return {}


def parse_metadata(text):
    values = {}
    for line in text.splitlines():
        if "=" in line:
            key, value = line.split("=", 1)
            values[key.strip()] = value.strip()
    return values


def read_json_lines(path):
    rows = []
    for line in read_text(path).splitlines():
        try:
            rows.append(json.loads(line))
        except json.JSONDecodeError:
            continue
    return rows


def attempt_status(path, current):
    if path == current:
        return "complete"
    name = path.name.lower()
    if "smoke-failed" in name:
        return "smoke failed"
    if "interrupted" in name:
        return "interrupted"
    if "failed" in name:
        return "earlier failed run"
    return "other"


def collect_attempts(result_dir):
    attempts = []
    for path in sorted(item for item in result_dir.parent.iterdir() if item.is_dir()):
        files = [item for item in path.rglob("*") if item.is_file()]
        metadata_text = read_text(path / "client-metadata.txt")
        metadata = parse_metadata(metadata_text)
        cases_path = path / "cases.tsv"
        case_count = 0
        if cases_path.exists():
            case_count = max(0, len(read_text(cases_path).splitlines()) - 1)
        attempts.append(
            {
                "name": path.name,
                "status": attempt_status(path, result_dir),
                "case_count": case_count,
                "file_count": len(files),
                "bytes": sum(item.stat().st_size for item in files),
                "commit": metadata.get("commit", ""),
                "started_at": metadata.get("started_at", ""),
                "ended_at": metadata.get("ended_at", ""),
                "has_summary": (path / "summary.json").exists(),
            }
        )
    return attempts


def collect_artifacts(result_dir):
    artifacts = []
    for path in sorted(
        item
        for item in result_dir.rglob("*")
        if item.is_file() and item.name != "summary.html"
    ):
        relative = path.relative_to(result_dir).as_posix()
        artifacts.append(
            {
                "path": relative,
                "bytes": path.stat().st_size,
                "modified_at": datetime.fromtimestamp(
                    path.stat().st_mtime, timezone.utc
                ).isoformat(),
            }
        )
    return artifacts


def reliability_checks(result_dir):
    checks = []
    for line in read_text(result_dir / "reliability.log").splitlines():
        match = re.match(r"^\s*✓\s+(.+?)\s*$", line)
        if match and "rate==" not in match.group(1):
            checks.append(match.group(1))
    return checks


def parse_iso(value):
    try:
        return datetime.fromisoformat(str(value).replace("Z", "+00:00"))
    except (TypeError, ValueError):
        return None


def capture_relation(first, last, run_start, run_end):
    if not all((first, last, run_start, run_end)):
        return "unknown"
    if last < run_start:
        return "before"
    if first > run_end:
        return "after"
    return "overlap"


def parse_size(value):
    match = re.match(r"^\s*([0-9.]+)\s*([KMGT]?i?B)\s*$", str(value))
    if not match:
        return 0.0
    amount = float(match.group(1))
    unit = match.group(2)
    powers = {"B": 0, "KB": 1, "KiB": 1, "MB": 2, "MiB": 2,
              "GB": 3, "GiB": 3, "TB": 4, "TiB": 4}
    base = 1024 if "iB" in unit else 1000
    return amount * base ** powers[unit]


def collect_server_captures(result_dir, metadata):
    run_start = parse_iso(metadata.get("started_at"))
    run_end = parse_iso(metadata.get("ended_at"))
    captures = []
    for path in sorted(result_dir.rglob("*stats*.jsonl")):
        if path.name == "client-stats.jsonl":
            continue
        timestamps = []
        containers = {}
        for payload in read_json_lines(path):
            timestamp = parse_iso(payload.get("timestamp"))
            if timestamp:
                timestamps.append(timestamp)
            stats = payload.get("stats", {})
            name = stats.get("Name") or stats.get("Container") or "unknown"
            aggregate = containers.setdefault(
                name,
                {"samples": 0, "cpu_sum_pct": 0.0, "cpu_max_pct": 0.0,
                 "memory_max_mb": 0.0},
            )
            cpu = float(str(stats.get("CPUPerc", "0")).rstrip("%") or 0)
            memory = str(stats.get("MemUsage", "")).split("/", 1)[0]
            aggregate["samples"] += 1
            aggregate["cpu_sum_pct"] += cpu
            aggregate["cpu_max_pct"] = max(aggregate["cpu_max_pct"], cpu)
            aggregate["memory_max_mb"] = max(
                aggregate["memory_max_mb"], parse_size(memory) / (1024 * 1024)
            )
        first = min(timestamps) if timestamps else None
        last = max(timestamps) if timestamps else None
        for aggregate in containers.values():
            aggregate["cpu_avg_pct"] = (
                aggregate.pop("cpu_sum_pct") / max(1, aggregate["samples"])
            )
        captures.append(
            {
                "path": path.relative_to(result_dir).as_posix(),
                "kind": "docker-stats",
                "first": first.isoformat() if first else "",
                "last": last.isoformat() if last else "",
                "relation": capture_relation(first, last, run_start, run_end),
                "samples": len(timestamps),
                "containers": containers,
            }
        )

    for path in sorted(result_dir.rglob("*.promlog")):
        timestamps = [
            parse_iso(match.group(1))
            for match in re.finditer(
                r"^# snapshot\s+(\S+)\s*$",
                read_text(path),
                flags=re.MULTILINE,
            )
        ]
        timestamps = [timestamp for timestamp in timestamps if timestamp]
        first = min(timestamps) if timestamps else None
        last = max(timestamps) if timestamps else None
        captures.append(
            {
                "path": path.relative_to(result_dir).as_posix(),
                "kind": "prometheus",
                "first": first.isoformat() if first else "",
                "last": last.isoformat() if last else "",
                "relation": capture_relation(first, last, run_start, run_end),
                "samples": len(timestamps),
                "containers": {},
            }
        )
    return captures


def render(result_dir):
    summary_path = result_dir / "summary.json"
    if not summary_path.exists():
        raise SystemExit(f"summary not found: {summary_path}")

    template_path = Path(__file__).with_name("report-template.html")
    template = read_text(template_path)
    if PLACEHOLDER not in template:
        raise SystemExit(f"report placeholder not found in {template_path}")

    metadata_text = read_text(result_dir / "client-metadata.txt")
    metadata = parse_metadata(metadata_text)
    report_data = {
        "summary": read_json(summary_path),
        "metadata": metadata,
        "metadata_text": metadata_text,
        "client_stats": read_json_lines(result_dir / "client-stats.jsonl"),
        "attempts": collect_attempts(result_dir),
        "artifacts": collect_artifacts(result_dir),
        "reliability_checks": reliability_checks(result_dir),
        "server_captures": collect_server_captures(result_dir, metadata),
        "generated_at": datetime.now(timezone.utc).isoformat(),
    }
    encoded = json.dumps(
        report_data, ensure_ascii=False, separators=(",", ":")
    ).replace("<", "\\u003c")
    output = result_dir / "summary.html"
    output.write_text(template.replace(PLACEHOLDER, encoded), encoding="utf-8")
    print(f"wrote {output}")


def main():
    if len(sys.argv) != 2:
        raise SystemExit(f"usage: {Path(sys.argv[0]).name} <result-directory>")
    result_dir = Path(sys.argv[1]).resolve()
    if not result_dir.is_dir():
        raise SystemExit(f"result directory not found: {result_dir}")
    render(result_dir)


if __name__ == "__main__":
    main()
