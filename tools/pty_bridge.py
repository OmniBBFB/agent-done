"""Relay terminal input and output for desktop acceptance tools."""

import fcntl
import json
import os
import select
import signal
import termios
import tty


def bridge_terminal(pid, master, artifacts, on_exit):
    """Relay a forked PTY and report the child's wait status after cleanup."""
    def resize(*unused):
        size = fcntl.ioctl(0, termios.TIOCGWINSZ, b"\0" * 8)
        fcntl.ioctl(master, termios.TIOCSWINSZ, size)

    previous_mode = None
    previous_resize = signal.getsignal(signal.SIGWINCH)
    status = None
    offset = 0
    controls = artifacts / "input.jsonl"
    try:
        resize()
        signal.signal(signal.SIGWINCH, resize)
        previous_mode = termios.tcgetattr(0)
        tty.setraw(0)
        with (artifacts / "tui.raw").open("ab", buffering=0) as log:
            while True:
                ready, _, _ = select.select([0, master], [], [], 0.1)
                if 0 in ready:
                    data = os.read(0, 65536)
                    if data:
                        os.write(master, data)
                if master in ready:
                    try:
                        data = os.read(master, 65536)
                    except OSError:
                        break
                    if not data:
                        break
                    log.write(data)
                    os.write(1, data)
                if controls.exists():
                    with controls.open() as stream:
                        stream.seek(offset)
                        for line in stream:
                            os.write(master, json.loads(line)["data"].encode())
                        offset = stream.tell()
                done, child_status = os.waitpid(pid, os.WNOHANG)
                if done:
                    status = child_status
                    break
    finally:
        signal.signal(signal.SIGWINCH, previous_resize)
        try:
            if previous_mode is not None:
                termios.tcsetattr(0, termios.TCSADRAIN, previous_mode)
        finally:
            os.close(master)
            if status is None:
                _, status = os.waitpid(pid, 0)
            on_exit(status)
