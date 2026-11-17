"""Terminal-specific arguments used by desktop acceptance tools."""

import os
from pathlib import Path
import shutil
import sys


def terminal_command(terminal, app_id, title, command):
    separator = '--' if terminal == 'kitty' else '-e'
    if terminal not in ('kitty', 'alacritty'):
        raise ValueError(f'unsupported terminal: {terminal}')
    executable = shutil.which(terminal)
    if executable is None and sys.platform == 'darwin':
        app = 'Alacritty' if terminal == 'alacritty' else 'kitty'
        candidate = Path('/Applications') / f'{app}.app/Contents/MacOS/{terminal}'
        if candidate.is_file():
            executable = str(candidate)
    # Do not enable single-instance mode: each test gets an independent OS window.
    return [executable or terminal, '--class', app_id, '--title', title, separator, *map(str, command)]


def desktop_environment():
    if sys.platform == 'darwin':
        return dict(os.environ)
    env = dict(os.environ, XDG_RUNTIME_DIR=f'/run/user/{os.getuid()}')
    if not env.get('WAYLAND_DISPLAY') and not env.get('DISPLAY'):
        sockets = sorted(p for p in Path(env['XDG_RUNTIME_DIR']).glob('wayland-*') if p.is_socket())
        if sockets:
            env['WAYLAND_DISPLAY'] = sockets[0].name
    return env
