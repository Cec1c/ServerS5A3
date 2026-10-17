# QST-002：奇怪的兵器——中途房间目标与父子进度分歧

## 目标与基线

- 目标：完成梅尔文、泽丁对话后，在根特南门的真实目标房间推进任务；活动任务投影与最终提交条件一致。
- 状态：**B父回调修复及自动回归通过；A中途清房领域层执行中。** 玩家正常流程由用户按实测清单验证。
- 公共基线：`7972cce4071c1029c4bfbe42b3c2f9a067dc34b0`。累计基线在私有交接中记录，同一探针亦在累计基线验证。
- PVF SHA256：`fef4f46f6c3096d10cd88c84579dea8bd4fffa18e5a75b4684a2e920864f5525`。
- 配套客户端 V124 SHA256：`eaa2485609fa9cc2c9ca99fadd743b5206a37b5517dd07d195eb35b3c0a29d50`。本次核对文件摘要，未运行客户端或新增二进制反编译结论。

## 现象与真实定义

反馈包括“活动栏显示完成叹号，任务手册未完成”和“无法完成”。公开索引中没有可直接解释本链的匹配客户端函数记录；以下证据来自实际 PVF、当前服务端和内存仓储测试。

| ID | 真实定义 | 目标 | 奖励 |
| --- | --- | --- | --- |
| 3525 | epic / quest clear / subtype 2 | 顺序链 3558、3559、3560；前置 3524；等级 65–99 | item `0 0`，父任务有正常经验 |
| 3558 | sub / meet npc | NPC 59（梅尔文） | NoExperience，item `0 0` |
| 3559 | sub / meet npc | NPC 58（泽丁） | NoExperience，item `0 0` |
| 3560 | sub / clear map | map **92247**；dungeon info `82 -1` | NoExperience，item `0 0` |

定义路径为 `n_quest/quest_renewal/epic_quest/antber/antber_epic_14.qst` 及其 `_01`、`_02`、`_03` 文件。真实 `Catalog.Find` 与脚本一致，四项奖励解析均无错误。**这不是 QST-001 的末子任务物品奖励遗漏。**

副本 82 的 maze **2** 绑定 `[0 3560 -1]`。92247 位于 **(2,1)**，类型为普通 map；最终 Boss 位于 **(4,0)**，map **92251**。此迷宫没有需要逐层切换的 story-stage 定义，不能按“缺少层切换”处理。

92247 声明六个普通怪 61801 和友军 APC 10448；`Action/3525_92247.act` 在 `ON START MAP` 播放 cinematic 2208。`cinematic/Dungeon/Cataclysm/Act11/82_GentSouthGate/2208.cmt` 只声明移动、转向和传话对白，没有显式任务完成指令。是否由客户端通用剧情逻辑发出子任务回调，仍需真实报文确认。

## 已确认的失败链

1. **接取及对话路径可走通。** 实际目录接取 3525 后父进度为 3，三个子任务均 active/1。探针按子任务 op33、父任务 op33 推进两段对话，3558/3559 成为 completed/0，父进度为 1，3560 仍 active/1；入图规则正确选择 maze 2。
2. **中途清房没有权威 clear-map 推进。** 使用实际迷宫进入 92247，六个怪物死亡都经过 `handleDungeonMonsterDeath` 并被接受。结果 `scene.Cleared=true`、`Boss=false`、run 仍 active，而 3560 仍 active/1，3525 仍为 1。`dungeon_combat_runtime.go` 的清房路径没有普通中途房间的 clear-map 记账；现有 `dungeon_clear_map_completion.go::persistCurrentDungeonClearMapCompletionPhaseA` 要求最终 completed run 和 boss-die-check 接受状态，仅把当前最终地图送入 owner。用最终地图 92251 调用真实 planner 也不会匹配 92247。
3. **父标量回调可制造“可交但提交失败”。** 对上述未完成 3560 的记录发送父 3525 的 op33/type0/decrement，`PlanTownLinkedProgress` 无可归档对话子项，bridge 回退到 `ApplySetTrigger`，直接把父进度 1 减到 0。此时 op574 投影包含 `3525=0, 3560=1`，但 `ApplyFinishSettlement → finishQuestClearTrigger` 重新检查真实子项，返回 `ErrFinishQuestNotPending`。最终校验正确，应保留。
4. **目标得到真实匹配后能结算。** 仅在测试中以明确的 92247 完成事实调用现有 planner，3560 归档、父进度归零；父结算及成功 body 构造通过。65 级夹具本次经验为 343001，无物品奖励。这是 planner/结算正对照，不代表运行时已有该清房接线。

