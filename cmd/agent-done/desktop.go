package main

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"time"
)

type desktopBackend struct {
	name     string
	snapshot func(context.Context) (Observation, error)
	focus    func(context.Context, Window) error
	events   func(context.Context, func(Observation)) error
	register func(context.Context, []int) error
	close    func()
}

func defaultDesktop() string {
	if runtime.GOOS == "darwin" {
		return "macos"
	}
	return "niri"
}

func selectDesktop(name string) (desktopBackend, error) {
	switch {
	case runtime.GOOS == "linux" && name == "niri":
		return desktopBackend{
			name: name, snapshot: niriSnapshot, events: niriEvents,
			focus: func(ctx context.Context, w Window) error { return niriFocus(ctx, w.ID) },
		}, nil
	case runtime.GOOS == "darwin":
		return macDesktop(name)
	default:
		return desktopBackend{}, fmt.Errorf("%s 不支持桌面后端 %q", runtime.GOOS, name)
	}
}

func (s *Service) pollDesktop(ctx context.Context) error {
	ticker := time.NewTicker(300 * time.Millisecond)
	defer ticker.Stop()
	for {
		s.desktopMu.Lock()
		desktop, err := s.desktop.snapshot(ctx)
		if err == nil {
			err = s.observe(desktop)
		}
		s.desktopMu.Unlock()
		if err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

var errSourceWindowNotFound = errors.New("无法从源进程找到终端窗口，请在本机独立终端窗口内运行")

func identifyWindow(observation Observation, chain []int) (*Identity, error) {
	for _, ancestor := range chain {
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
		return &Identity{Backend: observation.Backend, WindowID: &window.ID, WindowPID: window.PID, WindowStart: window.Start}, nil
	}
	return nil, errSourceWindowNotFound
}
