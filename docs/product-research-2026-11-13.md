# CLI agent 会话导航：产品调研与跨平台可行性

调研日期：2026-11-13。

agent-done 的产品方向是：**保留现有终端，用一个全局快捷键，直达下一个需要用户介入的 CLI agent 会话。** 重点是按会话状态选择目标、准确定位和连续查看。通知及状态栏帮助用户决定何时返回，键盘跳转承担主要操作。

平铺窗口管理器不是这个概念的前提。macOS、Linux 非平铺桌面和 Windows 都有窗口控制路径，但当前实现仅在本机 Codex + niri + Waybar、独立终端 OS 窗口的组合上有验收证据；其他平台属于有文档依据的可行性判断，尚未移植或实机验证。

本记录汇总本轮讨论，依据项目源码、已有验收记录和上游一手文档。不比较项目规模，不声称穷举市场，也不把上游的支持声明当作本项目的验收结果。[原始调研记录](research-notes.md)保留早期过程，其中的阶段描述应结合后续验收和当前 README 阅读。

## 目标用户与核心体验

目标用户是在多个 CLI agent 会话间并行工作、已有终端和桌面使用习惯、希望继续使用现有工具的人。这里的“面向 CLI 用户”描述用户的工作方式，不要求辅助工具只能显示命令行界面；窗口聚焦本身仍涉及图形桌面。

核心体验如下：

1. 用户在浏览器、编辑器或其他应用工作，多个 agent 在各自终端窗口运行。
2. agent 完成回合、等待授权、等待回答或执行失败，进入待查看状态。
3. 用户按一次全局快捷键，直接进入一个待介入会话，无需搜索标题、选择窗口或点击通知。
4. 确认目标实际聚焦后，清除它的待查看标记；授权和回答仍由用户操作。
5. 再按一次进入下一个；没有有效目标时保持原位。

