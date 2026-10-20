# Luke D33 候选独立审查报告

日期：2026-10-11（香港）。仓库：`servers5a3-git-upload-20261011`（共享交付目录）。
范围：仅卢克相关源与 test。装备、商城、部署、数据库、主工程未改动。
真实资产：`...\future-owned-instance\runtime\data\dnf\Script.pvf`（protected_nkpi，1,126,204 项）。

## 结论摘要

- 未发现需要改行为的"确定缺陷"；光暗路线、双 boss、材料过滤与真实 PVF 一致，锁序无反转、planner 无死锁。
- 两项限制已按"不猜协议"写入代码注释与 changlog：普通机械王座入场用终身 `DungeonPermission.ClearState`（无 reset/日界证据）；材料过滤按 `[material]` 抑制，覆盖面比点名的四种材料更广。

## 1. 队员光暗路线（实际入场后持久化 / 重连 / 换队 / shared energy / 新 epoch）

真实 `etc/raid/luke/luke.etc` 第二 `[PHASE]` 的 `[LOCKOUT TYPE]`：

| 路线 | 4 = 光 | 5 = 暗 | 0 = 共享 |
|---|---|---|---|
| 地牢 | 5311 5061 5057 5059 5064 | 5313 5062 5058 5060 5065 | 5063 能源控制 / 5066 能源储存 |

- 仅“成功入场”记录路线：`reduceCurrentLukeSceneLocked` 的 `enter` 分支在 `luke.EnterDungeon` 成功后调用 `commitCurrentLukeRouteLocked`；准备/失败入场不记录路线（`TestRealPVFLukeCancelledPreparationAndSingleModeDoNotChooseRoutes`）。
- 路线保存在同一服务进程内的 `runtimeRaidBattle.LukeMemberRoutes`，`cloneRuntimeRaidBattle` 深拷贝，快照不别名。该状态不是数据库持久化，不保证服务重启后的恢复。
- 重连（`handleOnlineRejoinRaid`）与换队（GroupIndex 变化）不清除路线（`TestRealPVFLukeAcceptedFollowersKeepRoutesThroughRejoinAndGroupMove`）。
- 过门/选图前统一校验：`currentRaidDungeonSelectionAllowed`、`freezeCurrentRaidDungeonGroupEntry`、`validateCurrentRaidDungeonEntry`。
- shared energy：`route == 0` 恒放行，永不选路。
- 新 epoch：`chosen.Attempt != b.Anton.Epoch()` 即视为未选，自动复位。单人/异相位/低阶段不受限。

结论：无缺陷。

## 2. normal sequential 42（前置 5101..5105、入口排除 5106）与 reset 语义

- `etc/sequential_dungeon_info.etc` 序号 42：`[dungeon index check] 5101 5102 5103 5104 5105 5106`，`[entrance except dungeon] 5106`，`[rewardable dungeon index] 5106`。
- `loadCurrentLukeThronePrerequisites` 解析正确：遇到 5106 停止，返回 `{5101..5105}`，入口 5106 排除。`dungeon/dungeon.lst` 确认 5106 = `Luke_Normal/Solium.dgn`，5101..5105 为同族普通卢克。
- 逐角色校验（`TestRealPVFLukeThroneNeedsEveryPersistedPrerequisiteForEachCharacter`），只读、不改资产，非王座地牢不受影响。

LIMITATION（已文档化，未猜日界）：入场判据是持久化终身 `DungeonPermission.ClearState`。序号 42 无 reset/day 字段，`etc/` 中也没有引用 5101..5106 的重置表，因此未实现服务日（06:00）跨天重置。若后续拿到 reset 证据再改；此前不得声称"每日精确"。

## 3. 双 boss 56699/56698 真实存活阻止提前 clear

- 真实地图 `map/luke_normal/light_altar/55946.map`：`[dungeon] 5104`，`[type] [boss]`，`[monster]` 同时声明 `56699 …[boss]` 与 `56698 …[boss]`（两张 boss 标记）。
- 真实地图 `map/luke_normal/dark_altar/55953.map`：`[dungeon] 5105`，单 `[monster] 56068 [boss]`。
- `currentLukeNormalBossesPending`：仅 5101..5106；当前房间 blocking boss 敌人数 >1 且存在未死 → 返回 true。据此在 op39 强制清房与 op117 通关判定两处阻断；两张 boss 都死后放行。
- 运行时阻断实测（`TestLukeNormalDualBossDeathsKeepSecondDropOwnerAndDeathAck`）：第一只死后 op117 不通过、房间未清；第二只死后结算并清除 gate。单 boss 房（5105）count=1 不受影响。

