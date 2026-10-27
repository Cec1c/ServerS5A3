# 超时空已有遭遇战组队恢复的队友模型投影（R169）

状态：候选代码与专属回归已落盘，尚未执行 Go、构建、串行测试或部署。基线为
`6f67b23874bc2d8c6f825fc091b822e528cae196`，独立 worktree 为
`H:/codex-work/20261021/r169/party`。

## 根因证据

`rejectCurrentFiendWarActivePartyRequestDecision` 的已准入成功分支此前只写
class1/op10 成功 ACK，然后调用 `sendDungeonPartyFrameRoster`。该 roster 只发送 op153
和每名成员一条 op9，可由 `party_online.go` 的 `sendDungeonPartyFrameRoster`
实现直接确认。

队友模型的对象创建位于另一条调用链：`projectDungeonPartyPeerActors` 调用
`sendDungeonPartyPeerActorState`，发送 mode0、mode1、op9。它要求双方有同一
`dungeonPartyRunKey`，按 `boundGameSessionCharacter` 记录 claim，发送失败只释放当前
方向的 claim。successful existing-party 请求没有调用这条投影，因此客户端即使重新收到
队伍名单，也没有收到重建 peer actor 所需的 mode0/mode1；这与用户报告的“只见技能、
模型缺失”具有直接的包级解释。该候选只证明缺失投影并补齐恢复路径，不把客户端
`connecting` 状态或实机可见性宣称为已验收。

## 候选行为

现有的 `currentFiendWarReadyEncounterParty` 继续作为唯一入口门禁：同一 encounter/run、
相同参与者与 party instance、每名成员当前绑定代次、ready 场景和 party run key
均须一致。ACK 与 op153/op9 之后，成功分支调用
`projectDungeonPartyPeerActors(p, "fiendwar_existing_encounter_request")`。

投影沿用现有 per-binding claim：方向已送达时重复请求不再重发；某一方向写失败时现有
helper 释放该方向记录，下一次通过同一门禁的请求可以续办。ACK 已经成功后，投影失败只
记录 `game-fiendwar-existing-party-peer-actor-failed` 并将原因标为
`existing_party_peer_actor_write_failed`，不会把已经成功的 ACK 扩大成会话错误。

本候选不写 op7/op8/op11，不修改 invitation、membership、run、scene、participants、
奖励或资产。`currentFiendWarExistingEncounterParty` 的跨 run 冻结保持不变，也没有新增
跨 run 新建或合并组队能力。

## 专属回归

`r169_fiendwar_existing_encounter_peer_actor_test.go` 使用真实 encounter 入场和 ready
fixture：

1. 清空两侧模型 claim 后发起已有 encounter 请求，验证双方都收到目标角色的
   mode0/mode1/op9；再次请求时不再发送 mode0/mode1，且始终没有 op7/op8/op11。
2. 改变一方的 party run key，沿用真实恢复入口，验证失败 ACK、两侧均无模型投影、
   没有 claim、没有成员或奖励变更。
3. 在发送方第一条 actor mode1 上注入写失败，验证写失败未逃逸、未完成方向没有残留
   claim；恢复连接后再次请求可补齐双向模型，随后第三次请求仍保持幂等。

三项测试均在真实 `r159ActiveEncounter` fixture 上运行入口、ready 检查、roster 和投影
路径。写入 party_accept.go 的行为由这些测试约束，测试未运行是因为本轮明确禁止
Go、构建和串行测试。

## 验收边界

本候选只覆盖“已准入、同 encounter、全员 ready”的恢复。它不能证明先前 op11 的 P2P
端点、UDP relay 或客户端所选中继本身可用，也不能证明实机 `connecting` 已消失。跨 run
新组队、场景/奖励迁移与跨 run 成员合并仍是独立未实现范围。全部修复完成后统一部署；
部署前需由主线程串行执行定向测试，并在真实客户端记录两侧恢复前后的模型与连接状态。
