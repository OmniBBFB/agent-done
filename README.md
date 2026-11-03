# agent-done

在 Waybar 显示需要查看的 Codex 会话个数，并在悬浮提示中显示对应终端的位置。初版针对本机 Linux + Codex CLI + Alacritty + niri + Waybar。

## 日常使用

```sh
agent-done codex
agent-done codex resume <会话ID>
agent-done status
agent-done jump
agent-done doctor
```

从 `agent-done codex` 启动的交互会话才会纳入跟踪。已经运行的普通 `codex` 窗口需要退出后用该入口启动或恢复。Codex 的交互参数照常传递，例如 `agent-done codex -C ~/app/my-project`。

首次启动时用 Codex 的 `/hooks` 界面审阅并信任 agent-done 的命令 hook：`<项目虚拟环境>/bin/python3 -m agent_done hook`。命令固定，恢复会话不需要每次重新信任。审阅时保留你已有 hook 的信任判断。

从该入口新启动或恢复的交互终端中，输入整行 `:q` 再回车即可退出。`UserPromptSubmit` hook 精确匹配后阻止提示词提交，启动器等该轮结束，再通过 PTY 执行 Codex 原生 `/quit`。包含 `:q` 的普通句子、带空格的 ` :q` 等均照常提交。规则入口是 `src/agent_done/prompt_rules.py`。

更新前已运行的窗口需先用 `/quit` 退出，再通过 `agent-done codex resume <会话ID>` 恢复，以加载新的终端控制桥接。普通 `codex` 入口和提醒不可用时的降级入口不提供 `:q` 退出。此动作退出当前客户端；Codex 共享 daemon 和其他窗口继续运行。

Waybar 显示 `Codex 0`、`Codex 1` 等。悬浮查看会话名称、提醒原因和 `显示器 · 工作区 · 第几行第几列`；浮动窗口单独标记。位置跟随 niri 的实时布局更新。

- 后台生成完成：计入待查看；失败也提醒，中断不计入完成。
- 等待工具授权：真正交给用户确认时提醒，自动审批不增加待处理项。
- 等待结构化回答或 MCP 补充输入：提醒待回答；普通文字追问由回合结束覆盖。
- 切回对应终端：清除当前显示会话的提醒，工具授权和问题仍等待你操作。
- 事件发生时终端已经聚焦：视作已查看，不增加计数。
- 同一会话的多个事件、多处打开只计一次；同一终端内其他隐藏会话不会因终端聚焦而被清除。
- 关闭终端或退出 Codex：清除失效绑定和计数。PID 同时核对进程启动标识，避免复用误认。

手动清除提醒：`agent-done ack`，或 `agent-done ack --session <ID>`。这些操作只改变通知的查看状态。

## 跳到已完成的会话

本机已配置 **Mod+N**（通常为 Super/Win+N）。按一次跳到最早完成、尚未查看的会话窗口；切回后原有焦点监听会清除该提醒，再按一次跳到下一个。没有可跳转的会话时不切换窗口。

对应命令是 `agent-done jump`。按完成时间排序，只选择仍在终端中显示的已完成会话；等待授权、等待回答、失败和已查看的会话不参与这个快捷键。已关闭的窗口和失效进程会跳过，同一终端内的隐藏会话保留提醒，避免跳到其他会话后误清除。

实现通过会话映射取得窗口 ID，再调用 `niri msg action focus-window --id ...`。跨显示器、工作区由 niri 处理，窗口行列不参与排序。跳转命令不会主动清除提醒，实际聚焦后才由监听器确认已查看；跳转失败时提醒保留。

本机 `~/.config/niri/config.kdl` 的已有 `binds` 中加入了下面的绑定，修改前已备份：

```kdl
Mod+N repeat=false hotkey-overlay-title="查看最早完成的 Codex 会话" {
    spawn "/home/csc/.local/bin/agent-done" "jump";
}
```

