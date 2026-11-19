package main

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

//go:embed waybar.css
var waybarStyle string

var managedStyle = regexp.MustCompile(`(?s)/\* agent-done:start \*/.*?/\* agent-done:end \*/\n?`)

func stripJSONC(raw []byte) []byte {
	// Quoted strings take precedence over comments. A space preserves token
	// boundaries where a block comment was removed.
	stringOrComment := regexp.MustCompile(`"(?:\\.|[^"\\])*"|//[^\n]*|(?s:/\*.*?\*/)`)
	raw = stringOrComment.ReplaceAllFunc(raw, func(token []byte) []byte {
		if token[0] == '"' {
			return token
		}
		return []byte(" ")
	})
	stringOrTrailingComma := regexp.MustCompile(`"(?:\\.|[^"\\])*"|,\s*[}\]]`)
	return stringOrTrailingComma.ReplaceAllFunc(raw, func(token []byte) []byte {
		if token[0] == ',' {
			return token[1:]
		}
		return token
	})
}

func updateWaybar(input []byte, binary string) ([]byte, error) {
	raw := stripJSONC(input)
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	if !json.Valid(raw) {
		return nil, errors.New("无效的 Waybar 配置")
	}
	bars, isArray := value.([]any)
	if !isArray {
		bars = []any{value}
	}
	for _, entry := range bars {
		bar, ok := entry.(map[string]any)
		if !ok {
			return nil, errors.New("Waybar 根配置必须是对象或对象数组")
		}
		var right []any
		if list, ok := bar["modules-right"]; ok {
			var valid bool
			right, valid = list.([]any)
			if !valid {
				return nil, errors.New("Waybar modules-right 必须是数组")
			}
		}
		found := false
		for _, id := range right {
			if text, ok := id.(string); ok && text == "custom/agent-done" {
				found = true
				break
			}
		}
		if !found {
			bar["modules-right"] = append([]any{"custom/agent-done"}, right...)
		}
		bar["custom/agent-done"] = map[string]any{"exec": shellQuote(binary) + " waybar",
			"return-type": "json", "restart-interval": 2, "tooltip": true}
	}
	updated, err := json.MarshalIndent(value, "", "    ")
	return append(updated, '\n'), err
}

type installFile struct {
	path     string
	new      []byte
	original []byte
	mode     os.FileMode
	present  bool
	link     string
}

func readInstallFile(path string) (installFile, error) {
	file := installFile{path: path, mode: 0600}
	stat, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return file, nil
	}
	if err != nil {
		return file, err
	}
	file.mode, file.present = stat.Mode().Perm(), true
	file.link, _ = os.Readlink(path)
	file.original, err = os.ReadFile(path)
	return file, err
}

func atomicWrite(path string, content []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".agent-done-")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	if err := tmp.Chmod(mode); err != nil {
		return err
	}
	if _, err := tmp.Write(content); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func replaceSymlink(path, target string) error {
	temporary, err := os.MkdirTemp(filepath.Dir(path), ".agent-done-link-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temporary)
	link := filepath.Join(temporary, "link")
	if err := os.Symlink(target, link); err != nil {
		return err
	}
	return os.Rename(link, path)
}

func restoreInstallFiles(files []installFile) error {
	var failures error
	for index := len(files) - 1; index >= 0; index-- {
		file := files[index]
		var err error
		switch {
		case file.link != "":
			err = replaceSymlink(file.path, file.link)
		case file.present:
			err = atomicWrite(file.path, file.original, file.mode)
		default:
			err = os.Remove(file.path)
		}
		if err != nil {
			failures = errors.Join(failures, fmt.Errorf("还原 %s: %w", file.path, err))
		}
	}
	return failures
}

func unitContent(binary string) []byte {
	command := strings.NewReplacer("%", "%%", "$", "$$", "\\", "\\\\", "\"", "\\\"").Replace(binary)
	return []byte(fmt.Sprintf(`[Unit]
Description=Agent attention notifications for niri and Waybar
After=graphical-session.target
PartOf=graphical-session.target

[Service]
Type=exec
ExecStart="%s" serve
Restart=on-failure
RestartSec=2
UMask=0077

[Install]
WantedBy=graphical-session.target
`, command))
}

func install(home, target, stateRoot string) (map[string]string, error) {
	binary := filepath.Join(home, ".local", "bin", "agent-done")
	config := filepath.Join(home, ".config")
	waybar := filepath.Join(config, "waybar")
	configPath := filepath.Join(waybar, "config.jsonc")
	if !exists(configPath) {
		configPath = filepath.Join(waybar, "config")
	}
	settings, err := readInstallFile(configPath)
	if err != nil {
		return nil, err
	}
	if !settings.present {
		return nil, errors.New("没有找到本机 Waybar 配置")
	}
	settings.new, err = updateWaybar(settings.original, binary)
	if err != nil {
		return nil, err
	}
	style, err := readInstallFile(filepath.Join(waybar, "style.css"))
	if err != nil {
		return nil, err
	}
	style.new = []byte(managedStyle.ReplaceAllString(string(style.original), "") + waybarStyle)
	unit, err := readInstallFile(filepath.Join(config, "systemd", "user", "agent-done.service"))
	if err != nil {
		return nil, err
	}
	unit.new = unitContent(binary)
	var oldLink string
	if _, err := os.Lstat(binary); err == nil {
		oldLink, err = os.Readlink(binary)
		if err != nil {
			return nil, fmt.Errorf("%s 已存在且不是项目符号链接", binary)
		}
		prior := oldLink
		if !filepath.IsAbs(prior) {
			prior = filepath.Join(filepath.Dir(binary), prior)
		}
		root := filepath.Dir(filepath.Dir(target))
		pythonEntry := filepath.Join(root, ".venv", "bin", "agent-done")
		if prior != target && prior != pythonEntry {
			return nil, fmt.Errorf("%s 已存在且指向其他入口", binary)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	backup := filepath.Join(stateRoot, "backups", time.Now().Format("20060102-150405.000000000"))
	if err := privateDir(backup); err != nil {
		return nil, err
	}
	files := []installFile{settings, style, unit}
	for _, file := range files {
		if file.present {
			if err := os.WriteFile(filepath.Join(backup, filepath.Base(file.path)), file.original, file.mode); err != nil {
				return nil, err
			}
		}
	}
	if oldLink != "" {
		if err := os.Symlink(oldLink, filepath.Join(backup, "agent-done")); err != nil {
			return nil, err
		}
	}
	result := map[string]string{"binary": binary, "backup": backup, "unit": unit.path, "waybar_config": configPath, "style": style.path}
	manifest, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(backup, "manifest.json"), manifest, 0600); err != nil {
		return nil, err
	}
	for _, path := range []string{filepath.Dir(binary), filepath.Dir(unit.path)} {
		if err := os.MkdirAll(path, 0755); err != nil {
			return nil, err
		}
	}
	for index, file := range files {
		if err := atomicWrite(file.path, file.new, file.mode); err != nil {
			return nil, errors.Join(fmt.Errorf("安装 %s 失败（备份 %s）: %w", file.path, backup, err),
				restoreInstallFiles(files[:index]))
		}
	}
	if err := replaceSymlink(binary, target); err != nil {
		return nil, errors.Join(fmt.Errorf("安装入口失败（备份 %s）: %w", backup, err), restoreInstallFiles(files))
	}
	return result, nil
}
