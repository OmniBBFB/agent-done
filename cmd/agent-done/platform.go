package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"strconv"
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

func containsPID(pids []int, candidate int) bool {
	for _, pid := range pids {
		if candidate == pid {
			return true
		}
	}
	return false
}

func sameProcess(pid int, start string) bool {
	return pid > 0 && start != "" && startTicks(pid) == start
}

func isSSHSession(session Session) bool {
	for _, view := range session.Views {
		if view.WindowID != nil || !isSSHProcess(view.OwnerPID, view.OwnerStart) {
			return false
		}
	}
	return len(session.Views) > 0
}

func isSSHProcess(pid int, start string) bool {
	if !sameProcess(pid, start) {
		return false
	}
	env, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "environ"))
	if err != nil {
		return false
	}
	for entry := range bytes.SplitSeq(env, []byte{0}) {
		for _, key := range []string{"SSH_CONNECTION=", "SSH_CLIENT=", "SSH_TTY="} {
			if len(entry) > len(key) && bytes.HasPrefix(entry, []byte(key)) {
				return true
			}
		}
	}
	return false
}

func connectionID() string {
	var id [16]byte
	rand.Read(id[:])
	return hex.EncodeToString(id[:])
}
