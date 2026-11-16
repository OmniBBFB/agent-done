# macOS 实验性接入

已实现 yabai 和原生 Accessibility 两条接入路径，尚未在 macOS 实机验收。Linux/niri 的测试结果不代表 macOS 已可用。第一期使用本地 Codex CLI、独立 OS 终端窗口和手动服务/快捷键；终端内部标签页、分屏不在支持范围。

## 安装与后端选择

在项目目录执行 `uv sync`。macOS 会安装 psutil 与 PyObjC ApplicationServices，Linux 不安装这些平台依赖。

服务启动时选择后端：

| 环境变量 | 接入方式 |
| --- | --- |
| `AGENT_DONE_DESKTOP=yabai` | 通过 PATH 中的 yabai 查询和聚焦窗口，需已有运行中的 yabai |
| `AGENT_DONE_DESKTOP=macos` | 通过公开 Accessibility 接口持有原生窗口引用，不依赖 yabai |

macOS 默认选择 `macos`，Linux 默认选择 `niri`。设置非法后端会报错；指定 yabai 不可用时不会静默改用另一套窗口 ID。

先在一个终端中运行服务，并保持该命令运行。例如：

```sh
AGENT_DONE_DESKTOP=yabai .venv/bin/agent-done serve
```

原生接入将 `yabai` 改成 `macos`。在其他独立终端窗口使用本项目的绝对入口路径启动：

```sh
/path/to/agent-done/.venv/bin/agent-done codex -C /path/to/project
/path/to/agent-done/.venv/bin/agent-done status
/path/to/agent-done/.venv/bin/agent-done jump
/path/to/agent-done/.venv/bin/agent-done doctor
```

客户端默认使用服务已选择的后端。若客户端显式设置 `AGENT_DONE_DESKTOP`，应与服务一致。更换后端需要停止旧服务、以新配置启动，并重新启动或恢复受跟踪会话。

当前 `install` 命令仍仅安装 Linux/Waybar 集成，不会自动写入 launchd、菜单栏或 macOS 键位。可以在自己的快捷键工具中绑定 `/path/to/agent-done/.venv/bin/agent-done jump`；这需要单独验证物理按键路径。

## 身份、权限与失败行为

包装器通过源进程父链找到终端进程；同一终端进程有多个 OS 窗口时，登记需要用当前焦点消歧。请在源窗口聚焦时启动，启动登记期间保持该窗口聚焦；后台脚本延迟启动不在当前验证范围。

yabai 路径使用窗口 ID、窗口进程及其创建身份。请按已安装 yabai 的要求配置权限；本项目不修改系统安全设置，也不保证跨 Space、原生全屏或特定 yabai 版本的行为。

原生路径需要给运行服务的 Python 进程相应的辅助功能权限。`doctor` 的 `desktop_error` 会显示权限或查询错误；它不会自动替用户更改权限。该实现使用公开 AX 接口，不使用标题/CWD 猜窗口、不调用私有 CGS Space 接口，也不以应用激活作为准确窗口已查看的证明。

原生 AX 引用由现有用户服务持有，窗口 ID 只在该服务进程生命周期内有效。服务重启后，旧原生绑定会停用，待查看状态保留；需重新启动或恢复对应会话登记。它们不能像 niri/yabai 的数字窗口 ID 一样直接恢复。

两个 macOS 后端在成功查询后间隔 0.3 秒再次观察桌面。请求聚焦不会清除提醒，只有观察到同一后端的准确目标窗口聚焦才确认已查看。权限撤销、查询失败、目标不唯一或不支持的聚焦路径都不会改为选择任意同应用窗口。

## 在 macOS 机器验收

先安装可从命令行启动的 Kitty 或 Alacritty。下面的工具使用当前解释器和代码启动隔离服务、创建三个临时终端窗口，不调用模型，也不修改生产状态库或服务：

```sh
uv run python tools/validate_desktop.py --backend yabai --terminal kitty --output outputs/macos-yabai/result.json
uv run python tools/validate_desktop.py --backend macos --terminal kitty --output outputs/macos-native/result.json
```

工具验证来源窗口互异、准确窗口进程、授权/提问计数 `2→1→0`、实际焦点与请求仍等待、空队列不改变焦点。结束后关闭自己的测试窗口。原焦点可识别时尝试恢复，并单独记录恢复结果；原生后端在原窗口尚未登记时可能无法恢复，该情况会明确写入报告。

该入口需要真实图形桌面及权限。辅助功能权限也适用于隔离服务的 Python；工具不能在普通无桌面 CI 中证明聚焦成功。测试提醒是数据夹具，不是真实 Codex 事件；物理快捷键、跨显示器/Space、最小化和原生全屏必须单独测试并记录版本。

完成窗口机制验收后，再使用正常 hook 信任流程验证真实 Codex 新建/恢复、完成、授权和提问。未通过实机验收的组合继续标为实验性。

## 路径与诊断

当前复用 Unix IPC 和 SQLite，默认状态目录及日志位置与 [README](../README.md#实现与数据) 相同。`AGENT_DONE_RUNTIME_DIR` 可显式指定 agent-done 的控制 socket/锁/转发 socket 所在目录；与图形桌面的 `XDG_RUNTIME_DIR` 分开，主要用于隔离验收。

`doctor` 返回实际服务后端、`desktop_connected`、`desktop_error` 和实验性标记；`status --json` 中每个 view 带 `backend`、窗口进程身份和 `active`。位置元数据不可用时不伪造行列。报告和逐次验收记录放在本地 `outputs/`，不提交。
