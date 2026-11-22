# 从 DNF90_Server 移植组队/结算/回城修复的说明

本文件说明本 PR 的代码来源、修复了哪些问题（含根因）、如何把同一批修复移植到
`ServerS5A3`，以及配套的运行时开关。

- 上游基线：`ServerS5A3` `master`（本 PR 基于 `aac77691`）
- 我方来源：`DNF90_Server` 分支 `baseline-perf-20261122`，标签 `baseline-20261122-perf2`（`394bc629`）
- 客户端：DNF **V212**，服务端配置档 `DNFBRIDGE_CURRENT_CLIENT_VERSION=official-24.9`
- PR 规模：**87 个文件，+3793 / −747**（相对上游 `aac77691`），分 5 个提交

---

## 一、移植了哪些代码（按提交看）

| 提交 | 范围 | 文件数（约） |
|---|---|---|
| `port(party)` | 组队核心：绑定账本、回城退队、进图批处理、op9 投影、peer 快照/开关、缓存 | 54 |
| `port(card)` | 卡牌/结算/清房：BOSS 死亡结算、翻牌布局与奖励、深渊派对奖励 | 11 |
| `port(town)` | 回城/城镇：`dungeon_return_town`、`initial_town_*`、场景切换投影 | 9 |
| `port(town-menu)` | 城镇同队队员右键菜单（补发 0x0007，带开关） | 3（+删 1） |
| `port(misc)` | `DungeonSession` 零分配门面、进图放行计时、状态板类型、测试修正 | 6 |

**没有移植**（与本 PR 目标无关，避免扩大面）：

- 中继免锁写快路径与 relay perf（上游已有 socket buffer 调优）
- 仓储层只读缓存（`mysql_*_read_cache`，纯性能）
- `raid_reward_*` / `dungeon_force_clear`（依赖更深的副本运行时改造）

---

## 二、修复的问题与根因

### 1. 中途退图 → 回城踢人/退队，血条不消失、退不干净

- **现象**：副本里有人退出，剩余队员回城后画面残留血条，队伍成员被踢或显示"已退出队伍"。
- **根因**：`sendMemberGiveupSurvivorRosterInContext` 在 **owner=频道号** 的作用域里给退图者
  投影 `record(D, ch)`。客户端 `.prsafe` 的 `SetMemberUser` 会把
  `[localPlayer+0x4C8]`（绑定中的 `CPartyRecord*`）抢绑到这条记录上；之后所有
  `record(self, ch)` 因为 `oldUser == newUser` 变成**空操作**，血条/成员再也绑不回本机记录。
- **修复**：退图者的 kind2 只在 **owner=0（副本作用域）** 发送；回城用 kind3 静默退役
  `(D,0)` 与 `(D,ch)` 两条记录；不再依赖"重发本机记录"复位。

### 2. 回城后队长看队员"红电脑"

- **现象**：回城重绑时队员头像变红、显示连接中。
- **根因**：回城重绑时若先发 **空 kind2 复位**，客户端 `wasBound` 出现 1→0，
  触发 `P2P->closeAll()`，把刚建立的链路全部关掉。
- **修复**：删除所有"空 kind2 复位"；回城改由客户端 `.prsafe 0x06EC536B`
  （party != firstUser 才重设）自动绑回；跨频道（`sameChannel==false`）时补发 `(D, ch)`。

### 3. 副本内有人退图后，剩余队员"已退出队伍 → 重新组队 → 连接中 → 防火墙"

- **现象**：副本内 3 人时有人退，剩下两人收到"已退出队伍"，随后组队界面异常。
- **根因**：副本内（scene=3）`.prsafe` **不拦空 kind2**，空 kind2 会把绑定清零；
  之后 kind0 又走"0→1"建队分支，客户端重建 `CParty`。
- **修复**：**副本内不再发空 kind2**；退图后中继（relay）只做 sync，不 `CloseParty`；
  城镇仍按原语义发空 kind2 清残留槽位。

### 4. 城镇反复踢人/重新组人：缺血条槽位、连接中、红电脑

- **现象**：城镇组队后踢人再加人，血条错位、连接中、点队友变红。
- **根因**（三处叠加）：
  1. 点击队友（mode 15）会把**同队** `record(other, ch)` 投影给本机，抢绑
     `[localPlayer+0x4C8]`；
  2. `op11`（队伍成员表）早于 `CUser*` 创建发送，客户端建行时拿不到 actor；
  3. 中继 `portOwners` 未随队伍变更清理，残留旧端口映射。
- **修复**：
  - mode 15 与记录投影**双守卫**：永不投影"同队但非本机"的城镇记录；
  - 入队流程改为**先建 `CUser*` 再发 op11**；
  - `Sync`/`CloseParty` 同步清 `portOwners`；
  - `op10`（槽位表）使用权威槽位 `member.Slot`，不用压缩后的切片下标。

### 5. 队友 actor 重复补投 → 客户端重建角色、卡顿

