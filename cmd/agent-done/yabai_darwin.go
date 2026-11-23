package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

type yabaiWindow struct {
	ID        int64 `json:"id"`
	PID       int   `json:"pid"`
	Display   int   `json:"display"`
	Space     int   `json:"space"`
	Floating  bool  `json:"is-floating"`
	Focused   bool  `json:"has-focus"`
	Minimized bool  `json:"is-minimized"`
}

func yabaiCommand(ctx context.Context, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	raw, err := exec.CommandContext(ctx, "yabai", append([]string{"-m"}, args...)...).Output()
	if exit, ok := err.(*exec.ExitError); ok && len(exit.Stderr) != 0 {
		return nil, fmt.Errorf("yabai: %s", strings.TrimSpace(string(exit.Stderr)))
	}
	if err != nil {
		return nil, fmt.Errorf("yabai: %w", err)
	}
	return raw, nil
}

func yabaiSnapshot(ctx context.Context) (Observation, error) {
	raw, err := yabaiCommand(ctx, "query", "--windows")
	if err != nil {
		return Observation{}, err
	}
	var windows []yabaiWindow
	if err := json.Unmarshal(raw, &windows); err != nil {
		return Observation{}, fmt.Errorf("yabai 窗口数据: %w", err)
	}
	result := Observation{Backend: "yabai", Windows: make([]Window, 0, len(windows))}
	starts := make(map[int]string)
	for _, w := range windows {
		start, ok := starts[w.PID]
		if !ok {
			start = startTicks(w.PID)
			starts[w.PID] = start
		}
		result.Windows = append(result.Windows, Window{ID: w.ID, PID: w.PID, Start: start,
			Position: &Position{Output: strconv.Itoa(w.Display), Workspace: w.Space, Floating: w.Floating}})
		if w.Focused {
			id := w.ID
			result.Focused = &id
		}
	}
	return result, nil
}

func yabaiFocus(ctx context.Context, window Window) error {
	id := strconv.FormatInt(window.ID, 10)
	raw, err := yabaiCommand(ctx, "query", "--windows", "--window", id)
	if err != nil {
		return err
	}
	var target yabaiWindow
	if err := json.Unmarshal(raw, &target); err != nil {
		return err
	}
	if target.ID != window.ID || target.PID != window.PID || !sameProcess(target.PID, window.Start) {
		return fmt.Errorf("yabai 目标窗口 %s 已失效", id)
	}
	if target.Minimized {
		if _, err := yabaiCommand(ctx, "window", id, "--deminimize"); err != nil {
			return err
		}
	}
	_, err = yabaiCommand(ctx, "window", "--focus", id)
	return err
}
