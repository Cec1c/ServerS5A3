---
category: 功能修复
---

# 补通游戏管理接口全服实时公告广播（broadcast_notice）

## 变更

- 在 `go-server/internal/modules/dnf/adminapi/protocol.go` 补充 `ActionBroadcastNotice`（`"broadcast_notice"`）、`BroadcastNoticeRequest` 与 `BroadcastNoticeResult` 契约，对齐 GM 端 `POST /api/v1/announcements/in-game-broadcast` 转发结构。
- 在 `go-server/internal/services/dnfbridge/game_admin_api.go` 与 `game_admin_broadcast.go` 接入 `action: "broadcast_notice"`：支持 `style`（`"both"` / `"megaphone"` / `"system"`）、GB18030 长度与字符校验、`requestId` 幂等去重与冲突检测（`broadcast_conflict`）。
- 投递侧先拷贝在线会话快照再释放服务锁，通过各目标会话自身有界事件循环异步下发顶部全服喇叭（`NOTI 118`，`mode=11`、`senderUID=0`）及/或系统文字（`NOTI 12`，`type=0`、`sender=0xffff`），不持锁写网络、单个慢连接或断开连接不会阻塞或影响其他在线玩家；针对 `DNF.exe` `0x200CE2C–0x200CE7B` 按署名字符串比对预测扣喇叭（物品 36）的原生行为，投递前校验目标角色名并在同名时自动将署名避让包裹为 `[署名]`，避免客户端误扣本地库存显示。

## 验证

- 本地 Docker `golang:1.26.5-bookworm` 执行 `go vet`、`go build ./cmd/server/dnf90` 与 `go test -count=1 -race`（覆盖真实 HTTP `/api/game/admin` + 真实回环 TCP 客户端封包解码、三种 `style`、幂等与冲突、鉴权与参数校验、慢/断连会话隔离与并发竞态）。
- 发布、部署与线上实机验收待后续按正常维护窗口另行推进。
