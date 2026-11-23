# iOS 客户端开发

`ios/AgentDone.xcodeproj` 是 SwiftUI 原生工程，部署目标为 iOS 17。初次启动进入演示模式，可以试用待处理列表、分页历史、审批和回答。在「设置」可以重置演示数据或切换到远程工作站。

首页「待处理」直接展开第一条请求，可查看执行位置、命令和理由，也可打开对应会话。「工作台」按工作站分组，可筛选进行中和已完成的会话。「会话」显示已打开的任务、聊天记录和补充指令。工作台会标记离线时仍保留的上次同步会话。切换会话时，未发送的草稿在本次运行内保留。

Codex 回复按 Markdown 显示标题、强调、列表、引用、分隔线、表格、网页链接和代码块。长代码块可横向滑动，点「复制」可复制原始代码。图片保留替代文字，用户发送的消息按原文显示。可在演示模式打开「工作台 → 更新开发文档」查看示例。

在「待处理」或「工作台」的右上角打开设置。演示和远程模式共用冷白底、深青色的工作台样式，并跟随 iOS 的明暗外观、动态字体和增强对比度设置。

## 构建与真机调试

用 Xcode 打开 [AgentDone.xcodeproj](../ios/AgentDone.xcodeproj)，选择 `AgentDone` scheme。真机调试时，在 `Signing & Capabilities` 选择自己的开发团队，切换目标到已连接的 iPhone，然后运行。默认 Bundle ID 为 `dev.agentdone.ios`，调试构建可通过 `PRODUCT_BUNDLE_IDENTIFIER` 指定独立标识。原地更新已配对的 iPhone 时，使用该手机上已配对 App 的 Bundle ID 和原开发团队，以继续读取其钥匙串配对。

真机需要开启开发者模式。如果首次打开时提示开发者不受信任，请在 iPhone 的「设置 → 通用 → VPN 与设备管理」中选择对应的开发者，按系统提示确认后再打开 App。 [Apple 开发者模式说明](https://developer.apple.com/documentation/xcode/enabling-developer-mode-on-a-device)

命令行可以在模拟器 SDK 上进行无签名构建：

```sh
xcodebuild -project ios/AgentDone.xcodeproj -scheme AgentDone \
  -destination 'generic/platform=iOS Simulator' \
  -derivedDataPath outputs/ios-simulator CODE_SIGNING_ALLOWED=NO build
```

## 配对远程工作站

1. 确认中转已登记手机与工作站，为各自发放独立令牌，并在两边的路由列表中加入对方。公开中转地址默认为 `wss://ad.omnibbfb.top/v1/connect`。 [中转传输 v1](../protocol/relay-v1.md)
2. 在 iPhone 打开「设置 → 配对工作站」，将显示的手机公钥通过受信渠道交给工作站，在工作站上核对并添加该设备。
3. 填写手机设备 ID、手机中转令牌、工作站设备 ID 和工作站公钥，选择「保存并连接」。App 支持保存一台工作站。设备私钥和中转令牌保存在本机钥匙串。

前台 App 通过 WSS 和 CryptoKit HPKE Auth 读取会话、分页历史和待处理卡片。工作站离线和中转异常时会显示连接状态。收到已验证的状态通知或重连后会重新同步。审批可选择仅允许一次或拒绝。工作站没有提供足够明细时，App 只提供拒绝。结构化问题按问题 ID 提交所选选项或允许的文本。

提交意图和幂等键保存在钥匙串。只在收到通过端到端认证的工作站答复后显示提交结果。转发失败或结果未知时，客户端会先查询工作站，用户明确重试同一回复且工作站确认仍待处理时才沿用原幂等键发送。 [控制协议 v1](../protocol/control-v1.md)

Go 工作站启用对应手机的控制权限后，App 可以处理支持的审批和提问。升级前已经打开的 Codex 会话需要通过新版 `agent-done codex resume` 恢复，才能通过原代理连接处理控制请求。 [工作站配置](remote-relay.md#配置工作站)

支持继续会话的工作站会在会话详情显示「补充指令」输入框，空闲时可以发送。演示模式可以进入「工作台 → 更新开发文档」试用。 [继续会话扩展](../protocol/control-v1.md#继续会话扩展)

获得控制权限后，在「工作台」的工作站旁点「新建会话」，即可打开位于工作站主目录（`~`）的空会话，并从「补充指令」发送第一条消息。会话由 Agent Done 服务管理，手机切到后台后仍可继续。

会话详情里的「自动审批」默认关闭。确认启用后，当前会话后续可自动处理的命令审批会收到一次性批准，已有待处理卡片仍需手动处理。随时可在同一位置关闭。 [会话管理扩展](../protocol/control-v1.md#会话管理扩展)
