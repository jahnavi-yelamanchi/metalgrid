#!/usr/bin/env python3
"""Job load generator: submits AcceleratorJob requests against the MetalGrid
REST API concurrently and reports throughput/latency. stdlib-only — no venv
needed to run a load test.
"""
import argparse
import concurrent.futures
import json
import random
import time
import urllib.error
import urllib.request


def submit_job(base_url, token, team, priority, sleep_seconds):
    body = json.dumps({
        "team": team,
        "image": "busybox:1.36",
        "command": ["sh", "-c", f"sleep {sleep_seconds}"],
        "acceleratorType": "mock-gpu",
        "acceleratorCount": 1,
        "priority": priority,
    }).encode()
    req = urllib.request.Request(
        f"{base_url}/v1/jobs", data=body, method="POST",
        headers={"Content-Type": "application/json"},
    )
    if token:
        req.add_header("Authorization", f"Bearer {token}")

    start = time.monotonic()
    try:
        with urllib.request.urlopen(req, timeout=10) as resp:
            resp.read()
            return resp.status, time.monotonic() - start
    except urllib.error.HTTPError as e:
        e.read()
        return e.code, time.monotonic() - start
    except Exception:
        return 0, time.monotonic() - start


def percentile(sorted_values, p):
    if not sorted_values:
        return 0.0
    idx = min(len(sorted_values) - 1, int(len(sorted_values) * p))
    return sorted_values[idx]


def main():
    p = argparse.ArgumentParser(description="MetalGrid job load generator")
    p.add_argument("--url", default="http://localhost:8080", help="apiserver base URL")
    p.add_argument("--count", type=int, default=200, help="total jobs to submit")
    p.add_argument("--concurrency", type=int, default=20, help="concurrent in-flight requests")
    p.add_argument("--teams", default="platform,team-a,team-b", help="comma-separated team names to mix across")
    p.add_argument("--job-sleep", type=int, default=1, help="seconds each submitted job's container sleeps")
    p.add_argument("--token", default=None, help="bearer token, if the target apiserver has auth enabled")
    args = p.parse_args()

    teams = args.teams.split(",")
    results = []
    start = time.monotonic()

    with concurrent.futures.ThreadPoolExecutor(max_workers=args.concurrency) as ex:
        futures = [
            ex.submit(
                submit_job, args.url, args.token,
                random.choice(teams), random.choice([-100, 0, 100]), args.job_sleep,
            )
            for _ in range(args.count)
        ]
        checkpoint = max(1, args.count // 20)
        for i, f in enumerate(concurrent.futures.as_completed(futures), 1):
            results.append(f.result())
            if i % checkpoint == 0 or i == args.count:
                print(f"{i}/{args.count} submitted")

    elapsed = time.monotonic() - start
    statuses = {}
    for status, _ in results:
        statuses[status] = statuses.get(status, 0) + 1
    latencies = sorted(lat for _, lat in results)

    print(f"\n{args.count} jobs in {elapsed:.1f}s ({args.count / elapsed:.1f} req/s)")
    print("status codes:", statuses)
    print(
        "latency p50=%.0fms p95=%.0fms p99=%.0fms max=%.0fms"
        % (
            percentile(latencies, 0.50) * 1000,
            percentile(latencies, 0.95) * 1000,
            percentile(latencies, 0.99) * 1000,
            (latencies[-1] if latencies else 0) * 1000,
        )
    )


if __name__ == "__main__":
    main()
