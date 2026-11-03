"""Codex integration: stable hooks, observed user sessions and transparent I/O."""

import asyncio
import copy
import json
import logging
import os
from pathlib import Path
import shlex
import shutil
import signal
import subprocess
import sys
import tempfile
import uuid

from websockets.asyncio.client import unix_connect
from websockets.asyncio.server import unix_serve
from websockets.exceptions import ConnectionClosed

from .desktop import codex_pid, start_ticks, window_identity
from .ipc import async_request, request
from .paths import codex_socket, private_dir, runtime_dir, state_dir
from .prompt_rules import CLIENT_EXIT_REASON
from .terminal import Terminal

LOG = logging.getLogger(__name__)
HOOK_EVENTS = ("SessionStart", "UserPromptSubmit", "Stop", "PermissionRequest", "Interrupt")
APPROVAL_METHODS = {
    "item/commandExecution/requestApproval", "item/fileChange/requestApproval",
    "item/permissions/requestApproval", "execCommandApproval", "applyPatchApproval",
}
QUESTION_METHODS = {"item/tool/requestUserInput", "tool/requestUserInput", "mcpServer/elicitation/request"}


def hook_command():
    return shlex.join([sys.executable, "-m", "agent_done", "hook"])


def hook_groups():
    return [{"hooks": [{"type": "command", "command": hook_command(), "timeout": 3}]}]


def with_hooks(config):
    """Preserve user settings and handlers; add our stable handler exactly once."""
    result = copy.deepcopy(config or {})
    nested = result.pop("hooks", {})
    for event, groups in nested.items():
        result.setdefault("hooks." + event, []).extend(groups)
    for event in HOOK_EVENTS:
        groups = result.setdefault("hooks." + event, [])
        if not any(h.get("command") == hook_command() for group in groups for h in group.get("hooks", [])):
            groups.extend(hook_groups())
    return result


def internal_thread(params):
    source = params.get("threadSource")
    return params.get("ephemeral") is True or source in ("thread_title", "subagent", "subAgent")


def rpc_key(value):
    return json.dumps(value, sort_keys=True, separators=(",", ":"))


