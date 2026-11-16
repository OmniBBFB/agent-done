"""Opaque process creation identities and ancestry on Linux and macOS."""

import os
from pathlib import Path
import sys


def _mac_value(pid, attribute, *args, default=None, **kwargs):
    try:
        import psutil
    except ImportError as error:
        raise RuntimeError("macOS 进程接入需要 psutil，请执行 uv sync") from error
    try:
        return getattr(psutil.Process(pid), attribute)(*args, **kwargs)
    except psutil.Error:
        return default


def start_ticks(pid):
    # Preserve existing Linux database identities; callers treat them as opaque.
    if sys.platform == "darwin":
        created = _mac_value(pid, "create_time")
        return repr(created) if created is not None else None
    try:
        return (Path("/proc") / str(pid) / "stat").read_text().rsplit(")", 1)[1].split()[19]
    except (OSError, IndexError):
        return None


def parent_pid(pid):
    if sys.platform == "darwin":
        return _mac_value(pid, "ppid")
    try:
        return int((Path("/proc") / str(pid) / "stat").read_text().rsplit(")", 1)[1].split()[1])
    except (OSError, IndexError, ValueError):
        return None


def ancestors(pid=None):
    result = []
    pid = os.getpid() if pid is None else pid
    while pid and pid > 1 and pid not in result:
        result.append(pid)
        pid = parent_pid(pid)
    return result


def codex_pid(launcher_pid):
    pending = [launcher_pid]
    visited = set()
    while pending:
        pid = pending.pop(0)
        if pid in visited:
            continue
        visited.add(pid)
        if sys.platform == "darwin":
            if _mac_value(pid, "name") == "codex":
                return pid
            pending.extend(child.pid for child in _mac_value(pid, "children", default=[]))
        else:
            proc = Path("/proc") / str(pid)
            try:
                if proc.joinpath("comm").read_text().strip() == "codex":
                    return pid
                pending.extend(int(x) for x in (proc / "task" / str(pid) / "children").read_text().split())
            except OSError:
                pass
    return launcher_pid


def same_process(pid, creation):
    current = start_ticks(pid)
    return current is not None and creation not in (None, "", "None") and current == str(creation)
