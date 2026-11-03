"""Exercise the native-command bridge through real nested terminal devices."""

import errno
import os
from pathlib import Path
import pty
import select
import subprocess
import sys
import tempfile
import time
import unittest


PROBE = r'''
import asyncio
import json
import os
from pathlib import Path
import sys
import termios
from agent_done.terminal import Terminal

async def main():
    before = termios.tcgetattr(0)
    app = "import os,tty; tty.setraw(0); data=b''\nwhile not data.endswith(b'\\r'): data+=os.read(0,1)\nprint('RECEIVED:'+data.hex(),flush=True)"
    terminal = await Terminal.start(sys.executable, "-u", "-c", app)
    await asyncio.sleep(0.1)
    terminal.request_exit()
    terminal.request_exit()
    code = await terminal.wait()
    Path(sys.argv[1]).write_text(json.dumps({"exit": code, "restored": termios.tcgetattr(0) == before}))

asyncio.run(main())
'''


class TerminalTests(unittest.TestCase):
    def test_exit_uses_native_command_and_restores_terminal(self):
        import json
        with tempfile.TemporaryDirectory() as directory:
            result = Path(directory) / "result.json"
            master, slave = pty.openpty()
            process = subprocess.Popen([sys.executable, "-c", PROBE, str(result)],
                                       stdin=slave, stdout=slave, stderr=slave)
            os.close(slave)
            output = bytearray()
            deadline = time.monotonic() + 5
            try:
                while time.monotonic() < deadline:
                    if select.select([master], [], [], 0.05)[0]:
                        try:
                            data = os.read(master, 65536)
                        except OSError as error:
                            if error.errno == errno.EIO:
                                break
                            raise
                        if not data:
                            break
                        output.extend(data)
                    if process.poll() is not None:
                        break
                self.assertEqual(process.poll(), 0, output.decode(errors="replace"))
                self.assertEqual(json.loads(result.read_text()), {"exit": 0, "restored": True})
                self.assertIn(b"RECEIVED:152f717569740d", output)
            finally:
                if process.poll() is None:
                    process.kill()
                process.wait()
                os.close(master)
