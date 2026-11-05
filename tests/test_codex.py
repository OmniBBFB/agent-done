import copy
from pathlib import Path
import tempfile
import unittest
from unittest.mock import AsyncMock, patch

from agent_done.codex import Observer, HOOK_EVENTS, hook_command, launch, with_hooks
from agent_done.state import State
from agent_done.runtime import Runtime


class LaunchDirectoryTests(unittest.IsolatedAsyncioTestCase):
    async def launch_command(self, arguments):
        with tempfile.TemporaryDirectory() as directory:
            socket = Path(directory) / "daemon.sock"
            socket.touch()
            process = AsyncMock()
            process.wait.return_value = 0
            process.returncode = 0
            with patch("agent_done.codex.shutil.which", return_value="/bin/codex"), \
                 patch("agent_done.codex.window_identity", return_value={}), \
                 patch("agent_done.codex.ensure_service", new=AsyncMock()), \
                 patch("agent_done.codex.async_request", new=AsyncMock()), \
                 patch("agent_done.codex.codex_socket", return_value=socket), \
                 patch("agent_done.codex.runtime_dir", return_value=Path(directory)), \
                 patch("agent_done.codex.asyncio.create_subprocess_exec", new=AsyncMock(return_value=process)) as spawn:
                await launch(arguments)
                return spawn.call_args.args

    async def test_remote_launch_passes_callers_directory(self):
        command = await self.launch_command([])
        self.assertIn("--cd", command)
        self.assertEqual(command[command.index("--cd") + 1], str(Path.cwd()))

    async def test_explicit_directory_is_preserved_without_duplicate_option(self):
        for arguments in (["-C", "/chosen"], ["--cd", "/chosen"],
                          ["--cd=/chosen"], ["-C/chosen"],
                          ["resume", "--cd", "/chosen", "--last"]):
            with self.subTest(arguments=arguments):
                command = await self.launch_command(arguments)
                self.assertEqual(list(command[-len(arguments):]), arguments)
                self.assertNotIn(str(Path.cwd()), command)


class CodexTests(unittest.IsolatedAsyncioTestCase):
    async def asyncSetUp(self):
        self.state = State(":memory:")
        self.runtime = Runtime(self.state)
        self.events = []

        async def emit(message):
            self.events.append(message)
            self.runtime.dispatch(message)

        self.observer = Observer("connection", lambda: {"window_id": 10, "pid": 123, "start_ticks": "45"}, emit)

    async def asyncTearDown(self):
        self.state.close()

    async def bind(self):
        await self.observer.client({"id": 1, "method": "thread/start", "params": {"threadSource": "user"}})
        await self.observer.server({"id": 1, "result": {"thread": {"id": "chat", "cwd": "/project", "name": "Project"}}})

    async def test_title_generation_cannot_replace_binding_or_create_attention(self):
        await self.bind()
        request = {"id": 2, "method": "thread/start", "params": {"ephemeral": True, "threadSource": "thread_title"}}
        self.assertEqual(await self.observer.client(request), request)
        await self.observer.server({"id": 2, "result": {"thread": {"id": "title"}}})
        await self.observer.server({"method": "turn/completed", "params": {"threadId": "title", "turn": {"id": "t", "status": "completed"}}})
        snapshot = self.state.snapshot()
        self.assertEqual([s["id"] for s in snapshot["sessions"]], ["chat"])
        self.assertEqual(snapshot["pending_count"], 0)

    async def test_actual_approval_and_structured_question_requests_then_responses(self):
        await self.bind()
        for method, kind, ident in (("item/commandExecution/requestApproval", "approval", 9),
                                    ("item/tool/requestUserInput", "question", 10)):
            request = {"id": ident, "method": method, "params": {"threadId": "chat", "turnId": "t"}}
            original = copy.deepcopy(request)
            await self.observer.server(request)
            self.assertEqual(request, original)
            snapshot = self.state.snapshot()
            self.assertEqual(snapshot["pending_count"], 1)
            self.assertEqual(snapshot["sessions"][0]["phase"], kind)
            response = {"id": ident, "result": {"decision": "decline"}} if kind == "approval" else {"id": ident, "result": {"answers": {"a": "b"}}}
            self.assertEqual(await self.observer.client(response), response)
            self.assertEqual(self.state.snapshot()["pending_count"], 0)

    async def test_request_resolved_notification_without_client_response(self):
        await self.bind()
        await self.observer.server({"id": "q", "method": "item/tool/requestUserInput", "params": {"threadId": "chat", "turnId": "t"}})
        await self.observer.server({"method": "serverRequest/resolved", "params": {"threadId": "chat", "requestId": "q"}})
        self.assertEqual(self.state.snapshot()["pending_count"], 0)

    async def test_completed_turn_confirmation_is_the_notification(self):
        await self.bind()
        await self.observer.server({"method": "turn/started", "params": {"threadId": "chat", "turn": {"id": "t"}}})
        await self.observer.server({"method": "turn/completed", "params": {"threadId": "chat", "turn": {"id": "t", "status": "completed"}}})
        self.assertEqual(self.state.snapshot()["pending_count"], 1)

    def test_user_hooks_model_routing_and_other_settings_survive_merge(self):
        original = {"model": "custom", "openai_base_url": "https://example.invalid/", "hooks": {
            "Stop": [{"hooks": [{"type": "command", "command": "existing-command"}]}]},
            "shell_environment_policy": {"inherit": "all"}}
        before = copy.deepcopy(original)
        result = with_hooks(original)
        self.assertEqual(original, before)
        self.assertEqual(result["openai_base_url"], before["openai_base_url"])
        commands = [h["command"] for group in result["hooks.Stop"] for h in group["hooks"]]
        self.assertEqual(commands, ["existing-command", hook_command()])
        repeated = with_hooks(result)
        self.assertEqual(repeated, result)
        self.assertTrue(all("hooks." + event in result for event in HOOK_EVENTS))
