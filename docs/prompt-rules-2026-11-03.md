# UserPromptSubmit 提示词规则与 :q 退出

2026-11-03，在现有 `agent-done codex` 入口交付整行 `:q` → `client_exit`。

## 执行过程

1. 稳定命令 hook `python -m agent_done hook` 从 stdin 读取 `UserPromptSubmit` 的 `prompt`。
2. `prompt_rules.py` 只精确匹配字符串 `:q`，返回 `decision: block` 和固定反馈 `agent-done:client_exit`。其他输入返回空对象，不写入状态库或日志。
3. 启动器观察 `hook/completed`，核对事件、command handler、blocked 状态及反馈文本；再结合本连接发出的 `turn/start` 响应确认回合归属。
4. 确认该回合结束且会话仍在当前终端显示后，转发结束通知，再通过本终端 PTY 输入原生 `/quit`。不向共享 daemon 或其他 TUI 发送终止信号。

PTY 还负责透传用户输入、终端响应、窗口大小变化，并在退出时恢复外层终端属性。动作对同一回合只执行一次。其他客户端发起的回合和隐藏会话的通知均不触发退出。

官方输入与阻断协议：[Hooks / UserPromptSubmit](https://learn.chatgpt.com/docs/hooks)。`client_exit` 是本项目实现的动作，非 Codex 原生 hook 返回字段。

## 验收结果

- 使用已安装入口，通过 `tools/acceptance.py` 打开独立 Alacritty 与真实 Codex CLI/daemon 0.166.0。
- 正常使用既有 hook 信任；没有绕过信任或通过 RPC 发起测试回合。
- 通过终端输入提交 `:q`；界面显示 `Blocked by hook` 及 `agent-done:client_exit`。
- 随后界面出现 `/quit`、`Shutting down...`、客户端断开提示，测试窗口正常关闭，退出码 0。
- 最终 `uv run python -m unittest discover -s tests -v`：33 项通过，包括精确匹配、服务不可用时阻断、回合归属、事件先于响应、重复通知、隐藏会话及真实嵌套 PTY 的原生命令和终端属性恢复。

结构化证据：`outputs/prompt-rules-2026-11-03/checks.json`。终端原始输出保留在私有临时目录 `/tmp/agent-done-prompt-exit-20261103/`，未加入项目。

## 使用与恢复

新启动的 `agent-done codex` 自动加载桥接。既有窗口先用 `/quit` 退出，再通过 `agent-done codex resume <ID>` 恢复。普通 `codex`、非交互式输入及启动器降级到普通 Codex 的情形不提供该动作。

此次修改前的备份位于 `~/.local/state/agent-done/backups/prompt-rules-20261103-103450/`，含修改文件及清单；本次新增文件包括提示词规则、PTY 桥接与对应测试。恢复旧入口代码后，需重新启动终端客户端。
