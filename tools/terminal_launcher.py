"""Terminal-specific arguments used by desktop acceptance tools."""

import os
from pathlib import Path


def terminal_command(terminal, app_id, title, command):
    separator = '--' if terminal == 'kitty' else '-e'
    if terminal not in ('kitty', 'alacritty'):
        raise ValueError(f'unsupported terminal: {terminal}')
    # Do not enable single-instance mode: each test gets an independent OS window.
    return [terminal, '--class', app_id, '--title', title, separator, *map(str, command)]


def desktop_environment():
    env = dict(os.environ, XDG_RUNTIME_DIR=f'/run/user/{os.getuid()}')
    if not env.get('WAYLAND_DISPLAY') and not env.get('DISPLAY'):
        sockets = sorted(p for p in Path(env['XDG_RUNTIME_DIR']).glob('wayland-*') if p.is_socket())
        if sockets:
            env['WAYLAND_DISPLAY'] = sockets[0].name
    return env
