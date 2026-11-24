---
category: 功能修复
---

# 稳定性止血：公会名单活跃窗口防抖、组队连坐与卡门修复及团本三项减负

## 变更

- **公会名单活跃窗口与防抖（整合原 `#121`）**：`memberListRequested` 改为仅由客户端主动请求/刷新公会窗口续期 60 秒活跃窗口，过期自动清理；`membershipRefresh` 纳入 250ms 防抖合并与副本战斗期延迟跳过。
- **组队跨会话写错隔离与路人误升级修复**：`broadcastCurrentPartyActorProjection`、`sendManagedRuntimePartySnapshotsForInitiator`、`synchronizePartyEnterSelectDungeon` 及组队成员通知扇出点在向非发起方写包失败时仅记受控日志，不再掐断发起方健康 TCP；`upgradeTownDisplayPeersForParty` 仅升级本队成员，不再误伤城镇无关路人。
- **副本过图已死怪防卡门**：`AnnounceAllActors` 对共享房间已击败敌对阻挡怪同步绑定并标记已击败，进房后对预击败怪按需补发无掉落权威 `op38`；`handleDungeonMoveRequestLocked` 首次命中 `ErrRunCurrentRoomNotCleared` 与重放命中 `ErrHostileNotBound` 时做一次 `O(房间怪数)` 纯内存对账自愈，并确保 `partyDeathVisualOnly` 后必发 `partyDeathStateOnly` 及低频卡门告警。
- **团本三处低风险减负**：安图恩/卢克二阶段启动未扣门票时跳过全团背包容器刷新；`startRuntimeRaidWithEntryCostPhase`、`createRuntimeRaid`、`createCurrentRuntimeRaid` 与 `rejoinRuntimeRaid` 将仓储 I/O 移出全局 `raidMu`；`raid_clock` 为超时空补齐 `SameProjection` 变更判断，无变更不刷包。

## 验证

- Docker Linux `golang:1.26.5-bookworm` 执行公会、组队连坐/路人隔离、副本卡门自愈与团本时钟/锁释放专项 `-count=1 -race` 测试、`go vet` 与 Linux 正式 `cmd/server/dnf90` 构建。
