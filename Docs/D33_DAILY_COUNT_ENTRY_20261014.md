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

## 6. 当前 EXE 的计数下行证据（2026-10-14 补）

对象：`H:/YKCN90工作区/正式客户端-v96/client/DNF.exe`，SHA-256 `8bf1d39d…41e387`。
方法：解析 PE 后扫描登记函数 `0x25242a0` 的全部调用点，重建「opcode → handler」表，
再用 capstone 反汇编相关 handler 与它的消费方。

- **812 不是次数投影**：本 build 的 812 handler 是 `0x1fd5f20`（不是旧注释里的 `0x01049E00`），
  它读取 `u8 kind + u32 对象句柄`，随后按 kind 走两套分支：kind=1 读 `u8 count` 后循环
  `u8 索引 + 1 字节值` 调 `0x133b060` 设对象字段；kind=0 读 `u16 + u32 + u8` 调
  `0x133a570/0x133b740/0x133c710/0x133cae0`。这是"按句柄设字段"的通用处理器，与每日次数无关；
  同一 build 里形如 `u32/u32/u8` 的 handler 有几十个（200/276/279/347/348/352/783…），
  说明旧注释把这条形状当成 812 的证据并不成立。
- **厂商的 806 不能用**：本 client 的登记表里 806/805 都没有登记项，厂商
  `DIMENSION_GATE_ENTRANCE_INFO = 0x0326` 的 `u32 remaining + u32 extra` 在本 build 无人消费。
- **真正的次数载体是 829/830 家族**（本仓已用于 316 的同一套）：
  - 829 handler `0x203b5a0`：读 `u32 limit` → `0x2961a20` 写 `[manager+0x8d4]`；再读 0x104 字节
    原始块（= 1 个被忽略的 `u32` + 32 × (`u32 dungeonId`, `u32 used`)），逐对调 `0x2969320`
    写入 `map[manager+0x8d8][dungeonId] = used`。
  - 830 handler `0x20351b0`：读 `(u32 dungeonId, u32 used)` 调同一个 `0x2969320` 更新一对。
  - 显示口径由消费方钉住：`0x1591c9f` 读 `0x2961a30`（limit）与 `0x2969340`（map[dungeonId]），
    然后 `sub ecx, eax` = **limit − used**，再按 ≤0 切换样式格式化显示。
  - 结论：829/830 的第二项是**已用次数**，客户端显示 `limit − used`（剩余次数）。
- **仍未实现投影**（不发猜测包）：829 的 limit 是 manager 上的**单个标量**，一份 829 会把 32 对
  全部重写，因此 316（3）、异次元裂缝（5）、艾肯（3/1）混合额度无法在同一份载荷里无歧义表达；
  且客户端 UI 是否真的为 7100/5097/7165-7179 这些 id 渲染该计数（依赖 client 侧 PVF/UI 配置）
  没有证据；艾肯的每副本额度还可能来自 client 自己读的 `etc/dungeoncyclesystem.etc`。
- `etc/dungeoncyclesystem.etc` 第 2、5 个整数（-1/0 与 0/1）**仍未证**：C# 参考服务端没有任何
  解析 `[dungeon cycle]` 的代码；当前 EXE 里也读不到明文 .etc 名称（全文件只有 `NpkIndex.etc`、
  `MissionSystem.etc` 两处），因此字段语义无法用本 build 的 EXE 反证。实现继续只按"副本 id
  后一位是额度"使用第 4 个整数，其余位置不解释。

## 7. 待原生验证（同事务修复之前列出的项）
## 8. 艾肯次数语义更正：按**通关**计数（2026-10-14 二次核对）

上一批按"入场"计数是**未证的推断**，已更正为按通关计数，依据是真档每日任务：