- **现象**：换房/重连后队友角色"闪一下重建"，战斗中出现规律性卡顿。
- **根因**（两条）：
  1. 客户端对 ARQ1 的 `0x7f14` 不回应，服务端按链式重投重试 6 轮，每轮都重发队友
     actor，客户端反复 `CUser` 重建；
  2. 换房时对同房间 peer 重复投递 actor。
- **修复**：
  - `DNFBRIDGE_DUNGEON_PEER_RECOVERY`（默认 **off**）：关闭 ARQ1 延迟重投链；
  - `DNFBRIDGE_DUNGEON_PEER_OP37_RESEND`（默认 **off**）：不再重投 op37；
  - 换房不再重投已在该房间的 peer；
  - 新增 `dungeonPeerActorSnapshotCache`：成品字节缓存，避免重复编码。

### 6. 进图/换房慢（500ms+）

- **现象**：进图落位 500–560ms，换房偶发 100ms+。
- **根因**：进图放行流程在**事件循环内**同步做大量 DB 读（角色/装备/技能/背包/任务/账号
  设置）+ 帧构造，单人一次进图 ~10 次 DB RTT（40–80ms），4 人叠加。
- **修复**：
  - `party_loading_character_cache`：放行周期内共享角色快照，只读；
  - `character_enter_short_cache`：进图角色短缓存；
  - `buff_swap_reapply_cache`：buff 重放缓存；
  - 工作线程预热 + 分阶段计时（`game-post-start-map-placement-timing`、
    `game-finish-loading-release-tail-timing`）。
- **实测**：进图落位 513→561ms 区间稳定（`inventory_ms` 134–172、`mid_ms` 191–226），
  换房 tail 最大 63ms，中继 `forward failed` = 0。

### 7. 打完 BOSS 翻牌界面不出现 / 无法翻牌

- **现象**：BOSS 死亡后不弹翻牌，直接被路由回城。**纯上游代码同样复现**（非移植引入）。
- **根因**：上游当前 master 的结算链路（BOSS 死亡结算 → 卡牌布局 → 选择 → 提交 → 城镇路由）
  与 V212 客户端的翻牌流程不匹配；缺少本仓库对结算/卡牌这几处文件的修复，客户端不会进入
  翻牌阶段（`settlementCardLayoutSent` 永远为 false，结算直接放行城镇路由）。
- **修复**：`dungeon_boss_die_settlement.go`、`dungeon_settlement*.go`、
  `dungeon_card_price.go`、`dungeon_card_reward_runtime.go`、`dungeon_hell_party_reward.go`、
  `dungeon_party_card_flip_row_model.go`（op71 行状态改用**固定队伍槽位**而非压缩下标）、
  `dungeon_room_clear_quest.go`（零分配门面短路）。

### 8. 选图界面 → 回城，队伍血条空/残留

- **现象**：进入选图界面不动，退回城镇后队友血条变成空槽。
- **根因**：回城链路（`dungeon_return_town` → 城镇场景切换 → `initial_town_*` 投影）与客户端
  期望的录制顺序不一致；血条槽位依赖城镇成员记录的 owner/队伍信息，回城时投影缺失或顺序
  错位，客户端就保留空槽。
- **修复**：`dungeon_return_town.go`、`return_select_character.go`、`town_enter_select.go`、
  `town_loading_diagnostic.go`、`town_post_transition_player_state.go`、`town_position.go`、
  `initial_town_player_state.go`、`initial_town_route_state.go`、`guild_member_profile.go`。

### 9. 城镇右键"同队队员角色本体"不弹菜单（点血槽正常）

- **现象**：没组队时点角色能弹菜单；组队后点**同队队员**不弹，只有点血槽能弹。
- **根因**：mode15 分支里，observer 与 target 同队时，基线为避免
  `record(target, ch)` 抢绑 `[localPlayer+0x4C8]`，**直接 `return nil`** —— 连
  `0x0007 BuildRequestPeerSelectionNotice` 也不发了，客户端没有任何数据可挂菜单。
- **修复**：保留"跳过 op9 投影/抢绑"的护栏，仅**补发 0x0007 选择通知**；
  开关 `DNFBRIDGE_SAME_PARTY_TOWN_SELECTION_NOTICE`（默认 off = 基线行为）。

---

## 三、怎么移植（可复现步骤）

### 3.1 取我方改动

```bash
git clone git@github.com:tingfeng-h/DNF90_Server.git dnf90
cd dnf90
# 我方基线标签
git checkout baseline-20261122-perf2

# 生成"相对分叉点"的补丁（分叉点 = 上游 2d99f016）
BASE=2d99f016
git diff $BASE..HEAD -- \
  go-server/internal/modules/dnf/party \
  go-server/internal/services/dnfbridge \
  go-server/internal/modules/dnf/worldmap \
  go-server/internal/modules/dnf/raid > /tmp/party-port.patch
```

> 本 PR 已经是把这份补丁整理后应用在 `aac77691` 上的结果，**一般不需要再自己 apply**；
> 下面步骤用于你只想取其中一部分，或后续上游继续前进后重新移植。

