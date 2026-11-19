#!/usr/bin/env python3
"""Validate desktop service restarts, including upgrading the first build.

Only agent-done's own Waybar output processes are restarted. Waybar reconnects
them automatically. No Codex processes or desktop windows are terminated.
"""

import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import time

from acceptance_run import record, wait
from acceptance_support import request


def main(artifacts):
    env = dict(os.environ, XDG_RUNTIME_DIR=f"/run/user/{os.getuid()}",
               DBUS_SESSION_BUS_ADDRESS=f"unix:path=/run/user/{os.getuid()}/bus")
    os.environ.update({k: env[k] for k in ("XDG_RUNTIME_DIR", "DBUS_SESSION_BUS_ADDRESS")})
    old_clients = []
    for proc in Path("/proc").iterdir():
        if not proc.name.isdigit():
            continue
        try:
            args = proc.joinpath("cmdline").read_bytes().decode().split("\0")[:-1]
        except (OSError, UnicodeError):
            continue
        if args and args[-1] == "waybar" and any(Path(arg).name == "agent-done" for arg in args):
            old_clients.append(int(proc.name))
    # Upgrade the original build, whose shutdown waits for subscribers. Release
    # those old connections; current builds close them themselves.
    process = subprocess.Popen(["systemctl", "--user", "restart", "agent-done.service"], env=env)
    time.sleep(0.2)
    for pid in old_clients:
        try:
            os.kill(pid, signal.SIGTERM)
        except ProcessLookupError:
            pass
    if process.wait(timeout=10):
        raise RuntimeError("service upgrade restart failed")

    def ready():
        try:
            result = request({"op": "diagnostics"})
            return result if result["snapshot"]["niri_connected"] and result["subscribers"] >= 2 else None
        except (OSError, ValueError):
            return None

    wait(ready, 10)
    started = time.monotonic()
    subprocess.run(["systemctl", "--user", "restart", "agent-done.service"],
                   env=env, timeout=5, check=True)
    elapsed = time.monotonic() - started
    result = wait(ready, 10)
    assert result["snapshot"]["pending_count"] == 0
    record(artifacts, "installed_service_restart_and_waybar_reconnect",
           {"restart_seconds": elapsed, "diagnostics": result})
    print(json.dumps({"passed": True, "restart_seconds": elapsed}), flush=True)


if __name__ == "__main__":
    main(Path(sys.argv[1]))
