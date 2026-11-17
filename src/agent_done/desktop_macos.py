"""Experimental public Accessibility adapter with service-owned AX references.

IDs are scoped to this service lifetime. No titles, private window-number API,
screen recording or CGS Space switching are used to guess window identity.
"""

import uuid
import sys

from .process import start_ticks, same_process


class Accessibility:
    def __init__(self):
        try:
            import HIServices
            import CoreFoundation
            import AppKit
        except ImportError as error:
            raise RuntimeError("原生 macOS 接入需要 PyObjC ApplicationServices，请执行 uv sync") from error
        self.ax = HIServices
        self.equal = CoreFoundation.CFEqual
        self.running_application = AppKit.NSRunningApplication.runningApplicationWithProcessIdentifier_
        self.system = self.ax.AXUIElementCreateSystemWide()
        self.ax.AXUIElementSetMessagingTimeout(self.system, 0.3)

    def check(self):
        if not self.ax.AXIsProcessTrusted():
            raise RuntimeError(f"请在 macOS 辅助功能设置中授权服务进程 {sys.executable}，然后重启服务")

    def app(self, pid):
        app = self.ax.AXUIElementCreateApplication(pid)
        self.ax.AXUIElementSetMessagingTimeout(app, 0.3)
        return app

    def copy(self, element, name, optional=False):
        error, value = self.ax.AXUIElementCopyAttributeValue(element, name, None)
        if not error:
            return value
        if optional and error in (self.ax.kAXErrorAttributeUnsupported, self.ax.kAXErrorNoValue):
            return None
        raise RuntimeError(f"macOS AX 读取 {name} 失败（{error}），查看状态保持未确认")

    def windows(self, pid):
        if self.running_application(pid) is None:
            return []
        return list(self.copy(self.app(pid), "AXWindows", optional=True) or [])

    def valid(self, element):
        error, _ = self.ax.AXUIElementCopyAttributeValue(element, "AXRole", None)
        if error == self.ax.kAXErrorInvalidUIElement:
            return False
        self._result(error)
        return True

    def focused(self):
        app = self.copy(self.system, "AXFocusedApplication", optional=True)
        return self.copy(app, "AXFocusedWindow", optional=True) if app is not None else None

    def activate(self, pid, window):
        if self.copy(window, "AXMinimized", optional=True):
            self._result(self.ax.AXUIElementSetAttributeValue(window, "AXMinimized", False))
        self._result(self.ax.AXUIElementSetAttributeValue(self.app(pid), "AXFrontmost", True))
        self._result(self.ax.AXUIElementPerformAction(window, "AXRaise"))

    @staticmethod
    def _result(error):
        if error:
            raise RuntimeError(f"macOS AX 聚焦请求失败（{error}），提醒仍保留")


class MacOS:
    name = "macos"
    ephemeral = True

    def __init__(self, api=None):
        self._api = api
        self.epoch = uuid.uuid4().hex
        self.counter = 0
        self.owners = {}
        self.references = {}

    @property
    def api(self):
        if self._api is None:
            self._api = Accessibility()
        return self._api

    def register(self, chain):
        self.api.check()
        for pid in chain:
            created = start_ticks(pid)
            if created and self.api.windows(pid):
                self.owners[pid] = created
                return

    def snapshot(self):
        self.api.check()
        references = {}
        # AX enumeration may omit windows on another Space. Keep a retained,
        # still-valid reference rather than misreporting that window as closed.
        for key, ref in self.references.items():
            if same_process(ref[0], ref[1]) and self.api.valid(ref[2]):
                references[key] = ref
        for pid, created in list(self.owners.items()):
            if not same_process(pid, created):
                del self.owners[pid]
                continue
            for element in self.api.windows(pid):
                ident = next((key for key, ref in self.references.items()
                              if ref[0:2] == (pid, created) and self.api.equal(ref[2], element)), None)
                if ident is None:
                    self.counter += 1
                    ident = f"{self.epoch}:{self.counter}"
                references[ident] = (pid, created, element)
        focused_element = self.api.focused()
        focused = next((key for key, ref in references.items()
                        if focused_element is not None and self.api.equal(ref[2], focused_element)), None)
        # Commit only a complete observation; AX failures must not look like closure.
        self.references = references
        return {"backend": self.name, "focused": focused,
                "windows": [{"id": key, "pid": ref[0], "start": ref[1], "position": None}
                            for key, ref in references.items()]}

    def activate(self, window_id):
        ref = self.references.get(window_id)
        if ref is None or not same_process(ref[0], ref[1]):
            raise RuntimeError("macOS 窗口引用已失效，请重新启动或恢复该受跟踪会话")
        self.api.check()
        self.api.activate(ref[0], ref[2])