class Observer:
    """Tracks only user conversations on one TUI connection. Never decides tools."""
    def __init__(self, connection, identity, emit):
        self.connection = connection
        self.identity = identity
        self.emit = emit
        self.client_requests = {}
        self.server_requests = {}
        self.sessions = set()
        self.active_session = None
        self.action_turns = {}

    async def client(self, message):
        result = copy.deepcopy(message)
        method = result.get("method")
        params = result.get("params") or {}
        key = rpc_key(result.get("id"))
        if method and "id" in result:
            self.client_requests[key] = (method, params.get("threadId"), internal_thread(params))
        if method in ("thread/start", "thread/resume", "thread/fork") and not internal_thread(params):
            params["config"] = with_hooks(params.get("config"))
            result["params"] = params
        if not method and "id" in result and key in self.server_requests:
            sid, request_key = self.server_requests.pop(key)
            await self.emit({"op": "request_close", "session": sid, "key": request_key})
        return result

    async def server(self, message):
        method = message.get("method")
        params = message.get("params") or {}
        sid = params.get("threadId") or params.get("thread_id")
        if "id" in message and not method:
            context = self.client_requests.pop(rpc_key(message["id"]), None)
            if context:
                request_method, requested_sid, internal = context
                thread = (message.get("result") or {}).get("thread")
                if thread and request_method in ("thread/start", "thread/resume", "thread/fork") and not internal:
                    sid = thread["id"]
                    self.sessions.add(sid)
                    self.active_session = sid
                    await self.emit({"op": "bind", "connection": self.connection, "session": sid,
                                     "identity": self.identity(), "title": thread.get("name") or "",
                                     "cwd": thread.get("cwd") or ""})
                elif request_method == "thread/unsubscribe" and requested_sid in self.sessions and "error" not in message:
                    await self.emit({"op": "deactivate", "connection": self.connection, "session": requested_sid})
                    if self.active_session == requested_sid:
                        self.active_session = None
                elif request_method == "turn/start" and requested_sid in self.sessions:
                    turn = (message.get("result") or {}).get("turn")
                    if turn:
                        action = self.action_state(requested_sid, turn["id"])
                        action.add("owned")
                        return self.take_action(requested_sid, turn["id"])
        if sid not in self.sessions:
            return
        if method == "turn/started":
            await self.emit({"op": "running", "session": sid, "turn": params["turn"]["id"]})
        elif method == "turn/completed":
            turn = params["turn"]
            await self.emit({"op": "complete", "session": sid, "turn": turn["id"], "status": turn["status"]})
            self.action_state(sid, turn["id"]).add("completed")
            return self.take_action(sid, turn["id"])
        elif method == "hook/completed":
            run = params.get("run") or {}
            if (params.get("turnId") and run.get("eventName") == "userPromptSubmit"
                    and run.get("handlerType") == "command" and run.get("status") == "blocked"
                    and any(entry.get("text") == CLIENT_EXIT_REASON
                            for entry in run.get("entries", []) if entry.get("kind") == "feedback")):
                self.action_state(sid, params["turnId"]).add("client_exit")
                return self.take_action(sid, params["turnId"])
        elif method in APPROVAL_METHODS | QUESTION_METHODS and "id" in message:
            raw_key = rpc_key(message["id"])
            request_key = self.connection + ":" + raw_key
            self.server_requests[raw_key] = (sid, request_key)
            await self.emit({"op": "request_open", "connection": self.connection, "session": sid,
                             "turn": params.get("turnId") or params.get("turn_id") or "", "key": request_key,
                             "kind": "approval" if method in APPROVAL_METHODS else "question"})
        elif method == "serverRequest/resolved":
            raw_key = rpc_key(params.get("requestId"))
            context = self.server_requests.pop(raw_key, (sid, self.connection + ":" + raw_key))
            await self.emit({"op": "request_close", "session": context[0], "key": context[1]})
        elif method == "thread/name/updated":
            await self.emit({"op": "title", "session": sid, "title": params.get("threadName") or params.get("name") or ""})
        elif method in ("thread/archived", "thread/deleted", "thread/closed"):
            await self.emit({"op": "remove", "session": sid})
            self.sessions.discard(sid)
            if self.active_session == sid:
                self.active_session = None

    def action_state(self, sid, tid):
        key = (sid, tid)
        if key not in self.action_turns:
            if len(self.action_turns) >= 128:
                del self.action_turns[next(iter(self.action_turns))]
            self.action_turns[key] = set()
        return self.action_turns[key]

    def take_action(self, sid, tid):
        state = self.action_state(sid, tid)
        if sid == self.active_session and state == {"owned", "completed", "client_exit"}:
            state.add("consumed")
            return "client_exit"


async def ensure_service():
    try:
        await async_request({"op": "status"}, timeout=0.3)
        return
    except (OSError, ValueError, RuntimeError, asyncio.TimeoutError):
        pass
    unit = Path.home() / ".config/systemd/user/agent-done.service"
    if unit.exists():
        process = await asyncio.create_subprocess_exec("systemctl", "--user", "start", "agent-done.service",
                    stdout=asyncio.subprocess.DEVNULL, stderr=asyncio.subprocess.DEVNULL)
        await asyncio.wait_for(process.wait(), 5)
    else:
        private_dir(state_dir())
        with (state_dir() / "service.log").open("a") as log:
            subprocess.Popen([sys.executable, "-m", "agent_done", "serve"], stdin=subprocess.DEVNULL,
                             stdout=log, stderr=log, start_new_session=True)
    for _ in range(50):
        try:
            await async_request({"op": "status"}, timeout=0.3)
            return
        except (OSError, ValueError, RuntimeError, asyncio.TimeoutError):
            await asyncio.sleep(0.1)
    raise RuntimeError("提醒服务未能启动")


