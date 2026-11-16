"""Reversible per-user integration. Existing desktop files are backed up."""

from datetime import datetime
import json
from pathlib import Path
import re
import shutil
import sys
import os
import tempfile

from .paths import private_dir, state_dir


def jsonc(text):
    # Remove comments only outside JSON strings; preserve quoted URLs and escapes.
    pattern = r'"(?:\\.|[^"\\])*"|//[^\n]*|/\*[\s\S]*?\*/'
    stripped = re.sub(pattern, lambda m: m.group() if m.group().startswith('"') else "", text)
    stripped = re.sub(r'"(?:\\.|[^"\\])*"|,\s*(?=[}\]])',
                      lambda m: m.group() if m.group().startswith('"') else "", stripped)
    return json.loads(stripped)


def executable():
    candidate = Path(sys.executable).parent / "agent-done"
    if not candidate.is_file():
        raise RuntimeError("请先在项目目录执行 uv sync")
    return candidate.absolute()


def write_file(path, text):
    mode = path.stat().st_mode & 0o777 if path.exists() else 0o600
    with tempfile.NamedTemporaryFile("w", dir=path.parent, prefix=".agent-done-", delete=False) as stream:
        temporary = Path(stream.name)
        stream.write(text)
    temporary.chmod(mode)
    os.replace(temporary, path)


def install():
    if sys.platform == "darwin":
        raise RuntimeError("macOS 目前使用手动服务与快捷键，请参阅 docs/macos.md；install 仅安装 Linux/Waybar 集成")
    target = executable()
    binary = Path.home() / ".local/bin/agent-done"
    binary.parent.mkdir(parents=True, exist_ok=True)
    if binary.exists() or binary.is_symlink():
        if binary.resolve() != target.resolve():
            raise RuntimeError(f"{binary} 已存在且不是本项目入口")
    else:
        binary.symlink_to(target)
    backup = private_dir(state_dir() / "backups" / datetime.now().strftime("%Y%m%d-%H%M%S-%f"))
    waybar = Path.home() / ".config/waybar"
    config_path = next((p for p in (waybar / "config.jsonc", waybar / "config") if p.exists()), None)
    if config_path is None:
        raise RuntimeError("没有找到本机 Waybar 配置")
    original = config_path.read_text()
    data = jsonc(original)
    bars = data if isinstance(data, list) else [data]
    module = {"exec": str(binary) + " waybar", "return-type": "json", "restart-interval": 2, "tooltip": True}
    for bar in bars:
        if not isinstance(bar, dict):
            raise RuntimeError("Waybar 根配置必须是对象或对象数组")
        right = bar.setdefault("modules-right", [])
        if "custom/agent-done" not in right:
            right.insert(0, "custom/agent-done")
        bar["custom/agent-done"] = module
    shutil.copy2(config_path, backup / config_path.name)
    write_file(config_path, json.dumps(data, ensure_ascii=False, indent=4) + "\n")
    style = waybar / "style.css"
    if style.exists():
        shutil.copy2(style, backup / "style.css")
        css = style.read_text()
    else:
        css = ""
    css = re.sub(r'/\* agent-done:start \*/[\s\S]*?/\* agent-done:end \*/\n?', '', css)
    css += """
/* agent-done:start */
#custom-agent-done {
    padding: 0 10px;
    margin: 3px 2px;
    border-radius: 6px;
    color: #89b4fa;
}
#custom-agent-done.pending {
    background: rgba(49, 50, 68, 0.9);
    color: #f9e2af;
}
#custom-agent-done.disconnected { color: #f38ba8; }
/* agent-done:end */
"""
    write_file(style, css)
    systemd = Path.home() / ".config/systemd/user"
    systemd.mkdir(parents=True, exist_ok=True)
    unit = systemd / "agent-done.service"
    if unit.exists():
        shutil.copy2(unit, backup / unit.name)
    # systemd expands percent specifiers even inside quoted command arguments.
    command = str(binary).replace("%", "%%").replace('"', '\\"')
    write_file(unit, f'''[Unit]
Description=Agent attention notifications for niri and Waybar
After=graphical-session.target
PartOf=graphical-session.target

[Service]
ExecStart="{command}" serve
Restart=on-failure
RestartSec=2
UMask=0077

[Install]
WantedBy=graphical-session.target
''')
    (backup / "manifest.json").write_text(json.dumps({"waybar_config": str(config_path), "style": str(style),
                                                     "unit": str(unit), "binary": str(binary)}, indent=2))
    return {"binary": str(binary), "backup": str(backup), "unit": str(unit), "waybar_config": str(config_path)}
