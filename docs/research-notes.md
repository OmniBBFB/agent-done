# agent-done

当 agent 需要用户注意时，通过桌面状态栏等渠道提醒用户。

当前阶段：已完成真实桌面的串行交互验证，提供实验启动/采集脚本。正常 hook 信任、新建后连续两轮回复、退出后恢复、niri 窗口身份更新与关闭清理均通过；尚未部署日常启动命令和 Waybar 功能。见[正常交互验收记录](normal-interaction-2026-11-02.md)。

## 产品定位

通过 agent 的 hook 接收一轮生成结束、等待授权、等待用户补充信息等事件，维护待查看会话，并发送提醒。

首要原则是本机可用：初版针对 Codex + Alacritty + niri + Waybar。后续可扩展声音、本地邮件和其他 agent；初版优先完成 Waybar 通知。

## 已确认的初版需求

- 一轮生成结束时提醒。
- 需要用户确认或授权时提醒。
- 需要用户回答追加问题时提醒。
- 通过 Waybar 自定义模块接入，显示待查看的独立 Codex 会话个数。
- 通过 niri IPC 获取对应终端的显示器、工作区、行、列。
- 用户切回对应终端后，清除该会话的待查看标记。
- 同一会话的多条未查看事件不重复增加会话总数。

“待查看”描述通知是否已查看；清除通知不会替用户同意授权，也不会替用户回答问题。

## 工作区和位置

niri 的工作区是一张虚拟桌面。每个显示器有一组上下排列的工作区；每个工作区内部的窗口按列左右排列，同一列还可以上下放多个窗口。

位置示例：`HDMI-A-1 / 工作区 1 / 第 1 行 / 第 3 列`。

- 显示器：物理屏幕的输出名称。
- 工作区：该显示器上当前排序中的第几张虚拟桌面。
- 列：工作区中从左到右的第几列。
- 行：该列中从上到下的第几个窗口。

niri 返回的 `layout.pos_in_scrolling_layout` 顺序为 `[列, 行]`，从 1 开始。工作区序号、行列和显示器归属可能随窗口移动而改变，应动态查询；用窗口 ID 和工作区 ID 做内部关联。浮动窗口显示“浮动”，不伪造行列。

## 事件接入的调研结果

| 场景 | 可用入口 | 验证状态 |
| --- | --- | --- |
| 回合完成 | Codex `notify` 的 `agent-turn-complete`；也存在 `Stop` hook | 官方文档已确认，尚未端到端安装验证 |
| 等待工具权限确认 | `PermissionRequest` hook | 官方文档已确认；本机真实审批路径和自动审批情况仍需验证 |
| 自然语言提问后结束回合 | 回合完成通知 | 可覆盖“提问作为最终回复”的情况 |
| 回合中途的结构化提问 | App Server 存在 `item/tool/requestUserInput` 请求 | 尚未确认有直接对应的生命周期 hook；本机接入方式待验证 |

`notify` 单独不能覆盖所有人工介入场景。不能以已查到结构化提问协议，推断普通 hook 或独立旁路监听者一定能收到该请求。`PreToolUse` 文档也提示部分特殊工具路径可能绕过普通 hook，因此需要实测。

`Stop` 是允许其他 hook 请求继续生成的停止点；若用于通知，需要避免把中间停止点误报为最终完成。主会话和内部子 agent 的通知也需要过滤、去重。

## 会话与终端绑定

需要维护这样的关联：

```text
Codex 会话 ID → 显示该会话的终端窗口 ID → 当前显示器、工作区、行列
```

Codex 的 hook 提供会话身份；niri 提供窗口身份和位置；Waybar 读取维护好的待查看状态。完成事件发生时正在聚焦的窗口可能是浏览器或另一个终端，不能拿它当作事件来源。

前轮本机只读检查发现：Codex CLI 0.165.0 连接 Codex 自身的共享 App Server daemon 0.166.0。终端客户端有 `NIRI_SOCKET`、`ALACRITTY_SOCKET` 等变量，daemon 没有这些变量。因此不能默认通过后台 hook 的父进程或环境识别正确终端。

绑定方案已通过真实桌面串行交互测试：包装器记录真实 TUI PID/TTY，经本地转发器登记会话与 niri 窗口身份；使用正常信任流程的 SessionStart/Stop 采集到一致关联。恢复同一会话后，PID/窗口更新正确。早期 CLI 0.165.0 的命令行 hook 转发限制见方案记录；本次客户端更新到 0.166.0，仍采用转发器明确传递配置。备选 `--no-daemon` 未作为默认决定。

### TTY 与进程关系的本机验证

用户提出：按当前使用方式，每个终端的 Codex 只启动、退出，不在终端间迁移，可用 TTY 和进程关系维护绑定。这个约束能简化初版。

只读检查已确认当前界面进程的关系：

```text
/dev/pts/0 → Codex TUI PID 1463955 → Node → zsh → Alacritty PID 1454261
```

前轮 niri 查询中 Alacritty PID 1454261 对应窗口 201，因此 TTY 到终端窗口这一段已具备实测证据。这些数字是检查时的快照，不应硬编码。

尚缺的是 `Codex 会话 ID ↔ TUI PID`。本次检查发现：

- 当前 TUI 的启动参数没有会话 UUID，环境中没有 `CODEX_THREAD_ID` 或 `CODEX_SESSION_ID`。
- TUI 的标准输入输出连接其 TTY；共享 App Server 没有控制 TTY，统一打开多个会话的 rollout 文件。
- 抽查多个 TUI 进程对应的最近 400 条本机日志，以及带 thread 或会话遥测信息的日志，未找到直接的会话 ID 关联。该检查不证明所有日志或内部接口都没有此关联。

