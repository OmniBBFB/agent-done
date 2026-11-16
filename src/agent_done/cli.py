"""Small command interface shared by terminals, hooks and the status bar."""

import argparse
import asyncio
import json
import logging
from logging.handlers import RotatingFileHandler
import os
from pathlib import Path
import sys

from . import __version__
from .paths import codex_socket, control_socket, private_dir, state_dir


def logging_setup():
    private_dir(state_dir())
    handler = RotatingFileHandler(state_dir() / "agent-done.log", maxBytes=1024 * 1024, backupCount=2)
    handler.setFormatter(logging.Formatter("%(asctime)s %(levelname)s %(name)s %(message)s"))
    logging.basicConfig(level=logging.INFO, handlers=[handler])


def hook():
    try:
        from .ipc import request
        event = json.loads(sys.stdin.read(8 * 1024 * 1024))
        # Never retain prompts, tool arguments, credentials or assistant text.
        selected = {k: event[k] for k in ("session_id", "turn_id", "hook_event_name", "source") if k in event}
        request({"op": "hook", "event": selected}, timeout=0.4)
    except (OSError, ValueError, RuntimeError):
        pass
    print("{}")


def main(argv=None):
    argv = list(sys.argv[1:] if argv is None else argv)
    if argv and argv[0] == "hook":
        hook()
        return
    try:
        logging_setup()
        if argv and argv[0] == "codex":
            from .codex import launch
            arguments = argv[1:]
            if arguments[:1] == ["--"]:
                arguments = arguments[1:]
            code = asyncio.run(launch(arguments))
            raise SystemExit(code if code >= 0 else 128 - code)
        parser = argparse.ArgumentParser(prog="agent-done", description="Codex 待介入会话导航")
        parser.add_argument("--version", action="version", version=__version__)
        commands = parser.add_subparsers(dest="command", required=True)
        commands.add_parser("codex", help="启动或恢复受跟踪的 Codex；参数原样传递")
        commands.add_parser("serve", help="运行本机提醒服务")
        commands.add_parser("waybar", help="向 Waybar 连续输出 JSON")
        commands.add_parser("jump", help="跳到最早需要介入且尚未查看的会话窗口")
        status = commands.add_parser("status", help="查看会话状态")
        status.add_argument("--json", action="store_true")
        ack = commands.add_parser("ack", help="手动清除提醒（默认全部）")
        ack.add_argument("--session")
        commands.add_parser("doctor", help="检查本机接入情况")
        commands.add_parser("install", help="安装用户入口、服务文件与 Waybar 模块，并备份配置")
        args = parser.parse_args(argv)
        if args.command == "serve":
            from .runtime import serve
            asyncio.run(serve())
        elif args.command == "waybar":
            from .waybar import stream
            try:
                asyncio.run(stream())
            except BrokenPipeError:
                pass
        elif args.command == "install":
            from .install import install
            print(json.dumps(install(), ensure_ascii=False, indent=2))
        elif args.command == "jump":
            from .navigation import jump
            target = jump()
            print(f"已请求聚焦窗口 {target['window_id']}（{target['session']}）" if target else "没有可跳转的待介入会话")
        elif args.command == "doctor":
            from .ipc import request
            checks = {"version": __version__, "codex_socket": str(codex_socket()), "codex_available": codex_socket().exists(),
                      "control_socket": str(control_socket())}
            try:
                result = request({"op": "diagnostics"})
                checks.update(service_available=True, pending_count=result["snapshot"]["pending_count"],
                              niri_connected=result["snapshot"]["niri_connected"],
                              desktop_backend=result["snapshot"].get("desktop_backend", "niri"),
                              desktop_connected=result["snapshot"].get("desktop_connected", result["snapshot"]["niri_connected"]),
                              desktop_error=result["snapshot"].get("desktop_error", ""),
                              status_subscribers=result.get("subscribers", 0), recent_hooks=result["hooks"])
                checks["experimental"] = checks["desktop_backend"] in ("macos", "yabai")
            except (OSError, RuntimeError, ValueError):
                checks["service_available"] = False
            print(json.dumps(checks, ensure_ascii=False, indent=2))
        else:
            from .ipc import request
            result = request({"op": "ack", "session": args.session} if args.command == "ack" else {"op": "status"})
            snapshot = result["snapshot"]
            if args.command == "status" and not args.json:
                print(f"待处理 {snapshot['pending_count']} 个会话")
                for session in snapshot["sessions"]:
                    mark = "*" if session["pending"] else " "
                    print(f"{mark} {session['id']}  {session['phase']}  {session['title'] or Path(session['cwd']).name}")
            else:
                print(json.dumps(snapshot, ensure_ascii=False, indent=2))
    except KeyboardInterrupt:
        raise SystemExit(130)
    except (OSError, ValueError, RuntimeError, asyncio.TimeoutError) as error:
        print(f"agent-done：{error}", file=sys.stderr)
        raise SystemExit(1)
