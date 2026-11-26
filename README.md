# agent-done

**并行跑多个 Codex CLI 会话时，用一个快捷键切回下一个需要你查看的终端。**

agent-done 保留你已有的终端工作流。Waybar 显示 `AGENT: 2 (LOCAL: 1, SSH: 1), RUNNING: 1, DONE: 0`。`AGENT` 汇总受监控的本地和 SSH 会话，`RUNNING` 和 `DONE` 只统计本地会话。待介入提醒包括回合完成、执行失败、待授权和待回答。将鼠标悬停在计数上可查看本地提醒和窗口位置。按下配置好的 **Mod+N**，可依次跳到有窗口的待介入会话。

## 安装与启用

macOS 请参阅 [macOS 安装与桌面接入](docs/macos.md)，可选原生 Accessibility 或 yabai。以下步骤用于 Linux。

需要 Linux 上可用的 niri、Waybar、systemd 用户服务和 Codex CLI。源码构建需要 Go 1.25 或更新版本及 C 编译器。安装器在 `~/.config/waybar/config.jsonc` 或 `~/.config/waybar/config` 查找已有配置。请在 niri 桌面会话的终端中运行。

### 1. 从源码安装

```sh
git clone https://github.com/OmniBBFB/agent-done.git
cd agent-done
CGO_ENABLED=1 go build -o dist/agent-done ./cmd/agent-done
./dist/agent-done install
systemctl --user daemon-reload
systemctl --user enable --now agent-done.service
pkill -USR2 -x waybar
```

重新构建后更新正在运行的服务时，执行 `systemctl --user restart agent-done.service`。

安装器将 `~/.local/bin/agent-done` 链接到构建出的二进制，写入 systemd 用户服务，并更新 Waybar 的配置和样式。请保留仓库和 `dist/agent-done`。安装输出的 `backup` 字段指向原配置的备份目录，其中保留了 Waybar 配置原文。下文假设 `~/.local/bin` 已加入 `PATH`，也可以直接使用 `~/.local/bin/agent-done`。

### 2. 绑定跳转快捷键

将下面的配置加入 niri 配置文件的现有 `binds` 块，把示例路径换成 `~/.local/bin/agent-done` 对应的绝对路径。如已占用 **Mod+N**，可选择其他按键。 [niri 快捷键配置](https://niri-wm.github.io/niri/Configuration:-Key-Bindings.html)

```kdl
Mod+N repeat=false hotkey-overlay-title="查看待介入的 Codex 会话" {
    spawn "/home/<user>/.local/bin/agent-done" "jump";
}
```

### 3. 启动 Codex 并确认提醒

```sh
agent-done doctor
agent-done codex
```

在 `doctor` 的输出中确认 `service_available` 和 `niri_connected` 均为 `true`。初次使用时，在 Codex 的 `/hooks` 界面审阅指向本项目 `dist/agent-done hook` 的命令。启动一个回合，在它结束前切到另一个窗口。Waybar 计数应增加一项。按 **Mod+N** 切回，确认该会话的提醒消失。也可以执行 `agent-done jump` 直接验证跳转。

## 日常使用

| 操作 | 命令 |
| --- | --- |
| 启动 Codex 并指定项目目录 | `agent-done codex -C ~/app/my-project` |
| 恢复已有会话 | `agent-done codex resume <会话 ID>` |
| 查看会话状态 | `agent-done status` |
| 跳到最早待查看的会话 | `agent-done jump` |
| 手动清除提醒 | `agent-done ack` 或 `agent-done ack --session <会话 ID>` |
| 检查服务及桌面后端连接 | `agent-done doctor` |

通过 `agent-done codex` 启动或恢复的会话才会登记，SSH 登录后也可以使用这个入口。已有的普通 Codex 会话可以先退出，再用上表命令恢复。有关联窗口的会话在窗口聚焦时自动清除提醒。无关联窗口的会话仍计入提醒，可以用 `agent-done ack --session <会话 ID>` 确认。

如果启动时提示提醒不可用，Codex 会照常启动。可用 `journalctl --user -u agent-done.service` 查看服务日志，修复后用包装命令重新启动或恢复该会话。

停用时，从 Waybar 配置和样式中移除 `custom/agent-done` 及对应样式，重载 Waybar，然后执行 `systemctl --user disable --now agent-done.service`。之后可以用原来的 `codex` 命令启动会话。需要从备份恢复配置时，参阅[回退说明](docs/go.md#本机切换与回退)。

## 支持范围

- Linux 支持 niri 和 Waybar，已验证 Kitty 与 Alacritty。macOS 可通过原生 Accessibility 或 yabai 关联窗口和跳转。每个需要独立跳转的会话请使用独立的系统终端窗口。
- 启动器默认使用运行 Codex 的主机上的共享 daemon。包装入口拒绝 `--remote` 和 `--no-daemon`。没有关联窗口时，会话仍可跟踪，窗口定位和跳转不可用。 [SSH 会话与断线行为](docs/remote-relay.md#ssh-会话) · [独立 app-server 的适用范围](docs/go.md#独立-codex-app-server)
- 仓库提供 iOS 17+ 的原生客户端和自托管中转。iPhone 可在演示模式试用界面。配对在线工作站后，可以查看受跟踪的会话和文本历史。获得控制权限的手机还可处理支持的审批与提问，并在支持继续会话的工作站上给空闲会话发送补充指令。 [iOS 客户端](docs/ios.md) · [工作站与中转](docs/remote-relay.md#配置工作站)

桌面服务把提醒状态、会话标题和工作目录保存在本机 SQLite，默认位于 `~/.local/state/agent-done/state.sqlite3`。

## 文档、反馈与贡献

- [开发说明](docs/development.md)涵盖代码入口和状态维护。
- [Go 构建、验收与回退](docs/go.md)涵盖测试、真实桌面验证和配置还原。
- [中转传输协议](protocol/relay-v1.md)和[端到端控制协议](protocol/control-v1.md)涵盖部署和互操作。

反馈问题时，请附上 Codex、niri 和 Waybar 版本、复现步骤，以及 `agent-done doctor` 中相关的诊断信息。 [GitHub Issues](https://github.com/OmniBBFB/agent-done/issues)

提交代码前，在仓库根目录运行 `go test ./...`。涉及窗口识别、焦点或跳转的改动，可按[桌面验收步骤](docs/go.md#构建与验证)验证。

## 许可证

本项目采用 [MIT 许可证](LICENSE)。第三方依赖适用各自的许可证。
