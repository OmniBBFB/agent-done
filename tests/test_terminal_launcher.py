"""macOS acceptance must use application executables and the real environment."""

import importlib.util
import os
from pathlib import Path
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location(
    "terminal_launcher", Path(__file__).resolve().parents[1] / "tools/terminal_launcher.py")
launcher = importlib.util.module_from_spec(spec)
spec.loader.exec_module(launcher)


class MacTerminalTests(unittest.TestCase):
    def test_application_install_works_without_terminal_on_path(self):
        with patch.object(launcher.sys, "platform", "darwin"), \
             patch.object(launcher.shutil, "which", return_value=None), \
             patch.object(Path, "is_file", return_value=True):
            command = launcher.terminal_command("alacritty", "probe", "probe", ["python", "probe.py"])
        self.assertEqual(command[0], "/Applications/Alacritty.app/Contents/MacOS/alacritty")
        self.assertEqual(command[-3:], ["-e", "python", "probe.py"])

    def test_macos_does_not_invent_a_linux_runtime_directory(self):
        with patch.object(launcher.sys, "platform", "darwin"), \
             patch.dict(os.environ, {"HOME": "/example"}, clear=True):
            env = launcher.desktop_environment()
        self.assertNotIn("XDG_RUNTIME_DIR", env)
        self.assertEqual(env["HOME"], "/example")
