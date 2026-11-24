---
category: 功能修复
---

# 攻坚中途重组后结算卡住：单人成员的隐藏组位不再与整队共用超过四人

## 变更

- 现象：2026-11-20 19:43 开打的 17 人安徒恩团（raid_key 1507329），第二阶段 20:08 通关后一直不发自动奖励和攻坚队奖励，服务端每 10 秒报一次 `dnfbridge raid reward freeze rejected reason=frozen_group_capacity frozen_groups=1:6,2:3,3:4,4:4`。
- 经过：两阶段之间大家反复退队、踢人、重新组队。19:51 前后 15、9 离开原 1 队的队伍，变成单人后服务端内部组号仍是 1。12 重新建的队伍 {12,67,14,204} 又被分进 1 队，因为单人不算占位。19:52:36 第二阶段开始，按内部组号冻结奖励归属，1 队冻结了 6 人；9 在 14 秒后才加入 2 队。冻结奖励要求每组最多 4 人，于是一直拒绝。
- 原因：当前布局下的单人在名单上显示为“单人”（NOTI591 组号 0），内部组号玩家看不到，但阶段开始时会原样冻结为奖励归属。整队放入某组、下一阶段补位、开团归一化这几处都只数“占位成员”，单人留在原组不计数，导致一个组号下超过 4 人。
- 修复（`raid_solo_seat_capacity.go`）：
  - 整队放进某组时，如果该组内部组位（含单人）超过 4 人，把不在副本中的单人挪到组号最小、仍有空位的组。只改内部组号和槽位，单人标记、显示和已冻结的奖励归属都不变。同步提交时保留这一改动，并在 `game-party-raid-groups-synchronized` 事件中输出 `solo_seats_relocated`。
  - 下一阶段开始前，按实际队伍关系再做一次同样的挪位。仍超过 4 人则拒绝开始。
  - 开团时按同样规则挪位；挪动时输出 `game-raid-solo-seats-relocated`。仍超过 4 人则提示“每个队伍最多编入4人，请调整编队”。
  - 本队成员、显式编组的成员、正在副本中的单人一律不挪；非当前布局（含魔界大战）不受影响。

## 验证

- 新增 `TestStartedRaidPartyMovesHiddenSoloSeatsOutOfItsGroup`：按 19:52:36 的现场形状，修复后 15、9 移出 1 队，冻结分组为 4/4/4/4/1。
- 新增 `TestRelocateOverflowSolosKeepsPinnedReservingAndActiveRows`：本队成员、占位成员、正在副本中的单人不动；旧布局不动。
- 调整两条上游断言：`TestStartedRaidFreePartyIgnoresSoloSeatsAndPreservesOtherParty`、`TestStartedRaidFreePartySequentialJoinsFreezeAllMembers`。改后规则是：单人仍保持单人，冻结归属不变，只有整队会与其共用超过 4 人时才挪走其隐藏组位。
- 使用线上 PVF（`1862d70d`）运行真实 PVF 用例，人工清组后进下一阶段、冻结归属保持、非法快照仍拒绝，均通过。
- `TestRealPVFAntonRegroupFinalGrants…` 在改动前后都因奖励数量与当前 PVF 不符失败（`qualified owner lost a pool 19`）。`TestStartedRaidFreePartyDoesNotStealOccupiedGroup` 在改动前就失败，原因是最大组数已从 5 改为 10。`SequentialJoins` 有原本就存在的时序不稳定（约 3.8 秒的连接证明超时）。