- `n_quest/new_quest/daily_90_legend_01.qst`（单人）：`[grade] [daily mission]`、
  `[type] [hunt enemy]`、`[dungeon info] 5097 -1 5098 -1`、
  `[int data] 5097 -1 53764 3 3  5098 -1 107001865 1 1`（53764 正是 5097 的
  destroy-object 通关目标），条件文案「通关3次『艾肯（单人） - 遗落的神殿』、1次『艾肯（单人）
  - 试炼』。(通关3次『遗落的神殿』后会出现『试炼』地下城。)」。
- `n_quest/new_quest/daily_90_legend_cdnf_01.qst`（组队 3570/3571）：`[type] [condition under clear]`、
  `[int data] 3571 -1`，文案同样是「通关3次…1次…」，并注明试炼的出现条件。
- 两点结论：`[dungeon cycle]` 第 4 个整数 = 当日**通关**上限（3 / 1，与任务逐字一致）；
  5098/3571（试炼）的解锁依赖当日通关 5097/3570 三次（第 5 列 0/1 与之相关，但第 2/5 列的
  确切含义仍未证）。5097 与 5098 共用同一个区域入口（`worldmap/Legend.wdm` 区域 45，
  任务 `[dungeon info]` 也把两者放在同一组）。

实现随之调整：

- 入场只提交门票 + 入场房间疲劳（同一事务，并在事务内复核当日次数未用尽），**不写次数**；
  中止/失败的入场不再吃掉当天次数。
- 通关时（boss die check 结算、每轮一次，进入运行时 Completed 之后）提交一次当日通关次数；
  提交失败只记事件、不阻塞已经打完的结果流程。
- 异次元裂缝（7100/7165-7179）维持按**入场**计数：`etc/dungeonetc/dimensiondungeoninfo.etc` 的
  `[hidden boss appear rate by enter count]` 与 `[max daily enter count]` 都是 enter 口径。

EXE 侧观察（本轮补充，注意**不构成**「客户端不读这张表」的结论）：当前 v96 客户端里 `dungeon cycle`、
`[dungeon cycle]`、`dungeoncyclesystem` 三种写法在 ASCII 与 UTF-16 下零命中（唯一的 `cycle` 命中是
protobuf 的 `lifecycle_id_generator_`）。这只说明「未以明文形式出现」；客户端完全可能通过字符串池/
哈希表间接持有这些标签，因此**不能**据此断定客户端不打开该表。该表语义仍只由 PVF 任务文案定，
客户端是否消费它保持未证。
另外客户端确实内嵌 protobuf 描述符 `DNFPB.noti.PB_ENUM_NOTIPACKET_DAY_OF_THE_DUNGEON.dungeonEntryCount`
（来自 `DNFShared/PB_Packet/PacketHeaders.pb.cc`）——这是"每日副本"家族的次数 NOTI，与艾肯/裂缝的对应
关系未证，列为待查项，不发猜测包。

## 9. 待原生验证（通关计数更正后）

- 客户端"剩余次数"显示：本批仍然**不发包**（829/830 已用于 316，未扩到这两类入场），因此
  7100/5097/7165-7179 的界面计数不会因为本批而变化；要扩需先钉住 829 limit 标量的作用域与
  这些 id 的 UI 渲染证据。
- 门票从 prepare 阶段移到 commit 阶段（同一入场流程内、发 entry 包之前）属于时序变化，需要
  一次原生复测确认客户端背包在进图前正常刷新。
- 组队入场：成员提交失败时既有语义是"只摘运行时、不返还资产"
  （`rollbackRuntimePartyPreparedFollowers`），本批没有改变它；成员侧被回滚的那些次提交是否会
  留下已提交的次数/门票/疲劳，需要单独的原生/评审确认。

## 10. 试炼前置与通关计数幂等：RED/GREEN 回执（2026-10-14）

RED = 修复前（1c9ce61）用只依赖两版共有 API 的临时探针 `TestZZCyclePrerequisiteAndNonceRedProbe`：

