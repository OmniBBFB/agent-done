"""Linux acceptance clients for the Go service, using only the standard library."""

import json
import os
from pathlib import Path
import socket
import subprocess


def binary():
    return Path(os.environ.get("AGENT_DONE_BINARY", Path.home() / ".local/bin/agent-done")).resolve()


def control_socket():
    if explicit := os.environ.get("AGENT_DONE_RUNTIME_DIR"):
        return Path(explicit) / "control.sock"
    base = os.environ.get("XDG_RUNTIME_DIR")
    default_runtime = Path(f"/run/user/{os.getuid()}")
    if not base and default_runtime.is_dir():
        base = default_runtime
    if base:
        return Path(base) / "agent-done/control.sock"
    state_home = Path(os.environ.get("XDG_STATE_HOME") or Path.home() / ".local/state")
    return Path(os.environ.get("AGENT_DONE_STATE_DIR") or state_home / "agent-done") / "run/control.sock"


def request(message, timeout=1):
    with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as client:
        client.settimeout(timeout)
        client.connect(str(control_socket()))
        client.sendall(json.dumps(message).encode() + b"\n")
        with client.makefile("rb") as stream:
            response = json.loads(stream.readline(4 * 1024 * 1024))
    if not response.get("ok"):
        raise RuntimeError(response.get("error", "agent-done request failed"))
    return response


def niri_socket():
    if explicit := os.environ.get("NIRI_SOCKET"):
        return explicit
    runtime = Path(os.environ.get("XDG_RUNTIME_DIR") or f"/run/user/{os.getuid()}")
    candidates = [path for path in runtime.glob("niri.*.sock") if path.is_socket()]
    return str(max(candidates, key=lambda path: path.stat().st_mtime)) if candidates else None


def niri_query(command):
    if not (path := niri_socket()):
        raise RuntimeError("没有找到 niri socket")
    result = subprocess.run(["niri", "msg", "--json", command], env=dict(os.environ, NIRI_SOCKET=path),
                            capture_output=True, text=True, timeout=4, check=True)
    return json.loads(result.stdout)


def start_ticks(pid):
    try:
        return (Path("/proc") / str(pid) / "stat").read_text().rsplit(")", 1)[1].split()[19]
    except (OSError, IndexError):
        return None


def same_process(pid, creation):
    current = start_ticks(pid)
    return current is not None and current == str(creation)
