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

## 本机切换与回退

先用 `dist/agent-done` 跑隔离验收，再按 [安装步骤](../README.md#安装与启用) 更新日常入口。Python 和 Go 服务共享 SQLite 文件及 Unix socket 路径。切换时由一个服务进程持有该状态目录，避免用不同的运行目录同时写同一份数据库。

安装器会在输出的备份目录中保存既有 Waybar 配置、样式、systemd unit 和命令链接。回退时先停 Go 服务，在独立检出目录准备旧 Python 版，再根据安装器的 `manifest.json` 还原配置和链接。确认链接指向独立检出的旧入口后，重载 systemd 和 Waybar，启动旧服务并恢复对应 Codex 会话。已有 SQLite 待查看状态使用同一数据格式。恢复后按当前启动路径审阅相应的 Codex hook。
