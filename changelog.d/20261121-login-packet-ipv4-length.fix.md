---
category: 功能修复
---

## 2026-11-21 class=1/op=1 登录首包按 582+4*N 结构化校验

### 问题与原因

- 现行客户端 `DNF.exe`（`VA 0x0252cae0`）在 `class=1/op=1` 登录包尾部写入本机 `gethostbyname(gethostname())` 返回的全部 `N` 个 IPv4 地址（`N >= 1`），精确包体长度为 `582 + 4*N`（含可选 16B 字段时为 `598 + 4*N`）。
- 服务端 `protocol/stream.go` 与 `dnfbridge/initial_town_route_state.go` 原先使用离散长度白名单 `{586, 590, 594, 598, 606, 610}`，漏掉 `N=5`（`602B`）及 `N>=8`（`614+B`），导致多虚拟网卡/WSL/加速器玩家登录包滑落至 legacy 分支并在鉴权前断连，客户端卡在「正在连接服务器……」。

### 改动内容

- 在 `go-server/internal/modules/dnf/protocol/stream.go` 导出 `IsClientLoginEndpointBodyLen` 与 `IsClientLoginEndpointBody`，按 `582 + 4*N`（`1 <= N <= 64`，兼容可选 16B）及实机 512B 段头/IPv4 数量自洽规则识别，并严格拒绝相邻错误长度。
- 在 `go-server/internal/services/dnfbridge/initial_town_route_state.go` 复用 `protocol.IsClientLoginEndpointBodyLen`，消除离散长度硬编码。
- 补齐 `N=5 (602B)`、`N=8 (614B)`、`N=12 (630B)`、`N=64 (838B)`、可选 16B 及严格鉴权/令牌绑定/非法相邻长度回归测试。