其他机器请替换命令路径。niri 保存配置后自动加载，无需重启桌面。绑定与加载机制见 [niri 官方文档](https://niri-wm.github.io/niri/Configuration:-Key-Bindings.html)和[配置说明](https://niri-wm.github.io/niri/Configuration:-Introduction.html)。

## 安装与启用

需要 Python 3.11+、uv，以及已经可用的 Codex、niri 和 Waybar。在项目目录运行：

```sh
uv sync
uv run agent-done install
systemctl --user daemon-reload
systemctl --user enable --now agent-done.service
pkill -USR2 -x waybar
```

确保 `~/.local/bin` 在 PATH 中；也可直接运行 `~/.local/bin/agent-done codex`。

安装器创建用户命令和 systemd 用户服务，在现有 Waybar 配置的 `modules-right` 加入 `custom/agent-done`，并添加样式。原有配置备份在 `~/.local/state/agent-done/backups/`。重复安装不会重复添加模块。Waybar JSONC 注释会在写回时规整，原文在备份中保留。安装器写配置，服务启用和 Waybar 重载由上述命令完成。

服务随图形会话启动，状态栏采用持续 JSON 输出：

```json
"custom/agent-done": {
  "exec": "~/.local/bin/agent-done waybar",
  "return-type": "json",
  "restart-interval": 2,
  "tooltip": true
}
```

停用：`systemctl --user disable --now agent-done.service`，从 Waybar 配置移除 `custom/agent-done` 和相应样式，再重载 Waybar。若安装后没有其他配置修改，也可从安装备份恢复原配置。

## 实现与数据

```text
真实 Codex TUI → 本地 Unix WebSocket 转发器 → Codex 原有共享 daemon
                       ↓ 会话 ID + 真实 TUI PID + niri 窗口 ID
稳定命令 hook → agent-done 用户服务 ← niri 窗口、布局、焦点事件
                       ↓ SQLite 状态 / Waybar JSON 订阅
```

包装器从真实进程祖先识别终端窗口，从会话创建、恢复响应登记会话身份。转发器为用户会话补齐 hook 配置，并观察真正的回合完成、授权和提问请求。它不会改写用户的授权决定或回答。内部标题生成和子 agent 不作为独立待查看会话。

`Stop` hook 只作为停止候选；以 `turn/completed` 确认结束，避免其他 Stop hook 请求继续生成时误报。`PermissionRequest` 也不直接算等待人工，须观察到客户端授权请求。结构化提问通过本地协议观察补齐普通 hook 的覆盖。

hook 仅发送事件类型、会话 ID、回合 ID 和来源，短超时、失败放行。状态服务不可用时，Codex 消息继续转发；启动时提醒服务不可用则继续启动普通 Codex，并显示提示。跟踪服务恢复后需要重新启动或恢复该会话来登记。

- 状态：`~/.local/state/agent-done/state.sqlite3`
- 轮转诊断日志：`~/.local/state/agent-done/agent-done.log`
- 本机控制 socket：`$XDG_RUNTIME_DIR/agent-done/control.sock`
- 目录权限 0700，socket 0600；不记录原始协议流、用户问题、工具参数或助手正文。会话标题和工作目录用于状态栏显示，会保存在状态库。

## 范围与维护

本机验证版本是 Codex CLI/daemon 0.166.0、niri 26.04、Waybar 0.15、Alacritty 0.16.1。Codex 实验协议更新后需重新验收。入口管理本机 daemon 连接，不接受 `--remote` 或 `--no-daemon`；暂不支持 tmux/screen 内多个窗格、远程终端或嵌入式 IDE 终端。声音和邮件留待后续。

工作区是一张虚拟桌面，可类比行列之外的 z 维度。niri 每个显示器的工作区上下排列；工作区内列从左到右、同列的行从上到下。工作区序号和行列会变化，内部关联使用窗口 ID。

```sh
uv run python -m unittest discover -s tests -v
```

测试覆盖会话去重、并发状态、恢复、焦点清除、隐藏会话、请求生命周期、内部线程过滤、配置合并、安装及 Waybar 输出。真实桌面验收工具是 `tools/acceptance.py`，通过实际 Alacritty 和 Codex TUI 输入操作；旧研究脚本保留在 `tools/`，不用于日常启动。

当前 33 项自动测试通过。完成、授权、结构化提问、恢复、并发和窗口生命周期的真实桌面验收见[正式版验收记录](docs/production-acceptance-2026-11-02.md)；新增跳转命令和真实窗口聚焦验收见[快捷键验收记录](docs/jump-2026-11-03.md)，`:q` 的拦截与正常退出见[提示词规则验收](docs/prompt-rules-2026-11-03.md)。退出时 Codex 原生提示可能包含临时连接地址，恢复时请使用 `agent-done codex resume <ID>`；恢复沿用会话已有权限设置。

调研背景见[原始记录](docs/research-notes.md)、[会话映射实验](docs/session-pid-mapping.md)和[前期正常交互验证](docs/normal-interaction-2026-11-02.md)。