结论：真实布局为双 boss → gate 有意义；运行时阻断语义正确。

## 4. 实际 material planner：raid 材料 / 普通 altar 两份 essence / 通关后消耗品

- 点名 raid 材料在真实 PVF 全为 `[stackable type] [material]`：10159142 光之根源、10159143 暗之根源、10159623 魔岩石、10159649 黑白的守护（入场/复归材料）。故 `currentLukeTeamMonsterMaterialSuppressed` 的 `[material]` 判据覆盖它们；未发现其他 type 的 raid 材料。
- 光明之祭坛精髓 10159404 为 `[stackable type] [quest]` + `[daily delete item] 1`，走任务掉落路径（`checkCurrentDungeonQuestMonsterDrops`），不被材料过滤命中。真实 quest 2902 中 56699/56698 各产 1 个、上限 2，两个独立产出即两份（`TestRealPVFLukeDailyLightAltarEssenceRequiresTwoSeparateMonsterGrants`）。
- 过滤作用域：`ChannelType==32` 且 Mode 2 且非 Single 且 instance/phase/attempt/channel 全等；普通地下城/单人/金币/装备/阶段卡不受影响。

LIMITATION（已文档化，未改行为）：过滤按 `[material]` 全类抑制，比"点名的 raid 材料"更广；该广度需真机确认，未据此收紧或放宽。

## 5. 锁序与 planner 死锁审计

ROOT补充：`TestRealPVFLukeMaterialPlannerFiltersBeforeRegisteringDrops`直接调用生产掉落规划器并检查下行与拾取账本；多人卢克不产材料，普通/单人/无效尝试保留材料，各对照保留金币。合成规则仅为测试输入，不是对PVF或运行数据的修改。

- 既有顺序为 `raidMu → initialEquipmentMu`（`raid_luke_sanctum_maze.go:40→63`、`raid_luke_route_owner.go`）。未发现反向 `initialEquipmentMu → raidMu`：`raid_reward_flow.go`、`raid_reward_card_online.go`、`raid_presence_disconnect.go` 均在取 `raidMu` 前释放 `initialEquipmentMu`。
- planner 调用链：op39 → `handleDungeonMonsterDeath`（仅持 `session.dungeon.mu`）→ `planCurrentDungeonMonsterDrops`（`dungeon_combat_runtime.go:625`）→ `currentLukeTeamMonsterMaterialSuppressed` 取 `raidMu`；调用者未持 `raidMu`，无自锁。顺序 `dungeon.mu → raidMu` 与路线路径一致。
- 路线路径在 `raidMu` 下取 `initialEquipmentMu` 与新调用一致，未引入新锁。

结论：无死锁、无锁序反转。

## 修改文件

- `go-server/internal/services/dnfbridge/dungeon_luke_normal_rules.go`：新增 LIMITATION 注释（终身 ClearState，无 reset 证据，不加日界）。
- `go-server/internal/services/dnfbridge/dungeon_luke_review_regression_test.go`（新增）：真实 PVF 材料类型/抑制覆盖、终身入场语义、altar 双/单 boss 布局三组断言。
- `changlog.md`：新增本节审查条目。
- 未改装备/商城/部署/DB/主工程；未改任何运行资产。

## 实测

- `go test -buildvcs=false -count=1 -run Luke ./internal/services/dnfbridge`（`DNFBRIDGE_REAL_PVF_SMOKE` 指向真实 PVF）：exit 0，`ok … 85.295s`。含新增材料类型与终身入场两组测试。
- `go test -buildvcs=false -count=1 -timeout 120s -run TestRealPVFLukeAltarBossRoomsAreDualAndSingleBlocking ./internal/services/dnfbridge`：exit 0，`ok … 1.131s`（真实 map 55946/55953 双/单 boss 布局）。
- `gofmt -l` 对改动文件：clean。
- 未跑全量，未部署。

## 未修缺口

1. 普通王座入场无服务日重置（缺 reset 证据）；已按限制记录，未发明日界。
2. 材料过滤 `[material]` 全类抑制的广度待真机确认。

## 建议给最终全量验证的入口

- 全量或子集运行时带上 `DNFBRIDGE_REAL_PVF_SMOKE` 指向真实 `Script.pvf`，否则 real-PVF 测试会 skip。
- 重点复跑：`-run "Luke"`（dnfbridge 包）与其新增三测。
