#!/usr/bin/env python3
"""Read-only Linux probe for rollout ownership and start-time candidates.

Run on the desktop host. Prints identities and timestamps, never prompts or
complete process environments. Candidate matches are not proven bindings.
"""

import datetime as dt
import json
import os
from pathlib import Path


def session_metadata(path):
    try:
        with path.open() as stream:
            event = json.loads(stream.readline(1024 * 1024))
        if event.get("type") != "session_meta":
            return None
        payload = event["payload"]
        stamp = payload.get("timestamp") or event["timestamp"]
        return {
            "session_id": payload["id"],
            "created": dt.datetime.fromisoformat(stamp.replace("Z", "+00:00")).timestamp(),
            "mtime": path.stat().st_mtime,
            "cwd": payload.get("cwd"),
        }
    except (OSError, ValueError, KeyError, TypeError):
        return None


def probe():
    boot = next(
        int(line.split()[1])
        for line in Path("/proc/stat").read_text().splitlines()
        if line.startswith("btime ")
    )
    ticks = os.sysconf("SC_CLK_TCK")
    directory_cache = {}
    results = []
    for proc in Path("/proc").iterdir():
        if not proc.name.isdigit():
            continue
        try:
            if proc.joinpath("comm").read_text().strip() != "codex":
                continue
            stat = proc.joinpath("stat").read_text().rsplit(")", 1)[1].split()
            started = boot + int(stat[19]) / ticks
            env = dict(
                item.split(b"=", 1)
                for item in proc.joinpath("environ").read_bytes().split(b"\0")
                if b"=" in item
            )
            home = Path(os.fsdecode(env.get(b"CODEX_HOME", os.fsencode(Path.home() / ".codex"))))
            cwd = os.readlink(proc / "cwd")
            tty = os.readlink(proc / "fd/0")
            opened = set()
            for fd in proc.joinpath("fd").iterdir():
                try:
                    path = Path(os.readlink(fd))
                    if path.name.startswith("rollout-") and path.suffix == ".jsonl":
                        metadata = session_metadata(path)
                        if metadata:
                            opened.add(metadata["session_id"])
                except OSError:
                    continue
        except (OSError, ValueError):
            continue

        candidates = []
        if tty.startswith("/dev/pts/"):
            day = dt.datetime.fromtimestamp(started, dt.timezone.utc).date()
            for offset in (-1, 0, 1):
                directory = home / "sessions" / (day + dt.timedelta(days=offset)).strftime("%Y/%m/%d")
                if directory not in directory_cache:
                    directory_cache[directory] = [
                        item for path in directory.glob("rollout-*.jsonl")
                        if (item := session_metadata(path)) is not None
                    ]
                for item in directory_cache[directory]:
                    delta = item["created"] - started
                    if abs(delta) <= 90:
                        candidates.append({
                            "session_id": item["session_id"],
                            "delta_seconds": round(delta, 2),
                            "cwd_matches": item["cwd"] == cwd,
                            "mtime": item["mtime"],
                        })
        candidates.sort(key=lambda item: item["mtime"], reverse=True)
        results.append({
            "pid": int(proc.name),
            "tty": tty if tty.startswith("/dev/pts/") else None,
            "started_utc": dt.datetime.fromtimestamp(started, dt.timezone.utc).isoformat(),
            "opened_rollout_sessions": sorted(opened),
            "time_candidates": candidates,
            "agentop_mtime_choice": candidates[0]["session_id"] if candidates else None,
        })
    return sorted(results, key=lambda item: item["pid"])


if __name__ == "__main__":
    print(json.dumps(probe(), ensure_ascii=False, indent=2))
