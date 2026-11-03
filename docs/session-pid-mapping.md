# Codex 会话与终端进程映射方案评估

评估日期：2026-10-30。范围：本机 Codex CLI 0.165.0 + 共享 App Server daemon 0.166.0。用户要求暂缓跳转快捷键，当前聚焦会话身份关联。

后续状态：2026-11-02 已在 CLI/daemon 均为 0.166.0 的真实桌面环境通过正常信任、三轮回复、新建与恢复、窗口身份关联及退出清理。详见[正常交互验收](normal-interaction-2026-11-02.md)。下面保留早期方案对照与有条件实验的原始范围。

## 结论

方案 1 的加强版本已完成有条件验证：在终端启动 Codex 时捕获进程身份，通过一个本地协议适配层向共享 daemon 的会话请求补入 hook 参数，SessionStart 能记录正确的会话 ID 和 TUI PID/TTY。但当前 CLI 不转发 `-c hooks.SessionStart=...`，仅靠 shell 包装追加参数不够。窗口身份采集、正常信任流程及恢复会话等仍待实现。方案 2 可确定会话文件的持有服务进程，但本机无法由此确定界面进程。方案 3 适合作为候选发现和辅助诊断。

这里的目标是 **显示会话的 TUI PID/终端窗口**。持有 rollout 的 daemon PID、hook 脚本自己的 PID、agent 执行工具命令的 PID，都不能替代这个身份。

## 方案 1：启动 hook 登记

官方 SessionStart 输入包含 `session_id`、`transcript_path`、`cwd`、`source` 等字段，没有文档化的 TUI PID/TTY 字段。直接调用 `os.getpid()` 得到的是 hook 脚本自己的 PID；父进程也不能默认就是 TUI。此前本机检查发现共享 daemon 没有终端的 `NIRI_SOCKET` 和 `ALACRITTY_SOCKET`，因此不能仅依赖继承环境。

建议验证以下组合：

1. 包装启动命令，在终端侧解析当前终端窗口 ID、启动锚点 PID 与启动时间。
2. 将身份固化为该次启动 hook 命令的参数，避免只通过环境变量传给早已运行的共享 daemon。
3. SessionStart 从标准输入获取会话 ID，与命令参数携带的终端身份一起登记。
4. 运行期间使用该登记关系，进程退出后清理；记录启动时间防止 PID 复用。

若目的只是定位和焦点清除，直接关联 `会话 ID ↔ niri 窗口 ID` 已能满足核心需求；TUI PID/TTY 可以作为进程生命周期校验信息。

`SessionStart` 是会话生命周期事件，不等于每次都会新建 OS 进程。该方案的实验结果见下节；正常信任流程、resume/clear/compact/fork 的事件行为尚未验证。

### 加强方案验证结果

使用项目内临时采集脚本和 PTY 终端实例，明确连接现有共享 daemon；没有安装全局 hook、设置 Codex 别名或重启 daemon。测试会话使用临时本机 URL，采集 hook 返回 `continue: false` 停止回合，没有真实模型生成。

关键结果：

1. 启动包装器使用 `exec` 启动原生 Codex 二进制，因此包装器记录的 PID 就是该测试的 TUI PID。若通过 npm 的 Node 启动器启动，Node 和原生 TUI 可能是不同 PID，产品实现需解析真实前端 PID 或保留启动锚点关系。
2. CLI 能识别命令行 hook 并显示信任提示。但显式连接 daemon 后，实际 `thread/start.config` 只包含 `shell_environment_policy`、`bypass_hook_trust`，没有 `hooks.SessionStart`。因此不能把客户端识别配置等同于服务端收到配置。
3. 把 hook 显式加入 App Server `thread/start.config` 后，`hooks/list` 能看到它，daemon 能执行它。
4. `shell_environment_policy.set` 的测试变量没有出现在 hook 环境，不能用这项设置直接传递终端身份。

最后一次真实 TUI 对照：

| 字段 | 实测值 |
| --- | --- |
| TUI PID | 1512822 |
| TTY | `/dev/pts/7` |
| TUI 创建请求返回的会话 ID | `01a18c46-e58f-7020-abb3-f518d280135d` |
| hook 输入中的 session_id | 与上述 ID 完全相同 |
| hook 自身 PID | 1513016 |
| hook 父进程 PID | 1066499，即现有 daemon |

实验代码在 TUI 与现有 daemon 之间运行本地 Unix WebSocket 转发器，仅为测试会话补入 hook 配置。首轮测试由控制连接调用真实 `turn/start` 触发；没有验证在该临时本机服务配置下手工输入消息的完整 UI 流程。测试 TUI 启动、会话创建、服务端 hook 执行和身份对应均为真实路径。

测试使用官方单次 `--dangerously-bypass-hook-trust` 放行本次已审阅脚本，不修改永久信任记录；这不是产品默认策略。动态改变命令参数会改变 hook hash，仍需设计正常信任方式。

