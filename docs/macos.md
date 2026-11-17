# macOS 实验性接入

已实现 yabai 和原生 Accessibility 两条实验性接入路径。yabai 已在 macOS + Alacritty 上通过独立窗口、待处理计数、焦点确认和真实 Codex 完成事件的首轮实测；AX 的真实窗口验收仍需要本机辅助功能授权。跨 Space、原生全屏等路径需分别验收，Linux/niri 的测试结果不能替代 macOS 验收。使用本地 Codex CLI 和独立 OS 终端窗口；终端内部标签页、分屏不在支持范围。

## 安装与后端选择

开始前准备 Git、uv、Python 3.11+，以及已完成登录、可独立运行的 Codex CLI。首次窗口机制验收还需要 Kitty 或 Alacritty，其可执行文件须在 PATH 中；仅将终端应用装入 Applications 不代表命令已可用。请从本机图形会话中的终端执行，不通过 SSH 或 tmux/screen 测试。

获取代码并安装项目：

```sh
git clone https://github.com/OmniBBFB/agent-done.git
cd agent-done
uv sync
```

已有检出目录则先在没有未保存改动的情况下执行 `git pull --ff-only`，再执行 `uv sync`。macOS 会安装 psutil 与 PyObjC ApplicationServices，Linux 不安装这些平台依赖。

在 macOS 上检查入口和依赖：

```sh
codex --version
.venv/bin/agent-done --version
.venv/bin/python -c 'import psutil, HIServices, CoreFoundation, AppKit; print("macOS dependencies OK")'
command -v kitty
```

使用 Alacritty 时最后一行改为 `command -v alacritty`。Codex CLI 还需支持 `--remote` 和 `app-server daemon`，可通过 `codex --help` 与 `codex app-server daemon --help` 检查；项目复用它的本地 daemon，未另行实现模型客户端。

服务启动时选择后端：

| 环境变量 | 接入方式 |
| --- | --- |
| `AGENT_DONE_DESKTOP=yabai` | 通过 PATH 中的 yabai 查询和聚焦窗口，需已有运行中的 yabai |
| `AGENT_DONE_DESKTOP=macos` | 通过公开 Accessibility 接口持有原生窗口引用，不依赖 yabai |

macOS 默认选择 `macos`，Linux 默认选择 `niri`。设置非法后端会报错；指定 yabai 不可用时不会静默改用另一套窗口 ID。

先选择一条路径，在一个终端中以前台方式运行服务，并保持该命令运行：

```sh
# 原生 macOS
AGENT_DONE_DESKTOP=macos .venv/bin/agent-done serve
```

或者，在已有 yabai 的机器上运行：

```sh
AGENT_DONE_DESKTOP=yabai .venv/bin/agent-done serve
```

两条命令择一执行。服务为每用户单实例，不能同时启动两个后端。需要停止手动前台服务时，在该服务终端按 Ctrl-C。

在其他独立终端窗口使用本项目的绝对入口路径启动；下文的 `/path/to/agent-done` 和 `/path/to/project` 均需替换为真实路径：

```sh
/path/to/agent-done/.venv/bin/agent-done codex -C /path/to/project
/path/to/agent-done/.venv/bin/agent-done status
/path/to/agent-done/.venv/bin/agent-done jump
/path/to/agent-done/.venv/bin/agent-done doctor
```

首次启动 Codex 后，在 `/hooks` 中审阅并信任当前虚拟环境的 `python -m agent_done hook` 命令。信任完成后再进行真实 hook 验收；虚拟环境的实际解释器路径可能显示为 `python3`。已有普通 Codex 会话需先正常退出，再通过以下入口恢复，才能登记窗口：

```sh
/path/to/agent-done/.venv/bin/agent-done codex resume <会话ID>
```

使用这个恢复入口，不复制 Codex 退出提示中带临时 `proxy.sock` 的命令。更新代码后，已经运行的服务和包装器不会自动重新加载；先正常退出受跟踪 CLI，停止旧服务，重新启动服务，再恢复对应会话。

客户端默认使用服务已选择的后端。若客户端显式设置 `AGENT_DONE_DESKTOP`，应与服务一致。更换后端需要停止旧服务、以新配置启动，并重新启动或恢复受跟踪会话。

当前 `install` 命令仍仅安装 Linux/Waybar 集成，不会自动写入 launchd 或 macOS 键位。可以在自己的快捷键工具中绑定 `/path/to/agent-done/.venv/bin/agent-done jump`；这需要单独验证物理按键路径。

菜单栏入口使用 `/path/to/agent-done/.venv/bin/agent-done menubar` 启动，显示 `AD 0` 等待处理会话计数。菜单包含待处理会话、桌面连接错误和跳到下一个会话的操作。退出菜单栏不会停止后台服务。该入口依赖已运行的服务，未连接时显示 `AD ?`，桌面后端断连时显示 `AD 0 !` 等带叹号的计数，悬浮或展开菜单可查看原因。不弹出 Dock 图标；如需登录自启，可将此命令单独配置为用户 LaunchAgent。

