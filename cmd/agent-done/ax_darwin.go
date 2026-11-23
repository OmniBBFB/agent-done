package main

/*
#cgo LDFLAGS: -framework ApplicationServices
#include <ApplicationServices/ApplicationServices.h>

static AXError ax_windows(AXUIElementRef app, CFArrayRef *out) {
	return AXUIElementCopyAttributeValue(app, kAXWindowsAttribute, (CFTypeRef *)out);
}

static AXUIElementRef ax_window_at(CFArrayRef windows, CFIndex index) {
	return (AXUIElementRef)CFArrayGetValueAtIndex(windows, index);
}

static AXError ax_focused_window(AXUIElementRef system, AXUIElementRef *out) {
	AXUIElementRef app = NULL;
	AXError err = AXUIElementCopyAttributeValue(system, kAXFocusedApplicationAttribute, (CFTypeRef *)&app);
	if (err == kAXErrorSuccess) {
		err = AXUIElementCopyAttributeValue(app, kAXFocusedWindowAttribute, (CFTypeRef *)out);
		CFRelease(app);
	}
	return err;
}

static AXError ax_valid(AXUIElementRef window) {
	CFTypeRef role = NULL;
	AXError err = AXUIElementCopyAttributeValue(window, kAXRoleAttribute, &role);
	if (role) CFRelease(role);
	return err;
}

static AXError ax_focus(AXUIElementRef app, AXUIElementRef window) {
	CFTypeRef minimized = NULL;
	AXError err = AXUIElementCopyAttributeValue(window, kAXMinimizedAttribute, &minimized);
	if (err == kAXErrorSuccess) {
		Boolean needs_restore = CFEqual(minimized, kCFBooleanTrue);
		CFRelease(minimized);
		if (needs_restore) {
			err = AXUIElementSetAttributeValue(window, kAXMinimizedAttribute, kCFBooleanFalse);
			if (err != kAXErrorSuccess) return err;
		}
	} else if (err != kAXErrorAttributeUnsupported && err != kAXErrorNoValue) {
		return err;
	}
	err = AXUIElementSetAttributeValue(app, kAXFrontmostAttribute, kCFBooleanTrue);
	if (err != kAXErrorSuccess) return err;
	return AXUIElementPerformAction(window, kAXRaiseAction);
}
*/
import "C"

import (
	"context"
	"errors"
	"fmt"
	"os"
)

type axWindow struct {
	window Window
	ref    C.AXUIElementRef
}

// The service holds desktopMu around every AX operation.
type axDesktop struct {
	system  C.AXUIElementRef
	windows map[int64]axWindow
	nextID  int64
}

func macDesktop(name string) (desktopBackend, error) {
	switch name {
	case "yabai":
		return desktopBackend{name: name, snapshot: yabaiSnapshot, focus: yabaiFocus}, nil
	case "macos":
		ax := &axDesktop{windows: make(map[int64]axWindow)}
		return desktopBackend{name: name, snapshot: ax.snapshot, focus: ax.focus, register: ax.register, close: ax.close}, nil
	default:
		return desktopBackend{}, fmt.Errorf("macOS 不支持桌面后端 %q，可选择 macos 或 yabai", name)
	}
}

func (a *axDesktop) check() error {
	if C.AXIsProcessTrusted() == 0 {
		path, _ := os.Executable()
		return fmt.Errorf("请在「系统设置 → 隐私与安全性 → 辅助功能」授权运行 %s 的宿主", path)
	}
	if a.system == 0 {
		a.system = C.AXUIElementCreateSystemWide()
		C.AXUIElementSetMessagingTimeout(a.system, 0.3)
	}
	return nil
}

func axApp(pid int) C.AXUIElementRef {
	app := C.AXUIElementCreateApplication(C.pid_t(pid))
	C.AXUIElementSetMessagingTimeout(app, 0.3)
	return app
}