```
RED_A prepare_5098_error=<nil>
RED_A: 5098 accepted without the three-clear prerequisite
RED_B used_after_two_identical_submissions=2
RED_B: retry double counted (2 != 1)
RED_C clear nonce 9 rejected early: dungeon daily entry limit reached
--- FAIL: TestZZCyclePrerequisiteAndNonceRedProbe
```

GREEN = 修复后（探针 + 两个常驻用例）：

```
RED_A prepare_5098_error=dungeon entry prerequisite is not met
RED_B used_after_two_identical_submissions=1
RED_C over_limit_error=dungeon daily entry limit reached
--- PASS: TestCurrentDungeonCycleEkernPrerequisiteBlocksEntryWithoutConsumingTickets
--- PASS: TestCurrentDungeonCycleClearCountRetryIsIdempotentAndCannotBypassLimit
--- PASS: TestCurrentDungeonCycleDailyLimitsParseAndFailClosed
```

实现要点：

- 试炼前置（服务端强制，不依赖客户端隐藏）：prepare 判一次，`EntryAssetsCommand` 在同一事务里
  再复核一次（`AllowanceDungeonID/Limit` + `PrerequisiteDungeonID/Count`）；失败整批回滚，
  门票与入场房间疲劳一分不扣。前置由表结构推导（同 worldmap、同第 2 列分组内，第 5 列=1 的副本以
  第 5 列=0 的副本为前置，所需次数取后者第 4 列），与真档任务文案 5098←5097≥3、3571←3570≥3 一致。
- 通关计数：成为这一轮通关的**阻塞前置**（`commitCurrentDungeonCycleClearCountLocked`，位于
  clear-map 同步之前）。失败时保持 `dungeonCycleClearCountCommitted=false` 并直接返回——不进入
  后续结算、不发 op115/终局包，客户端重传触发重试；计数带 `ClearNonce = runtime.lifecycleToken`，
  同服务日同 nonce 是幂等回放，重试只计一次，失败也不会绕过每日上限。
## 11. nonce 跨会话唯一性修复（2026-10-14 主审阻断点）

问题：`ClearNonce = int64(runtime.lifecycleToken)` 不跨会话唯一（`session.dungeon.runToken` 每会话从 0 递增，
重登后首轮又是 1），当日已记 nonce=1 时，重登后的新一轮会被误判成幂等回放而不计次数 → 可反复绕过。

RED（a125397，探针 `TestZZCycleNonceSessionIndependenceRedProbe`，两个「新会话首轮」lifecycleToken 都是 1）：

```
RED_NONCE after_session_1_first_run used=1
RED_NONCE after_session_2_first_run used=1
RED_NONCE: second new session was treated as a replay (used=1, want 2)
--- FAIL: TestZZCycleNonceSessionIndependenceRedProbe
```

GREEN（本批，同一探针 + 常驻用例）：

```
RED_NONCE after_session_1_first_run used=1
RED_NONCE after_session_2_first_run used=2
--- PASS: TestCurrentDungeonCycleClearCountNonceIsSessionIndependent
--- PASS: TestCurrentDungeonCycleClearCountRetryIsIdempotentAndCannotBypassLimit
--- PASS: TestCurrentDungeonCycleEkernPrerequisiteBlocksEntryWithoutConsumingTickets
--- PASS: TestCurrentDungeonCycleDailyLimitsParseAndFailClosed
```

实现：

- nonce 改为**每轮独立随机**的 128bit 值（`ClearNonceHi`/`ClearNonceLo` 两个 int64 半段持久化，
  `crypto/rand`；全零视为未生成并重取），首次提交时生成并固定在本轮运行时上
  （`ensureCurrentDungeonCycleClearNonce`），不再使用会话内递增的 `lifecycleToken`。
- 重试用同一对值 → 幂等回放只计一次；不同会话/不同轮次必然不同 → 重登后的新一轮一定计数，
  不会撞号漏计。128bit 也把同一天内多轮的撞号概率降到可忽略，避免「碰撞被当成回放」的漏计。
