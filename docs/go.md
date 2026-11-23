# 开发与迁移

Go 版在 Linux/niri 上提供同名命令，覆盖 Codex 启动和恢复、会话提醒、焦点跳转、Waybar、诊断和安装。旧 Python 运行时与实验性 macOS 适配可从 Git 提交 `10eb90f` 取回。

## 构建与验证

```sh
CGO_ENABLED=1 go build -o dist/agent-done ./cmd/agent-done
go test ./...
go test -race ./...
go vet ./...
```

Go 版使用 Gorilla WebSocket 连接 Codex 的 Unix socket，用 `go-sqlite3` 保留已有数据库格式。构建需要 C 编译器，驱动默认编译其自带的 SQLite。启动时通过原有迁移补齐早期 Linux 视图列。

桌面验收脚本可选用系统 Python 3，只使用标准库和 Go 服务的 IPC。以下命令用隔离状态打开临时终端，验证 niri 窗口身份和实际聚焦，结束时尝试恢复原焦点。

```sh
python3 tools/validate_desktop.py --backend niri --terminal kitty \
  --binary dist/agent-done --output outputs/go-rewrite/desktop-kitty.json
```

桌面验收用合成审批和提问验证状态与跳转。真实 Codex TUI 验收另用 `tools/acceptance.py`，可以设置 `AGENT_DONE_BINARY` 指向待验二进制。真实审批和提问需要实际产生相应事件。

## 进程与数据边界

- `codex` 启动器管理 TUI 子进程的生命周期。每条 Unix WebSocket 代理连接持有自己的 Observer 和请求关联。检测到绑定、回合或请求事件时，把摘要交给本机服务。
- `serve` 是 SQLite 的所有者。服务串行处理状态更新和快照发布，IPC 保持每行一个 JSON 消息。窗口身份和焦点来自 niri，PID 另核对 `/proc` 启动标识。
- `jump` 根据待查看状态和更新时间选择候选，服务再次校验窗口身份后请求 niri 聚焦。收到实际焦点观测时才清除提醒，审批或提问仍保持待处理。
- Waybar 订阅当前快照，连接断开后重试。hook 只提交身份字段并保留短超时。

## 独立 Codex app-server

`--private-server` 最初用于让按次加载 API 凭据和 provider 配置的 Codex 交互会话继续接受 Agent Done 跟踪。共享 Codex daemon 已在后台运行，使用它启动时的进程环境。需要让 Codex 后端继承本次启动的 shell 环境时，可以为该次交互单独启动 app-server。

| 使用场景 | 启动方式 |
| --- | --- |
| 日常交互，沿用工作站的共享 daemon 配置，包括普通 SSH 会话 | `agent-done codex` |
| 临时使用一组 API 凭据、自定义 provider 或 API 端点，并让该进程在交互结束时退出 | `agent-done codex --private-server` |

将 `--private-server` 放在 `codex` 后的第一个参数位置。需要让独立 app-server 应用 `-c` 覆盖项时，将它们紧接在该参数后，放在提示词和 `resume` 等子命令之前。API 凭据由调用方通过环境变量提供。恢复已有会话时仍需满足 Codex 对该会话 provider 的要求。

启动器为本次 TUI 创建独立的 app-server 子进程和临时 Unix socket，Agent Done 继续通过代理跟踪会话。TUI 结束时，启动器回收子进程并清理 socket。隔离范围是本次启动的进程和 socket，Codex 配置与历史仍由所用的 `CODEX_HOME` 决定。

App 的文本历史读取目前通过共享 daemon 获取。依赖 App 完整读取这类会话历史的工作流应使用共享模式。

## 本机切换与回退

先用 `dist/agent-done` 跑隔离验收，再按 [安装步骤](../README.md#安装与启用) 更新日常入口。Python 和 Go 服务共享 SQLite 文件及 Unix socket 路径。切换时由一个服务进程持有该状态目录，避免用不同的运行目录同时写同一份数据库。

安装器会在输出的备份目录中保存既有 Waybar 配置、样式、systemd unit 和命令链接。回退时先停 Go 服务，在独立检出目录准备旧 Python 版，再根据安装器的 `manifest.json` 还原配置和链接。确认链接指向独立检出的旧入口后，重载 systemd 和 Waybar，启动旧服务并恢复对应 Codex 会话。已有 SQLite 待查看状态使用同一数据格式。恢复后按当前启动路径审阅相应的 Codex hook。