async def launch(arguments):
    executable = shutil.which("codex")
    if not executable:
        raise RuntimeError("PATH 中没有 codex")
    if any(a in ("--help", "-h", "--version", "-V") for a in arguments):
        return subprocess.call([executable, *arguments])
    if any(a == "--no-daemon" or a == "--remote" or a.startswith("--remote=") for a in arguments):
        raise RuntimeError("此入口管理本机 daemon 连接，请移除 --remote / --no-daemon")
    try:
        identity_base = window_identity()
        await ensure_service()
    except (OSError, RuntimeError, asyncio.TimeoutError):
        print("agent-done：提醒暂不可用，继续启动 Codex。", file=sys.stderr)
        return subprocess.call([executable, *arguments])
    if not codex_socket().exists():
        process = await asyncio.create_subprocess_exec(executable, "app-server", "daemon", "start")
        if await process.wait():
            raise RuntimeError("无法启动 Codex daemon")
    private_dir(runtime_dir())
    owner_pid = os.getpid()
    owner_ticks = start_ticks(owner_pid)
    connection = str(uuid.uuid4())
    process = None
    warned = False

    async def emit(event):
        nonlocal warned
        try:
            await async_request(event, timeout=0.5)
        except (OSError, ValueError, RuntimeError, asyncio.TimeoutError):
            if not warned:
                LOG.warning("attention service unavailable; Codex traffic continues")
                warned = True

    def identity():
        pid = codex_pid(process.pid)
        return dict(identity_base, pid=pid, start_ticks=start_ticks(pid),
                    owner_pid=owner_pid, owner_start_ticks=owner_ticks)

    observer = Observer(connection, identity, emit)

    async def handle(client):
        try:
            async with unix_connect(str(codex_socket()), max_size=None, ping_interval=None, close_timeout=2) as upstream:
                async def forward(source, target, from_client):
                    async for raw in source:
                        try:
                            message = json.loads(raw)
                        except (ValueError, TypeError):
                            await target.send(raw)
                            continue
                        action = None
                        try:
                            if from_client:
                                message = await observer.client(message)
                            else:
                                action = await observer.server(message)
                        except (KeyError, TypeError, ValueError):
                            LOG.exception("unrecognized Codex event; forwarding unchanged")
                        await target.send(json.dumps(message))
                        if not from_client and action == "client_exit":
                            if not process.request_exit():
                                LOG.warning("client_exit needs an interactive terminal")
                tasks = [asyncio.create_task(forward(client, upstream, True)),
                         asyncio.create_task(forward(upstream, client, False))]
                try:
                    done, pending = await asyncio.wait(tasks, return_when=asyncio.FIRST_COMPLETED)
                    for task in pending:
                        task.cancel()
                    for task in done:
                        task.result()
                finally:
                    for task in tasks:
                        task.cancel()
                    await asyncio.gather(*tasks, return_exceptions=True)
        except (OSError, ConnectionClosed):
            LOG.info("Codex connection closed")
        finally:
            await emit({"op": "unbind", "connection": connection})

    with tempfile.TemporaryDirectory(prefix="codex-", dir=runtime_dir()) as directory:
        proxy_socket = Path(directory) / "proxy.sock"
        async with unix_serve(handle, str(proxy_socket), max_size=None, ping_interval=None, close_timeout=2):
            proxy_socket.chmod(0o600)
            hooks = []
            for event in HOOK_EVENTS:
                command = '[{hooks=[{type="command",command=' + json.dumps(hook_command()) + ',timeout=3}]}]'
                hooks += ["-c", "hooks." + event + "=" + command]
            process = await Terminal.start(executable, "--remote", "unix://" + str(proxy_socket),
                                           *hooks, *arguments)
            # Ctrl-C reaches Codex through PTY input; keep the supervisor alive
            # so interrupting a turn does not lose tracking.
            previous_int = signal.signal(signal.SIGINT, lambda *args: None)
            loop = asyncio.get_running_loop()
            def terminate():
                if process.returncode is None:
                    process.terminate()
            for sig in (signal.SIGTERM, signal.SIGHUP):
                loop.add_signal_handler(sig, terminate)
            try:
                return await process.wait()
            finally:
                signal.signal(signal.SIGINT, previous_int)
                for sig in (signal.SIGTERM, signal.SIGHUP):
                    loop.remove_signal_handler(sig)
                await emit({"op": "unbind", "connection": connection})
