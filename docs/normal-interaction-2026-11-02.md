# 正常桌面交互验收：2026-11-02

结论：会话身份绑定与 SessionStart/Stop hook 的正常交互流程通过。真实 Alacritty 窗口中完成两轮回复，正常退出后恢复同一会话，再完成一轮回复。测试没有通过控制 API 发起回合，没有使用 hook 信任跳过开关，没有修改模型路由。

本次通过结果对应 CLI 0.166.0、共享 daemon 0.166.0、Alacritty 0.16.1、niri 26.04。开始检查时 CLI 仍为 0.165.0，随后安装路径被更新；初次遇到短暂的 `ETXTBSY`，最终启动的是正常安装路径下的 0.166.0。

## 测试方式

运行 `tools/normal_interaction_probe.py`，打开真实 Alacritty 测试窗口，Codex TUI 连接本地转发器，再连接现有 daemon。自动化通过终端 PTY 输入测试文本与回车，实际路径为：

```text
Alacritty 窗口 → 普通终端输入 → Codex TUI → 本地转发器 → 现有 daemon → 正常模型回复
```

测试消息只要求回复固定标记，不调用工具或读取文件。测试使用真实模型生成，正常回复均从服务端的主会话完成事件和 assistant item 核对，没有将屏幕中的用户输入回显当成回复。

转发器只补入测试 hook 配置、读取创建/恢复响应以建立映射；不注入 `turn/start`，也不更改模型端点。控制 RPC 仅用于结束后的测试会话归档。

两个 hook 使用稳定命令：

```sh
/usr/bin/python3 /home/csc/app/agent-done/tools/normal_interaction_probe.py --hook
```

SessionStart 与 Stop 都从输入取得 session_id，再查映射记录进程和窗口身份；返回 `{}`，不阻止正常生成。

## 正常信任流程

第一轮直接交互能得到真实回复，但 hook 未执行。通过 `/hooks` 打开正常界面，确认仅有本次定义的两个测试 hook 待审核，再执行界面提供的 `t trust all`。界面显示 SessionStart 已激活，随后真实采集到了 SessionStart 和 Stop。

正常信任记录会保留。之后完整验收及恢复会话继续使用同一稳定命令，未使用 `--dangerously-bypass-hook-trust`，无需每次因 PID 改变而重新审核命令。

这也说明：服务端能列出 hook 不等于 hook 已获信任或已执行，验收必须检查真实事件记录。

## 通过结果

会话 ID：`01a19a1f-e2e8-74a2-9d54-36af3cfc8512`。

| 场景 | TUI PID | TTY | niri 窗口 | 位置 | 真实回复 |
| --- | --- | --- | --- | --- | --- |
| 新建，连续两轮 | 3484679 | `/dev/pts/13` | 210 | HDMI-A-1 / 工作区 1 / 第 1 行第 5 列 | AGENT_DONE_NORMAL_OK、AGENT_DONE_SECOND_OK |
| 退出后恢复 | 3484953 | `/dev/pts/13` | 211 | HDMI-A-1 / 工作区 1 / 第 1 行第 5 列 | AGENT_DONE_RESUME_OK |

检查结果：

- 会话 ID 在恢复后保持不变，TUI PID 与窗口 ID 正确更新。
- TTY 被复用为同一个 `/dev/pts/13`；结合 PID 和进程启动时间后，身份没有串到旧窗口。
- 新建时收到 `SessionStart(source=startup)`，恢复时收到 `SessionStart(source=resume)`。
- 三个正常完成回合各收到一次 Stop。
- hook 父进程均为现有 daemon PID 1066499，hook 记录的会话、TUI PID 和实时 niri 窗口位置相符。
- 两次 `/quit` 后绑定均变为 inactive，niri 中对应测试窗口消失。
- 原始结果的 12 项复核全部通过。

## 后台标题会话的过滤

正常 TUI 还会为标题生成创建会话，请求具有：

```json
{"ephemeral": true, "threadSource": "thread_title"}
```

它也产生回合完成事件。前置测试曾把这条异步完成事件当成用户第二轮完成，导致错误判定。已修正实验工具：绑定时排除临时会话，等待完成时严格匹配主会话 ID。最终结果中，后台标题生成没有替换主会话的窗口绑定，也没有算作用户待查看任务。

生产实现应使用明确来源与主会话身份区分内部任务，不能只看到任意 `turn/completed` 就增加计数。除本次标题任务外，其他临时会话类别尚未验收。

## 产物与清理

- 验证工具：`tools/normal_interaction_probe.py`。
- 持久结果：`outputs/normal-interaction-2026-11-02/summary.json`、`result.json`。
- 正常信任界面文本：`outputs/normal-interaction-2026-11-02/hook-trust-ui.txt`。
- 原始成功运行：`/tmp/agent-done-normal-cycemtuy/`。
- 测试窗口均已关闭，测试进程已退出，三个前后测试的持久主会话均在状态库中确认为 archived。
- 没有安装全局 hook、Codex 命令别名、Waybar 模块或快捷键；本轮正常审核产生的两个测试 hook 信任记录保留。

## 验收边界

本次通过的是串行新建/回复/退出/恢复流程，以及真实 niri 窗口位置关联。尚未验证多个用户终端并发、异常退出或窗口移动；权限确认和结构化追问通知、Waybar 待查看计数与焦点清除尚未实现。本轮不等于整个 agent-done 初版交付完成。
