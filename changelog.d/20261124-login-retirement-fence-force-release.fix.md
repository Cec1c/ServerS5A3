---
category: 功能修复
---

# 登录栅栏不再永久泄漏，事件循环卡死后账号仍可重登

## 变更

- `account_login_replacement.go` 新增 `forceReleaseRetirement`（按账号）与 `forceReleaseSessionFence`（按会话）：等待旧会话收尾超时后强制拆掉 `retiring` 栅栏，并从 `sessions` 里移除已失效会话。
- `authenticateRemoteLogin` 的等待分支在 `ctx.Done()` 时调用该方法并记 WARN；本次登录仍按原逻辑失败一次，下次登录即可直接通过。
- `game_session_events.go` 的延期 stop 由无界 `context.Background()` 改为带界 `context.WithTimeout(gameSessionShutdownWait)`；stop 超时/失败时主动释放栅栏并记 WARN，避免挂在永不触发的 `finished()` 上。成功路径保持原逻辑不变。被拆会话早已 `loginRevoked` 且 transport 关闭，放行新登录无副作用。

## 验证

- `go build -tags=timetzdata ./...` 通过；dnfbridge 全量测试在改动前后失败集合一致。
- 现场：90CN 曾因 `database/sql` closingMutex 互等导致事件循环停不下来，账号被永久锁死、客户端一直停在“正在连接服务器”；本修复给出有界等待与兜底释放。
