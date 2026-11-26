package main

import (
	"os/exec"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func TestWaybarCountsOnlyLocalActivity(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Waybar identifies SSH launches through Linux /proc")
	}
	launcher := exec.Command("sleep", "30")
	launcher.Env = []string{"SSH_CONNECTION=test"}
	if err := launcher.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = launcher.Process.Kill()
		_ = launcher.Wait()
	})
	pid := launcher.Process.Pid
	ssh := View{Identity: Identity{OwnerPID: pid, OwnerStart: startTicks(pid)}}
	remote := []View{ssh}
	windowID := int64(42)
	window := View{Identity: Identity{WindowID: &windowID}}
	local := []View{ssh, window}
	snapshot := &Snapshot{
		DesktopConnected: true,
		PendingCount:     2,
		Sessions: []Session{
			{Title: "Local running", Phase: "running"},
			{Title: "SSH running", Phase: "running", Views: remote},
			{Title: "Local question", Phase: "question", Pending: true, Reason: "question", Views: local},
			{Title: "SSH complete", Phase: "done", Pending: true, Reason: "done", Views: remote},
		},
	}
	update := renderWaybar(snapshot)
	if want := "AGENT: 4 (LOCAL: 2, SSH: 2), RUNNING: 1, DONE: 1"; update.Text != want {
		t.Errorf("text = %q, want %q", update.Text, want)
	}
	if !strings.Contains(update.Tooltip, "Local question") || strings.Contains(update.Tooltip, "SSH complete") {
		t.Errorf("unexpected activity in tooltip: %q", update.Tooltip)
	}
	if !slices.Equal(update.Class.([]string), []string{"pending"}) {
		t.Errorf("class = %v, want pending", update.Class)
	}

	snapshot.Sessions = snapshot.Sessions[3:]
	snapshot.PendingCount = 1
	update = renderWaybar(snapshot)
	if want := "AGENT: 1 (LOCAL: 0, SSH: 1), RUNNING: 0, DONE: 0"; update.Text != want {
		t.Errorf("text = %q, want %q", update.Text, want)
	}
	if !slices.Equal(update.Class.([]string), []string{"idle"}) {
		t.Errorf("class = %v, want idle", update.Class)
	}
}