既然协议适配层已经能看到 `thread/start` 返回的 ID，产品也可以直接在这里登记 `会话 ID ↔ TUI/窗口身份`，省去用于登记的动态 SessionStart hook。生成结束、等待确认等通知仍通过稳定配置的 hook 接入。这是后续实现建议，本轮未实现生产启动器。

实验产物：

- `tools/validate_launch_hook.py`：PTY/TUI 与协议边界对照，属于实验工具。
- `tools/validate_session_hook_rpc.py`：直接验证 daemon 的每会话 hook 配置，并归档测试会话。
- `tools/_probe_rpc.py`：本机测试所用的最小 WebSocket JSON-RPC 客户端，不作为生产通用客户端。
- 成功结果：`/tmp/agent-done-hook-k3ll266b/result.json`。
- daemon 直接验证：`/tmp/agent-done-rpc-hook-l1o82ybs/result.json`。

已归档本轮产生的服务端测试会话；临时 TUI 已退出。无 hook 记录的前置实验不算验证成功，其中一次采集实际发生在本地运行模式，已排除为共享 daemon 的证据。尚未测试多个 TUI 同时启动的隔离性、恢复旧会话或桌面窗口 ID 的端到端绑定。

## 方案 2：文件描述符 / lsof

只读扫描 `/proc/<pid>/fd`，等价地检查 lsof 所展示的文件持有关系。本机结果：

- 7 个连接 `/dev/pts/*` 的 Codex TUI 进程，均未打开 rollout 文件。
- 共享 App Server PID 1066499 无控制 TTY，同时打开 11 个不同会话的 rollout 文件。

所以当前得到的是 `多个会话 ID → 同一个 daemon PID`，无法继续唯一关联到各个 TUI/终端。更换成 lsof 查询不会改变文件的实际归属。

在单进程自行持有会话文件的运行方式下，这条路径可能适用；本轮未以 `--no-daemon` 做端到端验证，也未改变现有运行模式。

## 方案 3：进程启动时间关联

阅读了用户提供的 [agentop discovery.rs](https://docs.rs/agentop/latest/src/agentop/telemetry/codex/discovery.rs.html)。它的实际代码路径：

- 为目标 PID 解析 CODEX_HOME。
- 根据进程启动日期扫描当天、前一天、后一天的目录。
- 读取 rollout 首行 `session_meta.payload.timestamp` 的 UTC 时间，与进程启动时间比较，容差为正负 90 秒。
- 多个候选中选择文件修改时间最新者；缓存停止更新若干轮后重新查找。

文件开头的说明仍提到文件名时间与当天/昨天，和后面的实际函数有差异；上述评估以实际函数为准。探测脚本复现时间候选筛选和 mtime 排序，不复制完整 agentop 或它的缓存策略。

本机结果如下，数字仅为采样时快照：

| TUI PID | TTY | 时间窗口内候选数 | 会话创建时间减进程启动时间 |
| --- | --- | --- | --- |
| 681285 | pts/14 | 1 | +2.68 秒 |
| 1290609 | pts/12 | 0 | 无 |
| 1435382 | pts/8 | 1 | +2.54 秒 |
| 1441917 | pts/16 | 1 | +3.15 秒 |
| 1463955 | pts/0 | 1 | +2.21 秒 |
| 1469331 | pts/17 | 2 | +3.08 秒、+39.94 秒 |
| 3971516 | pts/3 | 0 | 无 |

PID 1469331 的两个候选连 cwd 都相同，因此仅增加目录匹配也不能排除歧义。两个无候选的 PID 在此前进程参数检查中均含 `resume`；这与“恢复旧会话时，原始创建时间早于当前进程启动”这一局限一致，但不是单凭时间就能识别其正确会话的证据。

其他失配原因包括启动后长时间停在选择器/登录页才创建会话。同一会话不迁移到其他终端，也不意味着旧会话的创建时间与每次重新启动进程的时间一致。mtime 反映谁最近写入文件，不能证明哪个进程显示哪个会话。

初版如保留时间候选功能，应明确标记“候选/未确认”；歧义时不自动选择最新文件并清除对应终端提醒，以免串错会话。

## 复现

在宿主机运行：

```sh
python3 tools/probe_session_mapping.py
```

脚本只读 `/proc` 和 rollout 的 session_meta 首行，输出身份与时间字段，不输出完整环境、启动提示词或对话正文。沙箱内运行只能看到沙箱进程，不代表宿主机没有 Codex TUI。

## 来源

- [Codex hooks：输入字段与 SessionStart 事件](https://learn.chatgpt.com/docs/hooks)
- [用户提供的 agentop 源码](https://docs.rs/agentop/latest/src/agentop/telemetry/codex/discovery.rs.html)
- 本机 `/proc` 只读扫描和启动时间候选实测；未安装 hook，未修改 Codex/niri/Waybar 用户配置。
