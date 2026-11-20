# 端到端控制协议 v1 候选

本文定义工作站与 iPhone 的首轮联调格式。客户端可据此实现
消息模型、界面和测试。上线之前仍需验证 Swift 与 Go 互操作，
以及工作站通过原 Codex 连接回包。服务器按 [中转传输 v1](relay-v1.md)
转发密文，协议由端点解释。

## 连接与设备配置

公开中转入口为 `wss://ad.omnibbfb.top/v1/connect`，子协议为
`agent-done.relay.v1`。每台设备独立持有中转接入令牌。
端点配置包含本机设备 ID、本机密钥和每台允许通信的对端设备 ID
及公钥。首轮联调用受信渠道交换公钥，双方核对后各自添加授权。
公钥采用 32 字节 X25519 原始表示。JSON 交换时使用标准带填充 Base64。
中转令牌用于接入中转，工作站的本地配对记录负责会话授权。

### 密文信封

候选采用 RFC 9180 HPKE Auth 模式 `0x02`，算法为
DHKEM(X25519, HKDF-SHA256) `0x0020`、HKDF-SHA256 `0x0001`、
ChaCha20Poly1305 `0x0003`。CryptoKit 对应
`HPKE.Ciphersuite.Curve25519_SHA256_ChachaPoly`。
每个消息创建一个新的 Auth 发送上下文，并仅调用一次 `seal`。
接收端使用自己的私钥以及配对记录里发送方的公钥初始化 Auth 接收上下文，
并仅调用一次 `open`。

- `info` 是 `agent-done/control/v1` 的 UTF-8 字节。
- AAD 为 `agent-done/control/v1\n{from}\n{to}` 的 UTF-8 字节。这里
  `\n` 是单个 LF，末尾没有 LF。发送端使用本机 ID 和目标设备 ID，
  接收端用外层 `from` 找到已配对的发送方，并使用本机 ID 作为 `to`。
- 明文是下面定义的 UTF-8 JSON 对象。发送方把 32 字节 HPKE
  encapsulated key 直接拼到带认证标签的密文前面。将拼接结果整体编码成
  标准带填充 Base64，放入中转的 `payload`。
- 按外层大小上限分页。内层 JSON 编码后的字节数上限为
  `256 * 1024 - 32 - 16`。收到未知发送方、解密或认证失败的消息时丢弃。
  中转状态始终只用于连接提示。