func (a *axDesktop) register(ctx context.Context, chain []int) error {
	if err := a.check(); err != nil {
		return err
	}
	for _, pid := range chain {
		if err := ctx.Err(); err != nil {
			return err
		}
		start := startTicks(pid)
		if start == "" {
			continue
		}
		app := axApp(pid)
		var windows C.CFArrayRef
		code := C.ax_windows(app, &windows)
		C.CFRelease(C.CFTypeRef(app))
		if code != C.kAXErrorSuccess {
			continue
		}
		count := C.CFArrayGetCount(windows)
		for i := C.CFIndex(0); i < count; i++ {
			ref := C.ax_window_at(windows, i)
			if a.windowID(ref, pid, start) == 0 {
				a.nextID++
				C.CFRetain(C.CFTypeRef(ref))
				a.windows[a.nextID] = axWindow{
					window: Window{ID: a.nextID, PID: pid, Start: start},
					ref:    ref,
				}
			}
		}
		C.CFRelease(C.CFTypeRef(windows))
		if count > 0 {
			return nil
		}
	}
	return errSourceWindowNotFound
}

func (a *axDesktop) windowID(ref C.AXUIElementRef, pid int, start string) int64 {
	for id, match := range a.windows {
		if match.window.PID == pid && match.window.Start == start &&
			C.CFEqual(C.CFTypeRef(ref), C.CFTypeRef(match.ref)) != 0 {
			return id
		}
	}
	return 0
}

func (a *axDesktop) snapshot(ctx context.Context) (Observation, error) {
	if err := a.check(); err != nil {
		return Observation{}, err
	}
	result := Observation{Backend: "macos", Windows: make([]Window, 0, len(a.windows))}
	for id, item := range a.windows {
		if err := ctx.Err(); err != nil {
			return Observation{}, err
		}
		if !sameProcess(item.window.PID, item.window.Start) {
			a.release(id)
			continue
		}
		switch code := C.ax_valid(item.ref); code {
		case C.kAXErrorSuccess:
			result.Windows = append(result.Windows, item.window)
		case C.kAXErrorInvalidUIElement:
			a.release(id)
		default:
			return Observation{}, fmt.Errorf("macOS AX 检查窗口失败（%d）", code)
		}
	}
	if len(result.Windows) == 0 {
		return result, nil
	}
	var focused C.AXUIElementRef
	switch code := C.ax_focused_window(a.system, &focused); code {
	case C.kAXErrorSuccess:
		defer C.CFRelease(C.CFTypeRef(focused))
		var pid C.pid_t
		if C.AXUIElementGetPid(focused, &pid) == C.kAXErrorSuccess {
			if id := a.windowID(focused, int(pid), startTicks(int(pid))); id != 0 {
				result.Focused = &id
			}
		}
	case C.kAXErrorNoValue, C.kAXErrorAttributeUnsupported:
		// The frontmost app has no focused window.
	default:
		return Observation{}, fmt.Errorf("macOS AX 查询焦点失败（%d）", code)
	}
	return result, nil
}

func (a *axDesktop) focus(ctx context.Context, window Window) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := a.check(); err != nil {
		return err
	}
	item, ok := a.windows[window.ID]
	if !ok || !sameProcess(window.PID, window.Start) ||
		C.ax_valid(item.ref) != C.kAXErrorSuccess {
		return errors.New("macOS AX 目标窗口已失效")
	}
	app := axApp(window.PID)
	defer C.CFRelease(C.CFTypeRef(app))
	if code := C.ax_focus(app, item.ref); code != C.kAXErrorSuccess {
		return fmt.Errorf("macOS AX 聚焦请求失败（%d）", code)
	}
	return nil
}

func (a *axDesktop) release(id int64) {
	C.CFRelease(C.CFTypeRef(a.windows[id].ref))
	delete(a.windows, id)
}

func (a *axDesktop) close() {
	for id := range a.windows {
		a.release(id)
	}
	if a.system != 0 {
		C.CFRelease(C.CFTypeRef(a.system))
	}
}
