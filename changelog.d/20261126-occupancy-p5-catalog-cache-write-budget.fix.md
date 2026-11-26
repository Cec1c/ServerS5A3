---
category: 功能修复
---

# 优化在线活动目录缓存、窄读查询与会话写预算隔离

## 变更

- **PVF 活动目录只读缓存**：新增 `event_catalog_cache.go`，按只读 `platformpvf.Archive` 指针键缓存新学期累计签到、史诗之路和回归 NPC 赠送目录，以 `sync/atomic` 不可变快照服务热路径并在返回边界克隆，避免定时推送、城镇/副本地图查询与领取请求重复扫描与解析整包 PVF 列表和字符串。
- **在线活动窄读查询**：新增 `CharacterExtraStatsRepository`（`LoadCharacterExtraStats`）窄接口，在 MySQL 侧通过单条 `LEFT JOIN` 按目标 `stat_key` 精确读取角色基础状态与活动计数，供新学期累计在线与史诗之路路由使用，保留完整角色读取回退语义。
- **背包槽位投影去重排序**：移除 `LoadInventorySlotItemPrefix` 与 `LoadInventorySlotItemPrefixFound` 查询中多余的 `ORDER BY i.entry_key, e.extra_key`（调用方仅构建无序 map 投影），消除高频查询在数据库侧触发 `Using temporary; Using filesort` 临时盘表与排序开销。
- **会话独立写预算与回源隔离**：修复公会/队伍/喇叭广播写回（`guild_chat_delivery.go`）在已准入写队列后将单请求 deadline 叠加到独立传输预算的问题，并将公会面板与跨区组队邀请同步触发的成员资料预热（`guild_views.go`、`party_cross_area_invite.go`）绑定至会话独立 `gameTransportContext()`，避免瞬时负载波动导致无辜在线连接超时断开。

## 验证

- `gofmt -l`（23 个变更 Go 文件）、`go mod verify`、`go vet`（含 `-tags=mysql_integration`）通过。
- `./internal/modules/dnf/repository`、`repository/memory`、`repository/mysql`、`onlineevent` 包测试通过。
- `./internal/services/dnfbridge` 依赖闭包定向回归（含 `-count=5` 与慢接收方独立写预算测试）及真实 PVF 基准测试通过（新学期/史诗目录热缓存路径分配由 63 万+ 次降至 1 次 / 13 次，延迟下降 5–6 个数量级）。
- 真实 MariaDB 11.8.9 集成测试（`-tags=mysql_integration`）验证：背包前缀投影 `EXPLAIN` 零 `temporary_table` / 零 `filesort`、`Created_tmp_disk_tables` 与 `Sort_rows` 均由非零降至 0，活动窄读与全量读取结果一致。
- Linux 服务端二进制构建（`cmd/server/dnf90`）通过。