### 3.2 应用到目标仓库

```bash
cd ServerS5A3
git checkout -b port/party-fixes master
git apply -3 --whitespace=nowarn /tmp/party-port.patch
```

### 3.3 冲突处理约定（重要）

高冲突文件都是"我方改动 + 上游也在改"的公共大文件，处理原则：

| 情形 | 策略 |
|---|---|
| 组队语义文件（`party_*.go`、`dungeon_party_*`、`dungeon_peer_*`） | **取我方版**（保留修复），再补上游新增引用 |
| 公共大文件（`game.go`、`service.go`、`subscribe` 等） | **取上游版**，然后按编译错误补我方新增字段/方法 |
| 上游新架构（异步队列 `party_room_move_queue.go`、socket buffer） | **保持上游**，不要用我方旧实现覆盖 |
| 中继链（`party_udp_relay*.go`） | **保持上游**；我方的免锁写快路径不移植 |
| 新增文件（我方独有） | 直接复制 |

应用后按编译错误补的常见三项：

```go
// 1) Service 结构体补字段（我方缓存）
characterEnterCache       characterEnterShortCache
dungeonPeerActorCache     dungeonPeerActorSnapshotCache

// 2) gameSession 补字段
clientBinding             partyClientBindingState
dungeonReturnAliasRetirePending uint32
fiendWarPartyClearOnTownSceneReady bool

// 3) worldmap.DungeonSession 补方法（IsCleared / RunStatus 零分配读取）
```

### 3.4 验证

```bash
cd go-server
go build ./internal/... ./cmd/...
go test ./internal/services/dnfbridge/ -run 'Party|Card|Town|Settlement' -count=1
go test ./internal/modules/dnf/party/ -count=1
```

- 组队状态机包：`internal/modules/dnf/party`（本 PR 只动 4 文件 +105/−3，状态机语义不变）
- 帧内容/帧内顺序约束：`op120-each-stable-slot-op3-all-op37-op30-op19-op14` 保持不变

---

## 四、运行时开关

| 开关 | 默认 | 作用 | 建议 |
|---|---|---|---|
| `DNFBRIDGE_SAME_PARTY_TOWN_SELECTION_NOTICE` | `off` | 城镇同队队员点击补发 0x0007 选择通知（弹菜单） | V212 客户端 **on** |
| `DNFBRIDGE_DUNGEON_PEER_RECOVERY` | `off` | ARQ1 延迟重投链（关闭可消除战斗规律性卡顿） | **off**（卡顿修复） |
| `DNFBRIDGE_DUNGEON_PEER_OP37_RESEND` | `off` | 换房时重投 op37 | **off** |
| `DNFBRIDGE_DUNGEON_PEER_ROOM_RESEND` | `off` | 换房重投同房间 peer actor | **off** |
| `DNFBRIDGE_PARTY_CARD_FLIP_ROW_MODEL` | `vendor` | op71 行状态档位（`vendor`=队伍槽位 / `legacy`=恒 0） | V212 **vendor** |
| `DNFBRIDGE_PARTY_UDP_RELAY_ENABLED` | `true` | 组队 UDP 中继 | on |
| `DNFBRIDGE_CLIENT_ATTEST_FILE` | 空 | 客户端校验文件（`mode: off` 关闭校验） | 按需 |

> 关闭任一开关即回到基线行为；所有开关都是"只加行为、不改已有帧"。

---

## 五、配套运行时配置（测试容器）

`runtime-snapshot/20261122-upstream-test/` 是本次验证用的完整运行时（已脱敏）：

| 文件 | 说明 |
|---|---|
| `compose-test.json` | 测试容器编排（端口、网络、健康检查） |
| `server.env` | 服务端环境变量（含上表开关） |
| `database.env` | 数据库账号（密码已替换为 `CHANGE_ME`） |
| `server/configs/dnfbridge.toml` | 服务端主配置（监听地址已替换） |
| `server/configs/dnf/logic.toml` | 逻辑层配置 |
| `server/configs/servergroup/plan.json` | 分组计划 |
| `server/client-attest.json` | 客户端校验配置（`mode: off`，secret 已脱敏） |
| `README.md` | 快照说明与替换清单 |

使用方式：把 `CHANGE_ME` / `<HOST_IP>` 换成你自己的值，再 `docker compose up -d`。

---

## 六、已知问题（本次未修）

1. **城镇点同队队员的"角色本体"** 已在服务端补发 0x0007 后正常；但如果将来客户端换版本，
   仍可能需要按客户端版本再调 marker（`0000`/`ffff`）语义。
2. **技能/装备等仓储层缓存**未移植（性能项，与本 PR 无关）。
3. **中继 socket buffer 调优**保持上游实现（上游已优于我方旧实现）。

---

## 七、回滚

```bash
git revert <本 PR 的 merge commit>
# 或运行时逐个关闭开关（见第四节），无需回滚代码
```
