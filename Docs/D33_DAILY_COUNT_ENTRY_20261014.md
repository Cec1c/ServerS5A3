# 异次元裂缝 / 艾肯 每日入场次数（DOC-083/094）证据与旧失败回执
日期：2026-10-14　范围：仅这两类入场的每日次数校验、提交与同批入场资产

## 1. 真档 PVF 依据

| 项目 | 真档证据 |
| --- | --- |
| 异次元裂缝区域 | `worldmap/DimensionGate.wdm` 区域 13，`[dungeon]` = 7100 + 7165-7179 |
| 次数归属 | 16 个副本全部声明 `[dimension dungeon]`（角色的**共享**日计数） |
| 额度来源 | `etc/dimensiongate.etc`：`[daily default enter count] 5`、`[daily default extra enter count] 0` |
| 疲劳 | 16 个副本全部 `[no fatigue]`（入场房间不扣疲劳） |
| 门票 | 各 DGN `[required item]` = 3332，按难度 1/3/5（对应 `etc/dungeonetc/dimensiondungeoninfo.etc` 的 `[num of require item by difficult] 0 1 1 3 2 5`） |
| 艾肯区域 | `worldmap/Legend.wdm` 区域 45「魔界集结地」，`[dungeon]` = 5097/5098/3570/3571 |
| 艾肯次数 | `etc/dungeoncyclesystem.etc` `[dungeon cycle]`：`45 -1 5097 3 0  45 -1 5098 1 1  45 0 3570 3 0  45 0 3571 1 1` |
| 艾肯疲劳/门票 | 5097/5098 扣入场房间疲劳；`[required item]` = 10158700 ×5 + 10161610 ×5 |
| 不同副本 | 316 魔界裂缝（`particular hell`）不在上述两张表内，沿用既有 3 次 + 2/4/6 成本 |

## 2. 旧失败回执 A：次数根本没有落库（修复前，基线 5fc6807）

```
TestBaselineProbeDimensionDungeonDailyCount:
  7100 dimension limit=5/0 source=vendor_pvf_etc_dimensiongate_etc
  after entry 1 the dimension daily count was not persisted:
    map[area_id:0 area_state:3 direction:5 fatigue:100 fatigue_display_update:56
        fatigue_limit:156 fatigue_service_day_id:20734 fatigue_used:56 pos_x:250
        pos_y:224 town_id:107]
TestBaselineProbeEkernDungeonCycleDailyCount:
  dungeon cycle limits=map[3570:3 3571:1 5097:3 5098:1]
  dungeon 5097 after entry 1: used=0 err=<nil>
```

## 3. 旧失败回执 B：次数提交失败会留下已扣的疲劳/门票（修复前，55e559a）

同一个桥级故障注入测试（`TestDailyCountEntryRejectionKeepsFatigueAndTicketsUnchanged`）在 55e559a 上的输出：

```
--- dimension_7100
  control_before fatigue=100 slots=map[0:20:{3332 1 ...}]
  control_after  fatigue=100 slots=map[]
  save_failure_after_prepare fatigue=100 slots=map[]
  prepare pre-deducted ticket 3332: map[]
  save_failure_after_commit  fatigue=100 slots=map[]     <- 次数写失败被拒，门票已经不在
  race_before fatigue=100 slots=map[]
  race_after  fatigue=100 slots=map[]
--- ekern_5097
  control_before fatigue=100 slots=map[0:22:{10158700 5 ...} 0:23:{10161610 5 ...}]
  control_after  fatigue=99  slots=map[]
  prepare pre-deducted ticket 10158700: map[]
  prepare pre-deducted ticket 10161610: map[]
  race_before fatigue=100 slots=map[]
  race_after  fatigue=99  slots=map[]                      <- 入场被 ErrDailyEntryLimit 拒绝，疲劳已扣 1
  character stats changed on a rejected entry:
    before=... fatigue:100 fatigue_used:56 ...
    after =... fatigue:99  fatigue_used:57 ...
```

结论：旧顺序（入场房间疲劳 → prepare 阶段门票 → 每日次数）在次数提交失败或同角色另一会话
抢走最后一次额度时，已经落库的疲劳与门票不会回来（补偿写回/事后返还不在本仓语义内）。

## 4. 本批修复

- 两类入场的每日次数、`ConsumeOnEntry` 门票、入场房间疲劳放进**同一个**角色资产事务：
  `dungeon.DailyEntryCommand` / `dungeon.DimensionGateEntryCommand` 新增 `InTransaction` 钩子，
  桥侧用它把 `fatigue.Refresh + Debit` 并入同一事务；门票只在既有锦标赛开关打开时随该事务扣除
  （与改动前的扣除条件、数量逐字一致），`consumeDungeonEntryItemsWithTournament` 对这两类入场跳过预扣。
- 次数判据在事务内、扣票前重新读取（额度用尽即整体拒绝），因此不存在"先扣后拒"的窗口。
- 范围只到这两类入场；316、4906-4910、锦标赛/史诗入场的事务边界与顺序未改。
- 未做：门票替代/免费通行（`[dimension freepass item]` 本仓未解析）、组队成员失败时的资产返还
  （既有语义为不返还，仅摘除运行时）。

## 5. 验证

- `TestDailyCountEntryRejectionKeepsFatigueAndTicketsUnchanged`：7100（无疲劳、门票 3332）与
  5097（疲劳 + 两张门票）各三个场景（成功 / 次数写失败 / 另一会话抢额度）在修复后全部通过，
  失败的入场前后角色 Stats 与背包槽位逐字节相同，且不采纳运行时。
- `TestRealPVFDimensionDungeonSharedDailyCounterAndEkernCycleLimits`、
  `TestCurrentDungeonCycleDailyLimitsParseAndFailClosed`，以及 12 项既准入相关真档用例通过。
- `./internal/modules/dnf/...` 与 `./internal/services/dnfbridge` 两个包全部测试通过（含
  `TestDungeonBridgeFilesHaveNoDirectRepositoryWrites` 的域边界约束）。

## 6. 待原生验证

- 官方 24.9 客户端 class0/812 计数下行的字节布局与发送/重登时机尚未闭环：本批仍然**不发包**，
  客户端"剩余次数"显示不会因为本批而变化。
- 门票从 prepare 阶段移到 commit 阶段（同一入场流程内、发 entry 包之前）属于时序变化，
  需要一次原生复测确认客户端背包在进图前正常刷新。

