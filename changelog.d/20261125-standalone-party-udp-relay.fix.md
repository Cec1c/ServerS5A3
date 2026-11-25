---
category: 功能修复
---

# 新增最小化独立 Go 组队 UDP 中继程序并缓存内嵌中继写超时

## 变更

- 新增独立 UDP 中继引擎 `go-server/internal/partyudprelay` 与最小化 CLI 入口 `go-server/cmd/party-udp-relay`，实现既有 v2 控制协议（`HELLO` HMAC-SHA256 认证、`SYNC`/`SYNC_OK`/`SYNC_FAIL` 有向端口矩阵分配与租约续期、`CLOSE`/`CLOSE_OK` 回收），支持每条有向腿首包学习 IPv4、同 IP 对称 NAT 换端口重学习、异 IP 源拒绝、4 槽首包缓冲、每腿限速、派对 60 秒 / PVP 180 秒端口冷却隔离与后台租约回收；不改变游戏服默认 `embedded` 模式与线上配置。
- 在 `go-server/internal/services/dnfbridge/party_udp_relay.go` 为内嵌中继转发路径引入按腿缓存的写超时（`ensureWriteDeadline`，超半周期刷新、写失败失效），消除逐包 `SetWriteDeadline` 调用，并补充 `party_udp_relay_deadline_test.go`。
- 补充单元与端到端测试（`internal/partyudprelay/server_test.go`、`dnfbridge/party_udp_relay_standalone_e2e_test.go`），覆盖 4 人 12 条有向腿回环转发、对称 NAT 换口、伪造 IP 拦截、限速、端口冷却/耗尽回滚、关闭与租约超时回收及密钥不入日志。

## 验证

- 在本地 Docker Linux（`golang:1.26.5-bookworm`）执行 `go mod verify`、`go vet`、`go test -count=1 -race`（覆盖 `./internal/partyudprelay`、`./cmd/party-udp-relay` 及 `./internal/services/dnfbridge` 组队 UDP 中继测试），并分别编译验证 Linux `cmd/party-udp-relay` 与 `cmd/server/dnf90` 二进制产物。
