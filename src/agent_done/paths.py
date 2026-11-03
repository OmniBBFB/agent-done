"""Per-user paths shared by launcher, hooks, service and status bar."""

import os
from pathlib import Path


def state_dir():
    return Path(os.environ.get("AGENT_DONE_STATE_DIR") or
                Path(os.environ.get("XDG_STATE_HOME", Path.home() / ".local/state")) / "agent-done")


def runtime_dir():
    base = os.environ.get("XDG_RUNTIME_DIR")
    if not base and Path(f"/run/user/{os.getuid()}").is_dir():
        base = f"/run/user/{os.getuid()}"
    return Path(base) / "agent-done" if base else state_dir() / "run"


def control_socket():
    return runtime_dir() / "control.sock"


def codex_socket():
    return Path(os.environ.get("CODEX_HOME", Path.home() / ".codex")) / "app-server-control/app-server-control.sock"


def private_dir(path):
    path.mkdir(parents=True, exist_ok=True, mode=0o700)
    path.chmod(0o700)
    return path
