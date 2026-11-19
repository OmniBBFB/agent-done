package main

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func homeDir() string {
	home, _ := os.UserHomeDir()
	return home
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func stateDir() string {
	return envOr("AGENT_DONE_STATE_DIR", filepath.Join(envOr("XDG_STATE_HOME", filepath.Join(homeDir(), ".local", "state")), "agent-done"))
}

func runtimeDir() string {
	if explicit := os.Getenv("AGENT_DONE_RUNTIME_DIR"); explicit != "" {
		return explicit
	}
	base := os.Getenv("XDG_RUNTIME_DIR")
	if base == "" {
		candidate := filepath.Join("/run/user", strconv.Itoa(os.Getuid()))
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			base = candidate
		}
	}
	if base == "" {
		return filepath.Join(stateDir(), "run")
	}
	return filepath.Join(base, "agent-done")
}

func controlSocket() string { return filepath.Join(runtimeDir(), "control.sock") }

func codexSocket() string {
	return filepath.Join(envOr("CODEX_HOME", filepath.Join(homeDir(), ".codex")), "app-server-control", "app-server-control.sock")
}

func privateDir(path string) error {
	if err := os.MkdirAll(path, 0700); err != nil {
		return err
	}
	return os.Chmod(path, 0700)
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func procFields(pid int) []string {
	raw, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return nil
	}
	index := strings.LastIndexByte(string(raw), ')')
	if index < 0 {
		return nil
	}
	return strings.Fields(string(raw[index+1:]))
}

func startTicks(pid int) string {
	fields := procFields(pid)
	if len(fields) <= 19 {
		return ""
	}
	return fields[19]
}

func ancestors(pid int) []int {
	var chain []int
	for pid > 1 {
		chain = append(chain, pid)
		fields := procFields(pid)
		if len(fields) < 2 {
			break
		}
		parent, _ := strconv.Atoi(fields[1])
		if containsPID(chain, parent) {
			break
		}
		pid = parent
	}
	return chain
}

func containsPID(pids []int, candidate int) bool {
	for _, pid := range pids {
		if candidate == pid {
			return true
		}
	}
	return false
}

func codexPID(launcher int) int {
	pending := []int{launcher}
	visited := map[int]bool{}
	for len(pending) > 0 {
		pid := pending[0]
		pending = pending[1:]
		if visited[pid] {
			continue
		}
		visited[pid] = true
		root := filepath.Join("/proc", strconv.Itoa(pid))
		name, _ := os.ReadFile(filepath.Join(root, "comm"))
		if strings.TrimSpace(string(name)) == "codex" {
			return pid
		}
		children, _ := os.ReadFile(filepath.Join(root, "task", strconv.Itoa(pid), "children"))
		for _, child := range strings.Fields(string(children)) {
			if value, err := strconv.Atoi(child); err == nil {
				pending = append(pending, value)
			}
		}
	}
	return launcher
}

func sameProcess(pid int, start string) bool {
	return pid > 0 && start != "" && startTicks(pid) == start
}

func connectionID() string {
	var id [16]byte
	rand.Read(id[:])
	return hex.EncodeToString(id[:])
}
