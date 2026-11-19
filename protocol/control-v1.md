# 端到端控制协议草案

这份草案用于工作站与 iPhone 互操作。以下消息只存在于中转信封的
加密载荷内。配对、加密编码和 Swift 测试向量在互操作验证后冻结。
中转程序可以独立按 [外层传输规格](relay-v1.md) 开发和测试。

## 业务消息

| 消息 | 请求参数 | 响应 |
| --- | --- | --- |
| `sessions.list` | 分页游标 | 授权范围内的已有会话及下一页游标 |
| `history.page` | 会话 ID 与分页游标 | 已保存的消息与下一页游标，读取历史不恢复会话 |
| `interactions.snapshot` | 可选会话 ID | 当前可处理请求及交互 ID |
| `interaction.respond` | 会话 ID、交互 ID、幂等键及审批决定或结构化回答 | 端点验证和提交状态 |

操作申请有应用层请求 ID，响应带回该 ID。首次连接或重连时
先同步快照。实时变化通过工作站发出的加密事件更新 App。
中转层的请求 ID 只用于相关联的转发结果。

## 一次性审批

```json
{
  "v": 1,
  "type": "request",
  "id": "request-12",
  "method": "interaction.respond",
  "params": {
    "session_id": "session-7",
    "interaction_id": "interaction-19",
    "idempotency_key": "intent-31",
    "response": {"kind":"approval","decision":"allow_once"}
  }
}
```

审批决定为 `allow_once` 或 `deny`。结构化提问可以使用
`{"kind":"answer","answers":{"question-id":["文字回答"]}}`，
问题 ID 和选项由工作站发出的卡片确定。

工作站核对对端设备授权，并将交互 ID 映射到原 Codex 连接、
原请求 ID 和有效期。在身份、内容和请求生命周期都有效时，
同一交互只允许一次有效提交。桌面处理过、已过期和连接已更换
的请求返回对应的应用结果。重复提交同一个幂等键时返回已有结果。

「工作站已接收」「已提交给 Codex」「Codex 已结束等待」是不同的
状态。App 用端点的加密结果更新卡片。转发超时或断线后先查询，
避免自动重发可能已经送达的审批。
