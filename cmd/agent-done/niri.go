package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type niriWindow struct {
	ID          int64      `json:"id"`
	PID         int        `json:"pid"`
	WorkspaceID int64      `json:"workspace_id"`
	Focused     bool       `json:"is_focused"`
	Floating    bool       `json:"is_floating"`
	Layout      niriLayout `json:"layout"`
}

type niriLayout struct {
	Position []int `json:"pos_in_scrolling_layout"`
}

type niriWorkspace struct {
	ID     int64  `json:"id"`
	Output string `json:"output"`
	Name   string `json:"name"`
	Index  int    `json:"idx"`
}

func niriSocket() string {
	if path := os.Getenv("NIRI_SOCKET"); path != "" {
		return path
	}
	base := envOr("XDG_RUNTIME_DIR", fmt.Sprintf("/run/user/%d", os.Getuid()))
	paths, _ := filepath.Glob(filepath.Join(base, "niri.*.sock"))
	var newest time.Time
	var socket string
	for _, path := range paths {
		if info, err := os.Stat(path); err == nil && info.Mode()&os.ModeSocket != 0 && (socket == "" || info.ModTime().After(newest)) {
			socket, newest = path, info.ModTime()
		}
	}
	return socket
}

func niriCommand(ctx context.Context, args ...string) (*exec.Cmd, error) {
	socket := niriSocket()
	if socket == "" {
		return nil, errors.New("没有找到 niri socket")
	}
	cmd := exec.CommandContext(ctx, "niri", append([]string{"msg"}, args...)...)
	cmd.Env = append(os.Environ(), "NIRI_SOCKET="+socket)
	return cmd, nil
}

func niriRun(ctx context.Context, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	cmd, err := niriCommand(ctx, args...)
	if err != nil {
		return nil, err
	}
	raw, err := cmd.Output()
	var exit *exec.ExitError
	if errors.As(err, &exit) && len(exit.Stderr) > 0 {
		err = errors.New(strings.TrimSpace(string(exit.Stderr)))
	}
	return raw, err
}

func niriObservation(windows map[int64]niriWindow, workspaces map[int64]niriWorkspace) Observation {
	result := Observation{Backend: "niri", Windows: make([]Window, 0, len(windows))}
	for _, window := range windows {
		workspace := workspaces[window.WorkspaceID]
		position := &Position{Output: workspace.Output, Workspace: workspace.Index, Floating: window.Floating}
		if workspace.Name != "" {
			position.Workspace = workspace.Name
		}
		if layout := window.Layout.Position; len(layout) >= 2 {
			position.Column, position.Row = &layout[0], &layout[1]
		}
		result.Windows = append(result.Windows, Window{ID: window.ID, PID: window.PID, Start: startTicks(window.PID), Position: position})
		if window.Focused {
			result.Focused = &window.ID
		}
	}
	return result
}

func indexWindows(windows []niriWindow) map[int64]niriWindow {
	index := make(map[int64]niriWindow, len(windows))
	for _, window := range windows {
		index[window.ID] = window
	}
	return index
}

func indexWorkspaces(workspaces []niriWorkspace) map[int64]niriWorkspace {
	index := make(map[int64]niriWorkspace, len(workspaces))
	for _, workspace := range workspaces {
		index[workspace.ID] = workspace
	}
	return index
}

func niriSnapshot(ctx context.Context) (Observation, error) {
	var windows []niriWindow
	var workspaces []niriWorkspace
	for _, query := range []struct {
		name   string
		output any
	}{
		{"windows", &windows}, {"workspaces", &workspaces},
	} {
		raw, err := niriRun(ctx, "--json", query.name)
		if err != nil {
			return Observation{}, err
		}
		if err := json.Unmarshal(raw, query.output); err != nil {
			return Observation{}, err
		}
	}
	return niriObservation(indexWindows(windows), indexWorkspaces(workspaces)), nil
}

func niriFocus(ctx context.Context, id int64) error {
	_, err := niriRun(ctx, "action", "focus-window", "--id", strconv.FormatInt(id, 10))
	return err
}

var errSourceWindowNotFound = errors.New("无法从源进程找到终端窗口，请在本机独立终端窗口内运行")

func identifyWindow(observation Observation, pid int) (*Identity, error) {
	for _, ancestor := range ancestors(pid) {
		var candidates []Window
		for _, window := range observation.Windows {
			if window.PID == ancestor {
				candidates = append(candidates, window)
			}
		}
		if len(candidates) == 0 {
			continue
		}
		if len(candidates) > 1 {
			for _, window := range candidates {
				if observation.Focused != nil && window.ID == *observation.Focused {
					candidates = []Window{window}
					break
				}
			}
		}
		if len(candidates) != 1 || candidates[0].Start == "" {
			return nil, errors.New("无法唯一识别源终端窗口，请在目标独立窗口中启动")
		}
		window := candidates[0]
		return &Identity{Backend: "niri", WindowID: &window.ID, WindowPID: window.PID, WindowStart: window.Start}, nil
	}
	return nil, errSourceWindowNotFound
}

type niriEvent struct {
	WindowsChanged *struct {
		Windows []niriWindow `json:"windows"`
	}
	WorkspacesChanged *struct {
		Workspaces []niriWorkspace `json:"workspaces"`
	}
	WindowOpenedOrChanged *struct {
		Window niriWindow `json:"window"`
	}
	WindowFocusChanged *struct {
		ID *int64 `json:"id"`
	}
	WindowClosed *struct {
		ID int64 `json:"id"`
	}
	WindowLayoutsChanged *struct {
		Changes [][2]json.RawMessage `json:"changes"`
	}
}

func niriEvents(ctx context.Context, observe func(Observation)) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd, err := niriCommand(ctx, "--json", "event-stream")
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	defer func() { cancel(); cmd.Wait() }()
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 65536), 8<<20)
	windows := make(map[int64]niriWindow)
	var workspaces map[int64]niriWorkspace
	haveWindows := false
	focus := func(id *int64) {
		for key, window := range windows {
			window.Focused = id != nil && key == *id
			windows[key] = window
		}
	}
	for scanner.Scan() {
		var event niriEvent
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			return err
		}
		switch {
		case event.WindowsChanged != nil:
			windows, haveWindows = indexWindows(event.WindowsChanged.Windows), true
		case event.WorkspacesChanged != nil:
			workspaces = indexWorkspaces(event.WorkspacesChanged.Workspaces)
		case event.WindowOpenedOrChanged != nil:
			window := event.WindowOpenedOrChanged.Window
			windows[window.ID] = window
			if window.Focused {
				focus(&window.ID)
			}
		case event.WindowFocusChanged != nil:
			focus(event.WindowFocusChanged.ID)
		case event.WindowClosed != nil:
			delete(windows, event.WindowClosed.ID)
		case event.WindowLayoutsChanged != nil:
			for _, change := range event.WindowLayoutsChanged.Changes {
				var id int64
				if err := json.Unmarshal(change[0], &id); err != nil {
					return err
				}
				if window, ok := windows[id]; ok {
					if err := json.Unmarshal(change[1], &window.Layout); err != nil {
						return err
					}
					windows[id] = window
				}
			}
		default:
			continue
		}
		if haveWindows && workspaces != nil {
			observe(niriObservation(windows, workspaces))
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return errors.New("niri 事件流已断开")
}
