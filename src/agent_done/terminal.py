"""Transparent interactive PTY with a narrow native /quit control action."""

import asyncio
import errno
import fcntl
import os
import pty
import signal
import termios
import tty


class Terminal:
    def __init__(self):
        self.process = None
        self.master = None
        self.previous = None
        self.pending = bytearray()
        self.exit_task = None
        self.eof = False

    @property
    def pid(self):
        return self.process.pid

    @property
    def returncode(self):
        return self.process.returncode

    @classmethod
    async def start(cls, *arguments):
        self = cls()
        if not (os.isatty(0) and os.isatty(1)):
            self.process = await asyncio.create_subprocess_exec(*arguments)
            return self
        self.loop = asyncio.get_running_loop()
        master, slave = pty.openpty()
        self.master = master
        try:
            self.previous = termios.tcgetattr(0)
            termios.tcsetattr(slave, termios.TCSANOW, self.previous)
            self.resize()

            def controlling_terminal():
                fcntl.ioctl(0, termios.TIOCSCTTY, 0)

            self.process = await asyncio.create_subprocess_exec(
                *arguments, stdin=slave, stdout=slave, stderr=slave,
                start_new_session=True, preexec_fn=controlling_terminal)
            os.set_blocking(master, False)
            tty.setraw(0)
            self.loop.add_reader(0, self.read_input)
            self.loop.add_reader(master, self.read_output)
            self.loop.add_signal_handler(signal.SIGWINCH, self.resize)
            return self
        except BaseException:
            self.close()
            if self.process is not None and self.returncode is None:
                self.process.terminate()
                await self.process.wait()
            raise
        finally:
            os.close(slave)

    def resize(self):
        size = fcntl.ioctl(0, termios.TIOCGWINSZ, b"\0" * 8)
        fcntl.ioctl(self.master, termios.TIOCSWINSZ, size)

    def read_input(self):
        data = os.read(0, 65536)
        if not data:
            self.loop.remove_reader(0)
            self.terminate()
        elif self.exit_task is None:
            self.write(data)

    def write(self, data):
        self.pending.extend(data)
        self.flush()

    def flush(self):
        try:
            while self.pending:
                count = os.write(self.master, self.pending)
                del self.pending[:count]
        except BlockingIOError:
            self.loop.add_writer(self.master, self.flush)
        except OSError as error:
            if error.errno not in (errno.EIO, errno.EBADF):
                raise
            self.loop.remove_writer(self.master)
        else:
            self.loop.remove_writer(self.master)

    def read_output(self):
        try:
            data = os.read(self.master, 65536)
        except BlockingIOError:
            return
        except OSError as error:
            if error.errno != errno.EIO:
                raise
            data = b""
        if data:
            view = memoryview(data)
            while view:
                view = view[os.write(1, view):]
        else:
            self.eof = True
            self.loop.remove_reader(self.master)

    def request_exit(self):
        if self.master is None:
            return False
        if self.exit_task is None:
            self.exit_task = asyncio.create_task(self.quit())
        return True

    async def quit(self):
        # The caller has forwarded turn/completed. Let the TUI process that event
        # before issuing its native command. Separate Enter from typing so the
        # composer does not treat the entire command as a paste burst.
        await asyncio.sleep(0.15)
        if self.returncode is None and not self.eof:
            self.pending.clear()
            self.write(b"\x15/quit")
            await asyncio.sleep(0.1)
            if self.returncode is None and not self.eof:
                self.write(b"\r")

    def terminate(self):
        if self.returncode is None:
            try:
                self.process.terminate()
            except ProcessLookupError:
                pass

    async def wait(self):
        try:
            return await self.process.wait()
        finally:
            if self.exit_task is not None:
                self.exit_task.cancel()
                await asyncio.gather(self.exit_task, return_exceptions=True)
            self.close()

    def close(self):
        if self.master is not None:
            self.loop.remove_reader(0)
            self.loop.remove_reader(self.master)
            self.loop.remove_writer(self.master)
            self.loop.remove_signal_handler(signal.SIGWINCH)
            try:
                if not self.eof:
                    self.read_output()
            finally:
                try:
                    if self.previous is not None:
                        termios.tcsetattr(0, termios.TCSADRAIN, self.previous)
                finally:
                    os.close(self.master)
                    self.master = None