两个失败子测在公共与累计基线均复现。父回调序列为人工构造的受控探针，不是玩家录包；因此已证实的是上述服务端缺口及可导致的状态分歧，尚不能把玩家原故障唯一归因于“客户端漏发 3560 回调”。

## 解决思路与执行边界

建议分成两个可独立验证的小目标，不修改 PVF、不硬编码 3525/3560/92247，不放松父任务提交条件。

**A. 让已证明的中途清房事实推进匹配任务。** 在权威房间死亡/清房边界，建立独立于最终副本结算的 room-clear quest 路径。只接受当前角色拥有的 runtime、选中迷宫内实际解析的目标 map、已经清除全部阻挡对象的场景，以及该真实 PVF 迷宫关联的活动 clear-map 子任务与活动父任务。按本次所证范围先支持这种无奖励 linked-sub 形状，不能把一个房间清空扩展成整个 dungeon 已通关。

复用领域现有“子项归档＋父进度重算”规则；新增专用 room input/过滤，避免直接将 `DungeonID=82` 传给宽泛 planner，顺带完成所有以副本 82 为目标的任务。记录本次 run/房间/目标的幂等依据，在同一任务事务提交；失败可重试。不能借此置 `bossDieCheckAccepted`、清掉最终 Phase A 标记、发结算翻牌或奖励。提交后沿现有 op574/op356 最小投影路径通知，具体时序由回归和原生验收确认。清房前的入图剧情若已合法推进，应幂等保持。

**B. 防止父任务标量归零先于子任务事实。** 在 `quest_trigger.go` 与 linked-objective 领域入口处理 quest-clear 父回调：用已提交子项状态计算并回传剩余数，不让“没有可归档子项”退回普通标量减法。保留普通单任务 trigger、合法对话子回调、终端 active/0 子项和同会话防重语义。已有错误 parent=0/child>0 状态也应通过明确的重算路径恢复一致，不能补造子任务完成记录。

允许修改范围：`quest/linked_objective*.go`、必要的独立 room-clear planner/owner、`dnfbridge/quest_trigger.go`、独立 room-clear 接线文件及相关死亡/组队重放入口、对应测试。`quest/finish_owner.go` 属于 QST-001 并行范围，本项不修改；若发现必须共改，由 Seeker 合并协调后另派。

## 测试与验收目标

- 保留本次真实 PVF 红例：中途房间清除全部六怪后 3560 应完成；未满足子项时父回调不得发布虚假的零进度。
- 负对照：仅进房/未清完、错误地图 92251、非当前 runtime、缺少或未接取父子项、错误角色/迷宫，不得凭本次房间事件完成 3560；最终 Boss 不能替代未证明的中途房间。
- 正对照：NPC59→NPC58 的实际定义与自动子项激活；正确目标完成后父 op34 一次结算、同会话重复抑制、重连回执；没有物品奖励，也不补发子项经验。
- 覆盖事务失败重试、父进度重算、重复清房及客户端晚到回调；若支持组队，分别验证共享房间事实与每名角色的任务资格。不得将单人通过报告成组队通过。
- 调查探针为 `TestSeekerQST002RealPVFProbe`：`middle-room-must-credit`、`parent-callback-must-not-finish-unresolved-child` 是预期红例；`target-credit-finishes-parent` 为正对照。文件与复跑位置由私有交接提供。实现完成后再按仓库 AGENTS 执行所需全量验证。
- 原生验收：V124 中接取、两段对话、进入正确迷宫和 92247 剧情/清房、活动栏与任务手册状态、提交及重登。按同一连接/角色/时间留 op33/34 和 op574/356、任务四行状态、入图 maze/map、房间清除事件；尤其确认父回调前是否已有 3560 子回调及其 trigger type。

## 交接结果

B已实现：父回调重算真实子项，历史父0/子1修正为1；全部合法子项active/0时父可交但子项物品继续由op34事务结算。未变化回调只校正一次，子项后来变化仍可推进。普通trigger、目录缺失边界、保存后失败回滚、末子任务奖励及同会话反馈循环回归通过。领域、bridge、control完整回归及list/vet/build通过；条件数据库测试跳过。A仍未完成，尚未部署。允许后续执行失败：返回具体尝试、失败命令与证据、精确 diff 和需要确认的问题。QST-004 的普通迷宫随机对象拒绝属于另一入图故障，本次 QST-002 的 maze2 实际入图已通过，不能合并为同一成因。
