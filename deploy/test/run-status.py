#!/usr/bin/env python3
"""TEST ONLY. Prints a run's status and its steps, or waits for it to finish.

Usage: run-status.py BASE_URL RUN_ID [--wait SECONDS]
Exits 0 if the run succeeded (or, without --wait, whatever its status), 1 otherwise.
"""
import json
import sys
import time
import urllib.request


def get(base, path):
    with urllib.request.urlopen(base + path, timeout=10) as r:
        return json.load(r)


def show(run):
    print(f"run {run['id']} #{run.get('number')}: {run['status']}")
    for s in run.get("step_list") or []:
        print(f"  {s['id']:<10} {s['status']:<10} attempt {s['attempt']}  runner {s.get('runner') or '-'}  {s.get('error') or ''}")


def main():
    base, run_id = sys.argv[1].rstrip("/"), sys.argv[2]
    wait = float(sys.argv[4]) if len(sys.argv) > 4 and sys.argv[3] == "--wait" else 0
    deadline = time.time() + wait
    while True:
        try:
            run = get(base, f"/api/v1/runs/{run_id}")
        except Exception as e:  # a server may be restarting; keep asking until the deadline
            print(f"  (asking again: {e})")
            run = None
        if run and (not wait or run["status"] in ("succeeded", "failed", "canceled")):
            show(run)
            sys.exit(0 if (not wait or run["status"] == "succeeded") else 1)
        if time.time() > deadline:
            if run:
                show(run)
            print("timed out")
            sys.exit(1)
        time.sleep(2)


if __name__ == "__main__":
    main()
