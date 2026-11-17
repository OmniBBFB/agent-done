"""Native macOS menu bar client for the local attention service."""

import asyncio
import logging
from pathlib import Path
import threading

from .ipc import snapshots


async def watch_updates(publish, stopped):
    """Deliver service changes immediately; retry only when disconnected."""
    while not stopped.is_set():
        try:
            async for snapshot in snapshots():
                if stopped.is_set():
                    return
                publish(snapshot, "")
        except (OSError, ValueError, RuntimeError, asyncio.TimeoutError) as error:
            publish(None, f"服务未连接：{error}")
        await asyncio.sleep(0.5)


def run():
    import AppKit
    import Foundation
    import objc

    class MenuController(Foundation.NSObject):
        def applicationDidFinishLaunching_(self, notification):
            self.latest = None
            self.message = "正在连接服务"
            self.stopped = threading.Event()
            self.item = AppKit.NSStatusBar.systemStatusBar().statusItemWithLength_(AppKit.NSVariableStatusItemLength)
            threading.Thread(target=self.poll, daemon=True).start()
            self.refresh_(None)

        @objc.python_method
        def poll(self):
            asyncio.run(watch_updates(self.publish, self.stopped))

        @objc.python_method
        def publish(self, snapshot, message):
            self.latest, self.message = snapshot, message
            self.performSelectorOnMainThread_withObject_waitUntilDone_("refresh:", None, False)

        @objc.python_method
        def add(self, menu, title, action=None):
            item = AppKit.NSMenuItem.alloc().initWithTitle_action_keyEquivalent_(title, action, "")
            if action:
                item.setTarget_(self)
            else:
                item.setEnabled_(False)
            menu.addItem_(item)

        def refresh_(self, timer):
            snapshot = self.latest
            count = snapshot["pending_count"] if snapshot else "?"
            disconnected = snapshot is not None and not snapshot.get("desktop_connected")
            title = f"AD {count}" + (" !" if disconnected else "")
            self.item.button().setTitle_(title)
            detail = (snapshot.get("desktop_error") or "桌面未连接") if disconnected else self.message
            self.item.button().setToolTip_(detail or f"agent-done · 待处理 {count} 个会话")
            menu = AppKit.NSMenu.alloc().init()
            menu.setAutoenablesItems_(False)
            self.add(menu, "跳到下一个待处理会话", "jump:")
            menu.addItem_(AppKit.NSMenuItem.separatorItem())
            self.add(menu, self.message or f"待处理 {count} 个会话")
            if snapshot:
                for session in snapshot["sessions"]:
                    if session["pending"]:
                        name = session["title"] or Path(session["cwd"]).name or session["id"][:8]
                        self.add(menu, " ".join(name.split())[:100])
                if not snapshot.get("desktop_connected"):
                    self.add(menu, snapshot.get("desktop_error") or "桌面尚未连接")
            menu.addItem_(AppKit.NSMenuItem.separatorItem())
            self.add(menu, "退出菜单栏", "quit:")
            self.item.setMenu_(menu)

        def jump_(self, sender):
            def invoke():
                from .navigation import jump
                try:
                    jump()
                except (OSError, RuntimeError, ValueError) as error:
                    logging.getLogger(__name__).warning("菜单栏跳转失败：%s", error)
            threading.Thread(target=invoke, daemon=True).start()

        def quit_(self, sender):
            self.stopped.set()
            AppKit.NSApplication.sharedApplication().terminate_(None)

    app = AppKit.NSApplication.sharedApplication()
    app.setActivationPolicy_(AppKit.NSApplicationActivationPolicyAccessory)
    controller = MenuController.alloc().init()
    app.setDelegate_(controller)
    app.run()
