import json
import subprocess
import sys
import unittest

from agent_done.prompt_rules import CLIENT_EXIT_REASON, prompt_result
from agent_done.codex import Observer


class PromptRulesTests(unittest.TestCase):
    def test_only_exact_user_command_is_consumed(self):
        self.assertEqual(prompt_result({"hook_event_name": "UserPromptSubmit", "prompt": ":q"}),
                         {"decision": "block", "reason": CLIENT_EXIT_REASON})
        for prompt in ("解释 :q", " :q", ":q ", ":q\n", "`:q`", ":quit", "", None, [":q"]):
            self.assertEqual(prompt_result({"hook_event_name": "UserPromptSubmit", "prompt": prompt}), {})
        self.assertEqual(prompt_result({"hook_event_name": "Stop", "prompt": ":q"}), {})

    def test_hook_blocks_even_when_notification_service_is_unavailable(self):
        # The command rule must still prevent inference if the status bar fails.
        import os
        import tempfile
        with tempfile.TemporaryDirectory() as directory:
            result = subprocess.run([sys.executable, "-m", "agent_done", "hook"],
                input=json.dumps({"hook_event_name": "UserPromptSubmit", "prompt": ":q"}),
                text=True, capture_output=True, timeout=3,
                env=dict(os.environ, XDG_RUNTIME_DIR=directory))
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(json.loads(result.stdout), {"decision": "block", "reason": CLIENT_EXIT_REASON})


class ClientExitTests(unittest.IsolatedAsyncioTestCase):
    async def asyncSetUp(self):
        async def emit(message):
            pass
        self.observer = Observer("connection", lambda: {}, emit)

    async def bind(self):
        await self.observer.client({"id": 1, "method": "thread/start", "params": {}})
        await self.observer.server({"id": 1, "result": {"thread": {"id": "chat"}}})

    async def submit(self, tid="t"):
        await self.observer.client({"id": 2, "method": "turn/start", "params": {"threadId": "chat"}})
        return await self.observer.server({"id": 2, "result": {"turn": {"id": tid}}})

    async def blocked(self, tid="t", **updates):
        run = {"eventName": "userPromptSubmit", "handlerType": "command", "status": "blocked",
               "entries": [{"kind": "feedback", "text": CLIENT_EXIT_REASON}]}
        run.update(updates)
        return await self.observer.server({"method": "hook/completed", "params": {
            "threadId": "chat", "turnId": tid, "run": run}})

    async def completed(self, tid="t"):
        return await self.observer.server({"method": "turn/completed", "params": {
            "threadId": "chat", "turn": {"id": tid, "status": "completed"}}})

    async def test_exit_waits_for_blocked_owned_turn_to_complete_and_is_once_only(self):
        await self.bind()
        await self.submit()
        self.assertIsNone(await self.blocked())
        self.assertEqual(await self.completed(), "client_exit")
        self.assertIsNone(await self.completed())
        self.assertIsNone(await self.blocked())

    async def test_notification_from_other_client_cannot_close_this_terminal(self):
        await self.bind()
        await self.blocked()
        self.assertIsNone(await self.completed())

    async def test_hook_events_before_turn_start_response_are_correlated(self):
        await self.bind()
        await self.blocked()
        await self.completed()
        self.assertEqual(await self.submit(), "client_exit")

    async def test_hidden_session_and_unrelated_hook_cannot_close_terminal(self):
        await self.bind()
        await self.submit()
        await self.blocked(status="failed")
        self.assertIsNone(await self.completed())
        await self.observer.client({"id": 3, "method": "thread/start", "params": {}})
        await self.observer.server({"id": 3, "result": {"thread": {"id": "other"}}})
        self.assertIsNone(await self.blocked())
        self.assertIsNone(await self.completed())
