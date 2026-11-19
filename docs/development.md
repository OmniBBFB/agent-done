# 开发说明

构建、测试、桌面验收和服务切换见 [开发与迁移](go.md)。

维护状态时请区分「已查看」和「请求已解决」。窗口聚焦仅清除提醒。回合开始、完成与打断负责对应的请求生命周期，`Stop` 和 `PermissionRequest` hook 单独出现时只用于诊断。

生产代码位于 [cmd/agent-done](../cmd/agent-done)。可选的真实桌面验收位于 [tools](../tools)，通过 Unix socket 或已构建的 Go 命令调用产品。按次验收报告和配置快照保存在 Git 忽略的 `outputs/`。