[HPKE 标准](https://www.rfc-editor.org/rfc/rfc9180.html) ·
[CryptoKit Auth sender](https://developer.apple.com/documentation/cryptokit/hpke/sender/init%28recipientkey%3Aciphersuite%3Ainfo%3Aauthenticatedby%3A%29) ·
[CryptoKit Auth recipient](https://developer.apple.com/documentation/cryptokit/hpke/recipient)

## 明文消息格式

| 类型 | 必需字段 |
| --- | --- |
| 请求 | `v: 1`、`type: "request"`、`id`、`method`、`params` |
| 成功响应 | `v: 1`、`type: "response"`、`id`、`result` |
| 失败响应 | `v: 1`、`type: "response"`、`id`、`error: {"code":"..."}` |
| 状态提示 | `v: 1`、`type: "event"`、`event: "state.changed"`，可选 `session_id` |

`id` 是设备端随机生成的 UUID 字符串。响应回传业务请求 ID。
外层转发也有自己的 ID，各自独立。客户端只接收本次请求的响应，并核对它来自
预期的工作站。遇到不认识的附加字段可以忽略。遇到不支持的
版本或未知的消息类型时停止处理该消息。时间字段是 Unix 毫秒整数。
会话和交互 ID 是工作站提供的不透明字符串。工作站每次登记新的
待处理请求都生成新的交互 UUID，重启或重新绑定连接后也不复用。

### 查询

查询分页使用可选的 `cursor` 和 `limit`。默认每页 20 条，
`limit` 范围为 1–50。每页响应带 `next_cursor`，没有下一页时值为
`null`。客户端按服务返回的顺序追加，不解析游标。工作站可以按消息大小
少返回一些条目。快照分页期间收到状态提示时，客户端完成当前同步后再刷新。

| 方法 | 参数 | `result.items` 中的对象 |
| --- | --- | --- |
| `sessions.list` | 分页参数 | `session_id`、`title`、`state`、`updated_at_ms` |
| `history.page` | `session_id` 和分页参数 | `message_id`、`role`、`text`、`truncated` |
| `interactions.snapshot` | 可选 `session_id` 和分页参数 | 见下方待处理卡片 |

会话 `state` 为 `idle`、`running`、`waiting` 或 `closed`。
历史从最新的消息开始，`role` 为 `user` 或 `assistant`。单条消息
如果超过该页容量，工作站可返回带 `truncated: true` 的片段。
Go 工作站返回本机 Agent Done 当前跟踪的存活会话，覆盖所有工作目录。会话存活期间可以读取已有文本历史，包括从桌面恢复后保留的历史。会话结束后，工作站会从列表移除该会话并拒绝新的历史查询。缺少支持控制的原 Codex 代理连接时，会话仍可读取。

状态提示触发重新请求受影响的会话或待处理快照。App 首次连接和重连
都主动刷新。中转和工作站不承诺离线状态提示重放。

### 待处理卡片

公共字段为 `session_id`、`interaction_id` 和 `kind`。
工作站知道期限时附上 `expires_at_ms`。手机以工作站的实际有效性判断为准。

- `kind: "approval"` 附 `summary` 和 `can_approve`。可批准的命令或文件
  操作同时给出 `details`，包括 `kind: "command"` 或 `"file_change"`、
  `cwd`、可选 `reason`，以及 `command` 或 `changes` 文本。
  工作站无法提供足够的审批明细时设置 `can_approve: false`。
- `kind: "question"` 附 `questions`。每个问题包含 `id`、`text`、
  `options`（选项字符串数组）和 `allows_text`。选项和文本按问题 ID 回传。
  当前范围覆盖仍等待 RPC 响应的同步问题。

### 提交与结果查询

```json
{
  "v": 1,
  "type": "request",
  "id": "648df507-55ba-4a28-a487-f6aa219540b9",
  "method": "interaction.respond",
  "params": {
    "session_id": "session-7",
    "interaction_id": "957b7681-5455-441e-aa69-d8f22eec4f9b",
    "idempotency_key": "659c97e1-a084-45e7-949f-21c801b97f72",
    "response": {"kind":"approval","decision":"allow_once"}
  }
}
```

审批决定是 `allow_once` 或 `deny`。结构化回答使用
`{"kind":"answer","answers":{"question-id":["文字回答"]}}`。
手机每次确定一个操作意图时生成幂等 UUID，查询或明确重试同一次
意图时沿用。相同设备、交互和幂等键的相同内容返回已有结果。
同一键关联的内容变化返回 `conflict`。

工作站核对已配对设备授权，将交互 ID 绑定到原 Codex 连接及原请求 ID，
确保只能提交一次。首轮响应和 `interaction.status` 都返回
`result: {"interaction_id":"...", "status":"..."}`。状态查询参数为
`session_id`、`interaction_id`，并可传同一 `idempotency_key`。

| 状态 | App 解读 |
| --- | --- |
| `pending` | 仍待操作 |
| `submitted` | 工作站已向原 Codex 连接提交，等待确认 |
| `applied` | 工作站确认该意图被处理 |
| `closed` | Codex 已结束等待，具体结果或操作者未确认 |
| `expired` | 请求过期，或原 Codex 连接失效 |
| `unknown` | 工作站无法确认结果。同步状态后由用户决定是否再次操作 |

`error.code` 首轮为 `invalid_request`、`forbidden`、
`not_found`、`conflict` 或 `unavailable`。适配器只有在可确定
原操作被处理时才发 `applied`。收到 Codex 请求关闭但无法确认
是谁处理时使用 `closed`。转发失败或断线后，先从配对工作站查询与刷新，
避免自动重发可能已经送达的批准。

Go 工作站只向 `control_peers` 指定并已配对的设备公开待处理卡片和控制方法。它会转发具有完整工作目录和命令的审批、拒绝文件修改请求，以及回答阻塞且无密项的结构化提问。缺少可核对变更明细的文件修改只提供拒绝，异步问题、权限变更和密项继续由原桌面会话处理。工作站以写入原连接确认 `submitted`，以原请求关闭确认 `closed`。 [工作站配置](../docs/remote-relay.md#配置工作站)

## 继续会话扩展

工作站可在 `sessions.list` 的条目中添加 `can_send_message: true`，表示该手机有权在会话空闲时发起新的 Codex 回合。字段缺失或为 `false` 时，客户端仍可查看历史和处理已有交互。

| 方法 | 参数 |
| --- | --- |
| `messages.send` | `session_id`、`idempotency_key`、`expires_at_ms`、`text` |
| `messages.status` | `session_id`、`idempotency_key`、`expires_at_ms` |

两种方法返回 `result: {"session_id":"...", "status":"..."}`。`text` 是非空白的 UTF-8 文本，最多 16 KiB。手机生成幂等 UUID 和当前时间后两分钟的有效期，重试时沿用原参数。

工作站在原会话中发起新回合。提交前须核对发送设备仍有权限、会话空闲、原连接可用、有效期未过期且符合允许的时钟偏差。桌面输入与手机输入争用时只允许一次回合启动，工作站不排队，也不代替用户批准后续工具操作。发送前须持久登记发送设备、会话、幂等键和原内容摘要，重启后继续识别同一意图。相同键与参数返回原状态，内容或有效期不同则返回 `conflict`。

| 状态 | 含义 |
| --- | --- |
| `accepted` | 工作站已确认 Codex 接受了该新回合 |
| `not_received` | 工作站确认该意图未提交且仍在有效期内，用户可明确重试 |
| `expired` | 工作站能确认该意图未提交且已过期，用户可以发起新意图 |
| `unknown` | 工作站无法确认是否提交，手机保留草稿和幂等键并查询结果 |

工作站返回 `invalid_request`、`forbidden`、`not_found`、`unavailable` 或 `busy` 时，须能确认该意图没有被交给 Codex。若提交途中断线且无法确认，应返回 `unknown`。工作站在新消息或后续输出可通过 `history.page` 读取时更新会话 `updated_at_ms`，并发送经过认证的 `state.changed`，供前台客户端刷新。

## 联调材料

- [明文消息样例](fixtures/control-v1.json)
- [HPKE Auth 互操作向量](fixtures/hpke-auth-v1.json)
- [Swift CryptoKit 验证脚本](fixtures/verify-hpke.swift)

在有 CryptoKit 的 Mac 上，从仓库根目录运行：

```sh
swift protocol/fixtures/verify-hpke.swift protocol/fixtures/hpke-auth-v1.json
```

这些是公开测试密钥。真实设备需要生成各自的密钥并单独登记中转凭据。
