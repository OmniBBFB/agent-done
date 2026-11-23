package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

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
