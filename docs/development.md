# 开发说明

agent-done 在现有终端之外维护 CLI agent 的待查看状态，并通过桌面窗口管理器定位和聚焦会话。当前已验证本机 Codex CLI + niri + Waybar，另有尚未实机验证的 macOS/yabai 与原生 AX 接入；每个 agent 使用独立 OS 窗口。

安装和日常操作见 [README](../README.md)。本文介绍当前架构、开发入口和验证方法。

## 开发环境

需要 Python 3.11+ 和 uv。Linux 真实桌面流程还需要可用的 Codex CLI、niri、Waybar，以及 Kitty 或 Alacritty；macOS 的依赖、手动服务及验收步骤见 [macOS 接入说明](macos.md)。

```sh
uv sync
uv run python -m unittest discover -s tests -v
```

自动测试覆盖状态与请求生命周期、会话绑定、导航目标选择、配置合并、服务订阅和 Waybar 输出。它们不能替代真实终端、Codex hook 信任或桌面焦点行为的验收。

## 数据流与源码职责

```text
终端中的 Codex TUI → 本地 Unix WebSocket 转发器 → Codex 共享 daemon
                         ↓ 会话身份与事件
稳定命令 hook → agent-done 服务 / SQLite ← niri 窗口和焦点事件
                         ↓
                Waybar 订阅 / jump 命令
                                  ↓
                     niri 聚焦 → 焦点事件确认已查看
```

| 模块 | 职责 |
| --- | --- |
| [cli.py](../src/agent_done/cli.py) | 命令入口、hook 输入筛选和诊断 |
| [codex.py](../src/agent_done/codex.py) | 启动包装、协议转发、会话创建/恢复绑定、完成及请求观察 |
| [process.py](../src/agent_done/process.py) | Linux/macOS 的进程父链、创建身份和存活核对 |
| [desktop.py](../src/agent_done/desktop.py) | 后端选择、来源登记、快照、聚焦请求与观察接口 |
| [desktop_niri.py](../src/agent_done/desktop_niri.py)、[desktop_yabai.py](../src/agent_done/desktop_yabai.py)、[desktop_macos.py](../src/agent_done/desktop_macos.py) | 系统交互、准确窗口身份和标准化桌面观察 |
| [state.py](../src/agent_done/state.py) | SQLite 状态、去重、待查看标记、请求与绑定生命周期 |
| [runtime.py](../src/agent_done/runtime.py) | 用户服务、控制请求、状态订阅和失效进程清理 |
| [navigation.py](../src/agent_done/navigation.py) | 从待查看队列选择有效目标并通过桌面接口请求聚焦 |
| [ipc.py](../src/agent_done/ipc.py)、[paths.py](../src/agent_done/paths.py) | 本地控制协议和路径 |
| [waybar.py](../src/agent_done/waybar.py)、[install.py](../src/agent_done/install.py) | 状态栏输出、用户入口与配置安装 |

启动包装器沿进程父链匹配桌面后端提供的窗口 PID，从会话创建或恢复响应获取会话 ID。同一进程有多个窗口时，在来源登记阶段用其聚焦窗口消歧；提醒服务使用已经建立的绑定定位来源，不使用事件发生时的前台窗口推测来源。

终端负责普通输入输出与画面显示；生产代码未使用 Kitty 远程控制、终端通知序列或内部标签/分屏接口。窗口身份包含后端，原生 AX 窗口引用只在服务内使用；支持终端内部区域时仍需另行适配。

## 维护时需要保留的行为

- `pending` 表示尚未查看，覆盖完成、失败、待授权和待回答。查看提醒不等于解决请求。
- `jump` 与状态栏使用同一待查看条件，按会话最近一次状态更新时间升序选择有效目标；空队列不切换窗口。
- 跳转命令不主动清除提醒。实际聚焦由桌面事件确认，聚焦失败时提醒保留。
- 一个窗口内未显示的会话不能仅因窗口聚焦而被确认已查看。内部标题生成和子 agent 也不计入独立待查看会话。
- 进程身份同时检查 PID 和启动标识；退出、关闭窗口、断开绑定时清理，避免 PID 复用导致误认。
- `Stop` hook 是停止候选，以协议的 `turn/completed` 确认完成；权限 hook 不单独证明需要人工审批，以真实客户端请求为准。
- 结构化提问包括 `item/tool/requestUserInput` 等服务端 RPC，以及 `item/completed` 中 `delivery=async`、带非空 `questions` 的 `agentMessage`。异步问题按连接和消息 ID 去重，不解析问题正文，也不等待回合完成才提醒；回合完成和新用户回合继续使用现有状态清理规则。
- 提醒服务不可用时放行 Codex；启动时跟踪失败会回退普通启动。服务恢复后需重新启动或恢复会话登记。
- hook 仅发送事件身份字段，不记录原始问题、工具参数或助手正文。会话标题和工作目录用于显示，会进入状态库。

后端契约、服务持有窗口引用与旧 niri 数据迁移见 [桌面适配设计](desktop-adaptation-design.md)；macOS 运行和验收见 [macOS 接入说明](macos.md)。

## 桌面验证

修改状态或协议逻辑后运行相关自动测试。涉及窗口定位、焦点、Codex 版本或 hook 的修改，还需验证真实桌面路径。

跳转工具可独立验证真实窗口身份、跨工作区聚焦和查看确认：

```sh
uv run python tools/validate_jump.py --terminal kitty --output /tmp/agent-done-jump.json
uv run python tools/validate_jump.py --terminal kitty --pending-requests --output /tmp/agent-done-requests.json
```

该工具调用已安装的 `~/.local/bin/agent-done`，运行前确认该入口指向待验证版本。它创建隔离服务和两个真实终端窗口，临时切换焦点，结束后关闭测试窗口并恢复原焦点。可使用 `--terminal alacritty` 检查 Alacritty。

这些提醒来自测试数据，不会发起模型请求，也不替代真实完成、授权或提问验证；工具调用跳转命令，不能证明物理快捷键绑定正确。快捷键还应手动验证：从其他应用按键，检查目标、计数变化、连续跳转和空队列行为。

完整 Codex 交互使用 [acceptance.py](../tools/acceptance.py) 及其辅助工具，先阅读其 `--help`。普通 hook 信任、新建/恢复、真实审批和结构化提问需要分别验证。早期 `probe_*` 和其他实验脚本保留用于诊断，不是生产入口。

Linux 进程接入使用 `/proc`，macOS 使用 psutil；用户服务继续使用 Unix socket 和 `fcntl`。安装器仍仅生成 Linux 的 systemd/Waybar 集成。Windows 不在当前范围。Codex 实验协议更新后，应重新检查会话绑定、事件覆盖和交互路径。

文档和过程产物的存放、跟踪规则见 [AGENTS.md](../AGENTS.md#文档与产物)。
