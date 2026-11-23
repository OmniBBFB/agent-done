# macOS 桌面接入

在本机图形会话中构建。需要 Go 1.25 或更新版本、Xcode 命令行工具，以及已可运行的 Codex CLI。

```sh
mkdir -p dist
go build -o dist/agent-done ./cmd/agent-done
```

## 选择后端

| 后端 | 启动方式 | 条件 |
| --- | --- | --- |
| 原生 Accessibility（默认） | `./dist/agent-done serve` | 在系统设置的「隐私与安全性 → 辅助功能」授权运行服务的宿主 |
| yabai | `AGENT_DONE_DESKTOP=yabai ./dist/agent-done serve` | yabai 在 PATH 中且服务已运行，`yabai -m query --windows` 可返回结果 |

在一个终端前台运行服务，在其他终端启动 Codex。每个需要单独跳转的会话使用一个独立的系统终端窗口，在该窗口聚焦时启动。应用里有多个窗口时，服务用其中实际聚焦的窗口消歧。同一个系统窗口内的标签页和分屏共用窗口身份。

```sh
./dist/agent-done doctor
./dist/agent-done codex
./dist/agent-done status --json
./dist/agent-done jump
```

`doctor` 中的 `desktop_backend` 显示实际后端，`desktop_connected` 显示桌面查询是否可用。权限不足或 yabai 不可达时，`desktop_error` 会给出原因。Codex 会话仍可通过代理登记。首次使用时，可在 Codex 的 `/hooks` 界面审阅指向新二进制的 hook 命令。

yabai 根据窗口 ID 和所属进程定位，并可提供显示器和 Space 编号。原生 AX 通过公开接口保存窗口引用，服务重启后需用 `agent-done codex resume <会话 ID>` 重新关联窗口。切换后端时，先正常退出受跟踪的 Codex CLI，再停止原服务，使用新后端启动并恢复会话。跨 Space、全屏和最小化后的跳转取决于系统和窗口管理器的行为。服务观察到准确窗口聚焦后才清除提醒，审批和提问仍等待用户处理。 [Apple Accessibility API](https://developer.apple.com/documentation/applicationservices/carbon_accessibility/attributes) · [yabai 命令手册](https://github.com/asmvik/yabai/blob/master/doc/yabai.asciidoc)

若使用 skhd，可将快捷键绑定到构建后的二进制绝对路径加 `jump`。macOS 以前台 `serve` 或已有用户服务启动，`install` 用于 Linux 的 Waybar/systemd 安装。

## 复用手机配对

桌面后端和手机配对使用各自的配置。同一台已配对的工作站沿用原来的 `remote.json`、密钥和 `CODEX_HOME`，可以继续使用手机上的配对。远程配置默认位于 `~/.config/agent-done/remote.json`，也支持 `XDG_CONFIG_HOME`。如果手机配对的是另一台工作站，本机须按独立设备配置。 [远程配置与控制权限](remote-relay.md#配置工作站)
