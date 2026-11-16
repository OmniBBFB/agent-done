import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from agent_done.install import install, jsonc
from agent_done.state import State
from agent_done.waybar import render


class DesktopIntegrationTests(unittest.TestCase):
    def test_waybar_escapes_titles_and_updates_live_location(self):
        state = State(":memory:")
        try:
            state.bind("a", "chat", {"window_id": 10, "pid": 123, "start_ticks": "45"}, "<b>unsafe & title</b>")
            state.observe_desktop({"backend": "niri", "focused": None, "windows": [
                {"id": 10, "position": {"output": "DP-1", "workspace": 2, "row": 2, "column": 3, "floating": False}}]})
            state.complete("chat", "t", "completed")
            obj = render(state.snapshot())
            self.assertEqual(obj["text"], "Codex 1")
            self.assertIn("&lt;b&gt;unsafe &amp; title&lt;/b&gt;", obj["tooltip"])
            self.assertIn("第 2 行第 3 列", obj["tooltip"])
            state.focus(10)
            self.assertEqual(render(state.snapshot())["text"], "Codex 0")
        finally:
            state.close()

    def test_jsonc_preserves_comment_like_and_trailing_comma_text_inside_strings(self):
        self.assertEqual(jsonc('{/* comment */ "a":"https://x//y,}", "b":[1,], // line\n}'),
                         {"a": "https://x//y,}", "b": [1]})

    def test_install_is_idempotent_and_preserves_existing_modules_with_backups(self):
        with tempfile.TemporaryDirectory() as directory:
            home = Path(directory)
            waybar = home / ".config/waybar"
            waybar.mkdir(parents=True)
            source = '{// comment\n"modules-right":["clock",],"clock":{"format":"x,}"},}'
            (waybar / "config.jsonc").write_text(source)
            (waybar / "style.css").write_text("/* existing */\n")
            executable = home / "venv/bin/agent-done"
            executable.parent.mkdir(parents=True)
            executable.write_text("placeholder")
            with patch("pathlib.Path.home", return_value=home), patch("agent_done.install.executable", return_value=executable), \
                 patch.dict(os.environ, {"XDG_STATE_HOME": str(home / "state")}, clear=False):
                first = install()
                second = install()
            data = json.loads((waybar / "config.jsonc").read_text())
            self.assertEqual(data["modules-right"].count("custom/agent-done"), 1)
            self.assertEqual(data["clock"], {"format": "x,}"})
            self.assertEqual((Path(first["backup"]) / "config.jsonc").read_text(), source)
            self.assertNotEqual(first["backup"], second["backup"])
            css = (waybar / "style.css").read_text()
            self.assertTrue(css.startswith("/* existing */"))
            self.assertEqual(css.count("/* agent-done:start */"), 1)