这段体验已由本机的 `agent-done jump`、niri 焦点事件和待查看状态实现。当前按会话最近一次状态更新时间升序选择目标，覆盖所有 `pending` 状态；不是仅跳到成功完成的会话，也不是按屏幕位置排序。[使用说明](../README.md#跳到待介入的会话)、[导航源码](../src/agent_done/navigation.py)、[状态源码](../src/agent_done/state.py)、[跳转验收及范围修正](jump-2026-11-03.md)

## 直接比较的产品

本轮选取三个与 CLI agent 提醒或导航直接相关的项目：

| 产品 | 与现有终端的关系 | 公开说明中的主要体验 | 与 agent-done 的关系 |
| --- | --- | --- | --- |
| [cmux](https://cmux.com/) | 使用 cmux 作为终端应用，在其中运行 CLI agent | 终端内的工作区、分屏、通知、未读状态和键盘导航 | 功能重合较多；主要区别在于 agent-done 保留既有终端和桌面工作流 |
| [agent-notify](https://github.com/paultendo/agent-notify) | 在已有终端/编辑器工作流中接入 agent 通知 | 完成、授权等提醒，声音、通知点击后返回终端；部分组合支持标签/分屏定位 | 外置工具形态接近；agent-done 更强调全局快捷键访问待介入会话队列 |
| [Agent Notifications](https://github.com/777genius/agent-notifications) | 在已有 agent 和终端/编辑器工作流中接入 | 多 agent、多平台通知、声音、webhook，以及有条件的 click-to-focus | 同样是相关产品；公开定位侧重消息投递和通知返回，需进一步检查导航机制 |

cmux 明确面向终端内运行的 coding agent 用户，不能将其概括为“不适用 CLI 用户”。更准确的目标差异是：是否愿意更换终端应用。它已有 `⌘⇧U` 跳到最新未读、查看工作区后清除未读等行为，因此“键盘跳转”“查看后清除”不能作为 agent-done 的独家能力。[cmux 通知生命周期](https://cmux.com/docs/notifications)、[快捷键](https://cmux.com/docs/keyboard-shortcuts)

agent-notify 的说明列出了终端检测、macOS 下具体终端和 multiplexer 的焦点方法；Linux/Windows 还提供自定义激活命令。不能把“支持多个系统的通知”理解为“在所有系统都能精确回到任意会话”。[agent-notify 兼容说明](https://github.com/paultendo/agent-notify#compatibility)、[配置](https://github.com/paultendo/agent-notify#configuration)

Agent Notifications 将 agent、事件、系统、声音和 click-to-focus 的支持分别列出。例如 Codex 的返回能力取决于终端和系统，部分其他 agent 的窗口返回只针对特定 Linux/X11 路径。其项目支持表和对应指南应作为具体组合的依据。[支持表](https://github.com/777genius/agent-notifications#supported-agents)、[点击返回说明](https://github.com/777genius/agent-notifications/blob/main/docs/CLICK_TO_FOCUS.md)

本轮对两个通知项目的比较是对公开定位和已查阅功能的判断，尚未对全部源码、配置或第三方扩展进行功能穷举；本轮查阅的文档未确认统一键盘队列入口，不等于证明它们绝无此能力。

### 排除出直接竞品的工具

rofi、Raycast、Alfred、PowerToys 等通用窗口搜索/切换工具不进入本轮直接竞品比较。它们可帮助用户找到已知目标窗口，但本轮需要解决的是“哪个 agent 正在等我、哪些已经看过、下一次应去哪里”。这些状态仍需由 agent 集成和会话管理维护。

这项排除针对其作为通用窗口切换器的角色，不评价其插件生态，也不排除将其用作启动入口或窗口控制组件。

## 产品区别点

已确认的定位是：**面向希望保留现有终端与桌面工作流的 CLI agent 用户，提供按待介入状态驱动的全局键盘导航。**

区别来自以下能力的组合：

- 保留既有终端及窗口管理器的操作习惯，不要求将会话集中到一个新的终端应用。
- 用统一命令按待查看状态选择目标，键盘操作不依赖通知弹窗是否还存在。
- 从任意桌面应用触发，定位到显示该会话的有效窗口。
- 去重、清理失效窗口和进程，聚焦失败时保留提醒，以实际聚焦确认已查看。
- 已查看状态与待授权/待回答请求分开，导航不会代替用户作决定。

以上是产品定位与体验组合，不是对竞争者功能缺失或技术独占的结论。快捷键容易被补充，持续价值在准确的会话定位、可靠的状态生命周期和低接入成本。跨平台、多 agent、终端内部标签/分屏仍是扩展方向，不能当作当前能力宣传。[当前范围与实现](../README.md)、[窗口身份与焦点监听](../src/agent_done/desktop.py)、[请求和查看状态](../src/agent_done/state.py)

## 系统与桌面组合的可行性

概念所需的桌面能力是：识别目标窗口、观察焦点和窗口生命周期、按用户指令激活目标，以及提供键盘入口。平铺布局只影响位置的展示方式；行列坐标不是内部绑定依据。

下表“可行”表示存在有依据的接入路径。除第一行外，都没有本项目在该组合上的端到端验收。

| 系统/组合 | 窗口控制路径 | 本项目证据与主要边界 |
| --- | --- | --- |
| Linux + niri + Waybar + Kitty/Alacritty | niri IPC 查询窗口、订阅事件、按窗口 ID 聚焦；Waybar 展示待查看状态 | **本机已实现并有验收记录**。每个 agent 独立 OS 窗口；Kitty 验收工具使用隔离服务和测试提醒，不等于重新跑完所有真实模型场景。[源码](../src/agent_done/desktop.py)、[正式验收](production-acceptance-2026-11-02.md)、[跳转验收](jump-2026-11-03.md)、[Kitty 验收入口说明](../README.md) |
| Linux + 其他平铺 WM，例如 i3/Sway | 通过各自 IPC 获取布局/窗口、订阅窗口事件并执行聚焦 | **文档支持可行性，未适配**。需要逐个 WM 对接，不能直接复用 niri 命令或窗口 ID。[i3 IPC](https://i3wm.org/docs/ipc.html)、[Sway IPC 一手手册](https://github.com/swaywm/sway/blob/master/sway/sway-ipc.7.scd) |
| Linux + X11 非平铺桌面 | X11 窗口身份及 xdotool 等控制路径 | **文档支持可行性，未适配**。焦点策略、最小化和跨工作区行为需实测；xdotool 不是通用 Wayland 方案。[xdotool](https://github.com/jordansissel/xdotool) |
| Linux + KDE Plasma / Wayland | KWin 脚本；kdotool 提供窗口查询和 `windowactivate` | **文档支持可行性，未适配**。使用 KWin 内部窗口 ID，需要单独处理焦点回执和窗口生命周期。[kdotool](https://github.com/jinliu/kdotool) |
| Linux + GNOME / Wayland | 在 GNOME Shell 扩展内使用 Mutter 窗口 API，建立与外部服务的接口 | **依据 API 的方案推断，未适配**。需要 GNOME 专用扩展；存在激活 API 不等于任意外部进程都能调用，也不证明跨版本兼容。[Mutter 激活窗口 API](https://mutter.gnome.org/meta/method.Window.activate.html)、[带工作区的激活 API](https://mutter.gnome.org/meta/method.Window.activate_with_workspace.html) |
| macOS + yabai | 查询窗口、信号与窗口聚焦接口 | **文档支持可行性，未适配**。需核对具体 macOS/yabai 版本、权限和跨 Spaces/全屏/最小化行为；不能对所有操作统一断言是否需要 scripting addition。[yabai 命令](https://github.com/asmvik/yabai/wiki/Commands)、[scripting addition 相关能力](https://github.com/asmvik/yabai/wiki/Disabling-System-Integrity-Protection) |
| macOS + AeroSpace | `list-windows`、`focus --window-id` 以及事件/回调接口 | **文档支持可行性，未适配**。应使用 AeroSpace 的窗口与工作区模型，处理原生全屏、最小化等边界。[AeroSpace 命令](https://nikitabobko.github.io/AeroSpace/commands) |
| macOS 普通桌面 | Accessibility API 或 Hammerspoon 的窗口查询、过滤、聚焦与监听能力 | **文档支持可行性，未适配**。需要相应辅助功能权限；`hs.window.allWindows()` 有当前 Space 范围限制，跨 Spaces 和全屏窗口需专门验证。[Hammerspoon 窗口文档](https://www.hammerspoon.org/docs/hs.window.html)、[窗口过滤/事件](https://www.hammerspoon.org/docs/hs.window.filter.html) |
| Windows 原生桌面 | 以 HWND 标识窗口，用 Win32 聚焦、窗口事件 hook 和全局快捷键 | **文档支持可行性，未适配**。系统限制前台激活，用户按键触发也需检查实际焦点回执；虚拟桌面、最小化和不同权限进程需专项验收。[SetForegroundWindow](https://learn.microsoft.com/en-us/windows/win32/api/winuser/nf-winuser-setforegroundwindow)、[SetWinEventHook](https://learn.microsoft.com/en-us/windows/win32/api/winuser/nf-winuser-setwineventhook)、[RegisterHotKey](https://learn.microsoft.com/en-us/windows/win32/api/winuser/nf-winuser-registerhotkey) |
| Windows 宿主终端 + WSL 内的 CLI agent | 在 WSL 内采集会话状态，与宿主侧窗口身份和跳转命令桥接 | **架构推断，尚未验证映射路径**。WSL 内的 Linux PID 不能直接当作宿主窗口身份；若共用 Windows Terminal 标签/分屏，还需终端内部定位。Windows 聚焦能力见上一行，WSL 会话到宿主窗口的绑定是额外缺口。 |

Linux 的关键区别是显示协议与合成器接口，不是平铺与非平铺。Wayland 的窗口控制通常依赖具体合成器；不能将某个桌面的方案推广成所有 Wayland 桌面的统一支持。[kdotool 对窗口控制接口的说明](https://github.com/jinliu/kdotool#introduction)、[xdotool 的 Wayland 限制](https://github.com/jordansissel/xdotool#wayland)

这些路径只支持“可以设计适配”的结论。当前代码还依赖 `/proc`、systemd、Unix IPC 和 Waybar，移植不只是替换一个聚焦命令；需要同时验证 agent 事件来源、进程身份、服务生命周期、窗口控制和状态展示。[进程及窗口接口](../src/agent_done/desktop.py)、[服务](../src/agent_done/runtime.py)、[启动和协议观察](../src/agent_done/codex.py)、[安装](../src/agent_done/install.py)、[IPC](../src/agent_done/ipc.py)

Windows 原生版还需要替换或适配当前的 `fcntl` 文件锁、异步 Unix socket/WebSocket 接入和 systemd 用户服务管理；不能因为 Win32 能聚焦窗口，就认为现有 Python 服务与启动器可以直接运行。WSL 能承载 Linux 进程，也不会自动补齐到 Windows 宿主窗口的会话映射。这些都是完整移植的待验证项。[服务与锁](../src/agent_done/runtime.py)、[控制 IPC](../src/agent_done/ipc.py)、[Codex 转发与服务启动](../src/agent_done/codex.py)

## 终端、窗口管理器与 agent 的职责

cmux 嵌入 libghostty；后者提供终端模拟、字体和渲染核心。cmux 自己管理应用界面、工作区、终端区域及通知导航，也能接收 OSC 9/99/777 或 CLI/hook 通知。因此，“基于 Ghostty”说明终端核心的来源，不表示这些高层功能都是 Ghostty 直接提供的。[Ghostty 架构](https://ghostty.org/docs/about#libghostty)、[cmux 对 libghostty 的说明](https://cmux.com/#faq)、[cmux 通知](https://cmux.com/docs/notifications)

当前 agent-done 的分工不同：

| 职责 | 当前承担者 | 依据 |
| --- | --- | --- |
| 显示 Codex TUI、传递普通终端输入输出 | Kitty 或 Alacritty 等既有终端 | [启动包装器](../src/agent_done/codex.py) |
| 识别生成完成、等待授权、等待回答等状态 | Codex 协议观察与稳定 hook | [Observer 与 hook 接入](../src/agent_done/codex.py)、[状态逻辑](../src/agent_done/state.py) |
| 会话关联到终端 OS 窗口 | 启动时的进程父链、niri 窗口 PID，以及会话创建/恢复响应 | [窗口身份](../src/agent_done/desktop.py)、[会话登记](../src/agent_done/codex.py) |
| 激活目标窗口、报告真实焦点变化 | niri | [跳转](../src/agent_done/navigation.py)、[焦点订阅](../src/agent_done/desktop.py) |
| 去重、待查看状态和失效绑定管理 | agent-done 服务与状态库 | [状态](../src/agent_done/state.py)、[服务](../src/agent_done/runtime.py) |
| 展示计数和位置 | Waybar 模块 | [Waybar 输出](../src/agent_done/waybar.py) |

生产代码没有使用 `kitty @`、`KITTY_WINDOW_ID`、OSC 99 或 Kitty 图像协议。它依赖终端作为运行载体及普通进程关系，并非完全与终端无关；但不要求生产终端名为 Kitty。桌面验收工具中的 Kitty/Alacritty 专用启动参数用于创建测试窗口。[窗口识别源码](../src/agent_done/desktop.py)、[生产启动源码](../src/agent_done/codex.py)、[验收启动器](../tools/terminal_launcher.py)

OS 窗口与终端内部标签/分屏是两层身份。窗口管理器激活 Kitty 窗口，并不能自动选中其中某个分屏；因此当前约束是每个 agent 使用独立 OS 窗口。以后若支持标签/分屏，需要增加内部身份、选择操作和焦点/可见状态确认。Kitty 已有可供评估的远程控制接口，但 agent-done 尚未接入；cmux 则直接持有自己管理的工作区和 surface 身份。[当前范围](../README.md)、[Kitty 远程控制](https://sw.kovidgoyal.net/kitty/remote-control/)、[cmux ID 与控制接口](https://cmux.com/docs/api)

## 当前能力与证据边界

本机已有正式入口、待查看计数、全局快捷键配置、混合状态导航、聚焦确认、恢复与窗口生命周期清理。正式验收覆盖真实完成、授权、结构化问题和并发会话；跳转验收通过实际窗口动作验证计数及焦点，提醒事件由隔离服务注入，未合成物理按键。[正式验收](production-acceptance-2026-11-02.md)、[跳转验收](jump-2026-11-03.md)

“切回即已查看”是产品约定，不证明用户已经阅读或解决问题。焦点只清除通知的未读状态，不解除 agent 的授权或回答请求。后台 hook 也不能仅凭执行时的前台窗口判断事件属于哪个会话。[状态实现](../src/agent_done/state.py)、[现有映射与观察路径](../src/agent_done/codex.py)

本轮没有安装或验收三个相关产品，没有实现新的系统适配，也没有证明市场上不存在同定位产品。已确认的是一个有明确目标用户和体验边界的产品方向。

## 后续讨论

下一轮再检查其他项目中可借鉴和吸收的具体做法：

- 平台适配：窗口与终端身份、事件接入、跳转回执，以及支持矩阵如何表达。
- 仓库管理：目录组织、测试与桌面验收、CI、发布安装、兼容性记录和贡献流程。
- 产品体验：通知与导航的协作、键盘入口、待查看排序和多会话管理。

这些是后续研究题，不是已经选定的实现方案或平台支持承诺。
