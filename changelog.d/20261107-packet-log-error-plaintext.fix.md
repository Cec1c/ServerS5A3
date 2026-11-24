---
category: 功能修复
---

# 诊断日志的 error 默认明文落盘，凭证层继续脱敏

## 变更

- `redactPacketDiagnosticErrors` 对 `error` 类型及 `key=="error"` 的字符串默认明文落盘；需要恢复旧行为时设 `DNFBRIDGE_PACKET_LOG_ERROR_REDACT=on`。
- 进图步骤失败与 op28 延迟错误额外带 `detail` 字段（`err.Error()`），避免只留 `error_sha256` 无法定位。
- 顺带修正 `text, redact = value.(string)` 的 comma-ok 误写，避免非字符串值触发 panic。
- 登录包整体载荷脱敏（`loginPacketLogMetadata`）完全不变，不含明文凭证。

## 验证

- 本地 `go build -tags=timetzdata ./...` 通过；`TestNativeAuth42LoginLogsRetainOnlyMetadataAndHash` 通过，确认登录脱敏未受影响。
- dnfbridge 全包既有的 155 个顶层失败在改动前后一致；`TestR195PartyDungeonLoadingBarrierEventsReachNormalLogWithPacketLogOff` 改动前后同样失败，与本次无关。