因此，TTY 可作为进程与窗口识别的骨架，但不能只凭进程列表宣称已经知道会话 ID。下一步验证在启动或会话创建时登记会话 ID 与 TUI/TTY 身份的方法，再由 agent-done 维护正反索引。daemon 里运行的 hook 不能靠直接执行 `tty` 获取源终端。

初版可按“不迁移”的使用约束维护映射；进程退出后清理，配合进程启动时间防止 PID/TTY 被复用。若以后支持在同一 TUI 中切换会话，再增加关联更新逻辑。

### Codex 运行时查询接口

已在本机执行 `codex doctor --json`：报告能给出 daemon 状态、版本、运行环境等诊断信息，但没有返回会话 ID 与 TUI PID 的对应表。独立启动的 doctor 进程也不会自动继承另一个 TUI 当前查看的会话。

对本机 daemon 0.166.0 的二进制执行 `app-server generate-json-schema --experimental`，核实以下协议字段；本轮未连接 daemon 执行这些 RPC：

| 接口 | 用途 | 与窗口绑定的关系 |
| --- | --- | --- |
| `thread/loaded/list` | 列出服务内已加载的 thread ID | 已加载不等于当前有终端正在显示该会话 |
| `thread/list`、`thread/read` | 返回 thread ID、sessionId、cwd、name、status 等信息 | Thread 对象未提供 TUI PID/TTY 字段 |
| `thread/status/changed` | 推送会话状态变化，文档包括等待授权状态 | 仍需关联终端身份 |
| `thread/backgroundTerminals/list` | 查询 agent 启动的后台命令；有 processId、可空 osPid | 这些进程不是用户运行 Codex 界面的终端进程 |

上述属于 App Server JSON-RPC，需要客户端连接并初始化，并非现成的 `codex sessions --json` 命令。交互式 `/statusline` 支持显示 session id，适合人工核对身份；尚未发现直接输出“当前会话 ID + 对应 TUI PID/TTY”的命令。

### 三种映射方案的对照

本机验证发现：文件描述符只能关联到共享 daemon；启动时间会出现多个候选或缺少候选。启动包装与服务端 SessionStart 的身份关联已通过实验，需要本地协议适配层。适配层本身也可直接从会话创建响应登记 ID，后续可用此方式减少动态 hook 的信任操作。详见 [方案评估与实测记录](session-pid-mapping.md)。

只读诊断脚本：[tools/probe_session_mapping.py](../tools/probe_session_mapping.py)。需要在能访问桌面进程的宿主机运行；输出时间候选，不代表绑定已确认。

## niri 跳转与快捷键（已调研，快捷键暂缓）

用户已明确暂时搁置跳转快捷键；下面仅保留接口与语法调研记录，初版不开发、安装快捷键。

niri 本机 CLI 支持按窗口 ID 聚焦：

```sh
niri msg action focus-window --id 201
```

窗口 ID 201 仅是本次调研的例子，运行时应从映射中取得当前目标 ID，无需按显示器、工作区和行列逐级导航。

可在现有 `binds` 内加入快捷键，以下示例已通过本机 `niri validate` 校验语法，但尚未写入用户配置或执行跳转：

```kdl
Mod+N { spawn "niri" "msg" "action" "focus-window" "--id" "201"; }
```

agent-done 的动态方案可以让 Waybar 点击和快捷键共用一个“选取待查看会话并聚焦对应窗口”的命令。该命令尚未实现；实际接入时不硬编码示例窗口 ID，并检查快捷键冲突。

## 建议的初版行为

- Waybar 文本：`Agent 待查看 3`。
- 悬浮提示列出会话名称、提醒原因和终端位置。
- 监听 niri 窗口焦点变化，切回绑定终端后清除该会话标记。
- 窗口移动后更新位置；窗口关闭后清理失效绑定。
- 后续声音、邮件复用事件和状态，不各自维护一套会话计数。

仍需明确的边界：事件发生时终端已经聚焦如何处理、同一个终端切换多个 Codex 会话如何识别当前会话。窗口焦点本身无法证明该窗口内所有会话都已被查看。已在真实交互中发现并过滤内部标题生成会话，不能将其完成事件计入用户待查看会话。

## 初版验收

1. 两个独立终端分别运行 Codex；完成、确认、提问三种场景都用真实事件验证。
2. 同一会话重复收到事件只计一次；两个会话待查看时显示 2。
3. 用户正在浏览器中时，事件仍关联到正确终端。
4. 切回其中一个终端，只清除对应会话；新的后续事件可以再次触发。
5. 移动终端后显示新位置；关闭终端后不遗留计数。
6. 正常确认和回答流程不受通知脚本影响；通知失败不阻塞 Codex。
7. 未验证的特殊提问或自动审批路径明确标注，不声称全覆盖。

## 调研来源

- [Codex 生命周期 hooks](https://learn.chatgpt.com/docs/hooks)
- [Codex notify](https://learn.chatgpt.com/docs/config-file/config-advanced#notifications)
- [Codex 命令与 --no-daemon](https://learn.chatgpt.com/docs/developer-commands)
- [Codex App Server 用户输入请求](https://learn.chatgpt.com/docs/app-server#toolrequestuserinput)
- [niri 工作区](https://niri-wm.github.io/niri/Workspaces.html)
- [niri 窗口布局字段](https://niri-wm.github.io/niri/niri_ipc/struct.WindowLayout.html)
- [Waybar 自定义模块](https://github.com/Alexays/Waybar/blob/master/man/waybar-custom.5.scd)
