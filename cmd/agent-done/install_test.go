package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestInstallSwitchesExistingPythonLinkWithBackup(t *testing.T) {
	home := t.TempDir()
	repo := filepath.Join(home, "repo")
	target := filepath.Join(repo, "dist", "agent-done")
	python := filepath.Join(repo, ".venv", "bin", "agent-done")
	waybar := filepath.Join(home, ".config", "waybar")
	link := filepath.Join(home, ".local", "bin", "agent-done")
	for _, dir := range []string{waybar, filepath.Dir(link)} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(python, link); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(waybar, "config.jsonc")
	original := []byte(`{"modules-right": ["clock",], /* comment */ "link": "https://example.invalid"}`)
	if err := os.WriteFile(config, original, 0640); err != nil {
		t.Fatal(err)
	}
	result, err := install(home, target, filepath.Join(home, "state"))
	if err != nil {
		t.Fatal(err)
	}
	if installed, err := os.Readlink(link); err != nil || installed != target {
		t.Fatalf("link = %q, error = %v", installed, err)
	}
	if previous, err := os.Readlink(filepath.Join(result["backup"], "agent-done")); err != nil || previous != python {
		t.Fatalf("backup link = %q, error = %v", previous, err)
	}
	backup, err := os.ReadFile(filepath.Join(result["backup"], "config.jsonc"))
	if err != nil || !bytes.Equal(backup, original) {
		t.Fatalf("backup config = %q, error = %v", backup, err)
	}
	updated, err := os.ReadFile(config)
	if err != nil || !bytes.Contains(updated, []byte("custom/agent-done")) {
		t.Fatalf("new config = %q, error = %v", updated, err)
	}
}