菜单栏通过 IPC 订阅接收状态变化，并立即交给主线程刷新，不按秒轮询。跳转由服务在操作前检查窗口及进程身份，聚焦后立即回读实际焦点；尚未聚焦时仍由桌面监听确认，不能仅因聚焦命令成功就清除提醒。

## 身份、权限与失败行为

包装器通过源进程父链找到终端进程；同一终端进程有多个 OS 窗口时，登记需要用当前焦点消歧。请在源窗口聚焦时启动，启动登记期间保持该窗口聚焦；后台脚本延迟启动不在当前验证范围。

yabai 路径使用窗口 ID、窗口进程及其创建身份。请按已安装 yabai 的要求配置权限；本项目不修改系统安全设置，也不保证跨 Space、原生全屏或特定 yabai 版本的行为。

原生路径需要给运行服务的 Python 进程相应的辅助功能权限。`doctor` 的 `desktop_error` 会显示权限或查询错误；它不会自动替用户更改权限。按错误提示在系统辅助功能授权界面处理运行宿主的权限，授权后重启服务并重新检查。该实现使用公开 AX 接口，不使用标题/CWD 猜窗口、不调用私有 CGS Space 接口，也不以应用激活作为准确窗口已查看的证明。

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

完成窗口机制验收后，再进行真实 Codex 流程：

1. 用上述入口在两个独立 OS 窗口启动或恢复 Codex，确认 `status --json` 中存在对应会话及 `active=true` 的 view。
2. 让其中一个会话在后台完成，确认 `status` 出现待查看项；在第三个终端运行 `jump`，确认切到准确窗口后计数减少。
3. 分别验证真正交给用户的工具授权、带选项/输入框的结构化提问，以及异步问题卡片。问题出现前将焦点移到其他窗口；切回后只清除提醒，不能自动批准或提交答案。
4. 验证空队列不跳转、目标窗口关闭、正常退出后恢复，再验证自己配置的全局快捷键。跨 Space、跨显示器、最小化及原生全屏分别记录结果。

原生接入在任何来源窗口尚未登记时，`desktop_connected=true` 只表示 AX 查询成功，不能证明会话绑定或跳转已通过。若启动时提示“提醒暂不可用，继续启动 Codex”，该次是普通 Codex 回退，不能算作接入成功。

未完成全量实机验收的组合继续标为实验性。yabai + Alacritty 已验证基础窗口导航、真实完成事件及本机 skhd 快捷键；AX、其他快捷键工具及跨 Space 等路径仍需分别验证。逐次结果保存在本地 `outputs/`，不随仓库分发。

## 完成后没有计数或无法跳转

先运行 `agent-done doctor`。`service_available=true` 只表示 IPC 可用，还需确认 `desktop_connected=true`。原生后端提示辅助功能权限不足时，应授权实际运行服务的可执行文件，随后重启服务；改名或更换解释器后应重新检查权限。已有正常工作的 yabai 可以显式选择 `AGENT_DONE_DESKTOP=yabai`，不会自动切换后端。

接着检查 `agent-done status --json` 中是否存在当前会话及 `active=true` 的 view。只用普通 `codex` 启动，或者包装器提示“提醒暂不可用”后回退的会话，不会被跟踪。修复权限或切换后端后，需退出并用 `agent-done codex resume <会话ID>` 重新登记。后台完成才增加提醒；在准确源窗口已聚焦时完成会视为已查看。

有待处理项时先执行 `agent-done jump`。命令能跳转而快捷键不能时，检查快捷键工具的绑定、辅助功能权限及日志。模拟按键未触发不能证明物理按键失效，需从其他窗口实际按键验证。

## 路径与诊断

当前复用 Unix IPC 和 SQLite。默认未设置 XDG/agent-done 路径覆盖时，macOS 状态和日志位于 `~/.local/state/agent-done/`，控制 socket 位于 `~/.local/state/agent-done/run/control.sock`。`AGENT_DONE_RUNTIME_DIR` 可显式指定 agent-done 的控制 socket/锁/转发 socket 所在目录；与图形桌面的 `XDG_RUNTIME_DIR` 分开，主要用于隔离验收。详细路径规则见 [README](../README.md#实现与数据)。

`doctor` 返回实际服务后端、`desktop_connected`、`desktop_error` 和实验性标记；`status --json` 中每个 view 带 `backend`、窗口进程身份和 `active`。位置元数据不可用时不伪造行列。报告和逐次验收记录放在本地 `outputs/`，不提交。

反馈问题时附上 macOS 版本、芯片架构、终端及 Codex 版本、Git 提交号、后端名称，以及 `doctor`、`status --json` 和验收 `result.json`。同时说明焦点当时在哪个窗口、是否涉及 Space/全屏/最小化；运行日志在 `~/.local/state/agent-done/agent-done.log`。分享前检查其中的会话标题和本地路径。
