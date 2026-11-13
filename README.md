# agent-done

保留现有终端，用一个全局快捷键，直达下一个需要你介入的 CLI agent 会话。

面向已有终端与桌面工作流、并行运行多个 CLI agent 的用户，优先提供键盘驱动的待介入会话导航，通知与计数帮助用户决定何时切回。
当前版本针对本机 Linux + Codex CLI + niri + Waybar：状态栏显示待查看会话个数和终端位置，按 **Mod+N** 跳到最早待查看的会话，实际聚焦后清除提醒，再按进入下一个。

其他平台与 agent 的支持属于调研方向，尚未实现。架构、源码职责和开发验证方法见[开发说明](DEVELOPMENT.md)。

当前已验证 Kitty 和 Alacritty 的独立 OS 窗口。生产代码通过进程父链与 niri 窗口 PID 关联，
不要求终端名为 Alacritty。每个 agent 使用独立 OS 窗口；不要把不同 agent 放进同一 Kitty
窗口的标签页或分屏，当前提醒和跳转按 niri 窗口处理，不能区分这些内部区域。
Linux 的 niri/Waybar 通知集成不直接适用于 macOS。

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

Waybar 显示 `Codex 0`、`Codex 1` 等。悬浮查看会话名称、提醒原因和 `显示器 · 工作区 · 第几行第几列`；浮动窗口单独标记。位置跟随 niri 的实时布局更新。

- 后台生成完成：计入待查看；失败也提醒，中断不计入完成。
- 等待工具授权：真正交给用户确认时提醒，自动审批不增加待处理项。
- 等待结构化回答或 MCP 补充输入：提醒待回答；普通文字追问由回合结束覆盖。
- 切回对应终端：清除当前显示会话的提醒，工具授权和问题仍等待你操作。
- 事件发生时终端已经聚焦：视作已查看，不增加计数。
- 同一会话的多个事件、多处打开只计一次；同一终端内其他隐藏会话不会因终端聚焦而被清除。
- 关闭终端或退出 Codex：清除失效绑定和计数。PID 同时核对进程启动标识，避免复用误认。

手动清除提醒：`agent-done ack`，或 `agent-done ack --session <ID>`。这些操作只改变通知的查看状态。

## 跳到待介入的会话

建议将 **Mod+N**（通常为 Super/Win+N）绑定到跳转命令。按一次跳到最早需要介入、尚未查看的会话窗口；切回后原有焦点监听会清除该提醒，再按一次跳到下一个。没有可跳转的会话时不切换窗口。

对应命令是 `agent-done jump`。与状态栏计数使用同一个 `pending` 条件，覆盖生成完成、等待授权、等待回答和执行失败，按会话最近一次状态更新时间从早到晚选择。已查看的会话、已关闭的窗口和失效进程会跳过，同一终端内的隐藏会话保留提醒，避免跳到其他会话后误清除。查看授权或问题仅清除提醒，请求仍等待你实际操作。

实现通过会话映射取得窗口 ID，再调用 `niri msg action focus-window --id ...`。跨显示器、工作区由 niri 处理，窗口行列不参与排序。跳转命令不会主动清除提醒，实际聚焦后才由监听器确认已查看；跳转失败时提醒保留。

在 `~/.config/niri/config.kdl` 的已有 `binds` 中加入下面的绑定，并将命令路径替换为自己的安装路径：

```kdl
Mod+N repeat=false hotkey-overlay-title="查看最早待介入的 Codex 会话" {
    spawn "/home/<user>/.local/bin/agent-done" "jump";
}
```

niri 保存配置后自动加载，无需重启桌面。绑定与加载机制见 [niri 官方文档](https://niri-wm.github.io/niri/Configuration:-Key-Bindings.html)和[配置说明](https://niri-wm.github.io/niri/Configuration:-Introduction.html)。

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

测试覆盖会话去重、并发状态、恢复、焦点清除、隐藏会话、请求生命周期、内部线程过滤、配置合并、安装及 Waybar 输出。开发环境与桌面验收方法见[开发说明](DEVELOPMENT.md)。

退出时 Codex 原生提示可能包含临时连接地址，恢复时请使用 `agent-done codex resume <ID>`；恢复沿用会话已有权限设置。
