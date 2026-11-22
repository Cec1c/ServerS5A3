package dnfbridge

import (
	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/party"
)

// 本文件是「组队操作的线格式收尾」的唯一收口点。
//
// # 为什么要有它
//
// 三个入队入口（邀请接受 op11 / 直接入队 op705 / 目录入队 op90）此前各自抄了一遍
// 同样的收尾序列：
//
//	同步普通队伍的攻坚分组 → 下发权威队伍快照 →（可选补一次权威名册）→ 刷新城镇同屏
//
// 差异只有三处，而且都是顺序敏感的真实差异（不是历史包袱）：
//   - 目录入队要在"同步之后、快照之前"回 op90 ACK；
//   - 直接入队在快照之前就要回 op705 ACK（因此 ACK 留在调用方）；
//   - 邀请接受要在快照之后、同屏刷新之前，给被邀请方补一次 op153+op9 权威名册。
//
// 2026-11-16 的现场已经证明，把"同一个操作"拆在三个入口里各自维护会出事：那次的
// 入队前清理只挂到了其中两个入口，第三个入口的行为就和另外两个不一致了。所以这里
// 把它收成一个函数，差异只允许通过下面两个显式钩子表达。
//
// # 约束
//
//   - **字节层面与改动前逐字节相同**：本文件不新增/删除任何 op9/op153/op11 帧；
//   - 钩子只允许放"调用方本来就要发的帧"，不允许在这里做状态判断；
//   - 权威状态仍然只由 party.RuntimePartyManager 持有（见 party_manager_runtime.go）。

// partyJoinFinish 描述一次入队成功之后要做的收尾。
type partyJoinFinish struct {
	// State 是权威快照投影出的收包者视角状态（publishRuntimePartySnapshot 的返回值）。
	State alignedcmd.PartyState
	// Party 是管理器返回的权威快照（含固定槽位）。
	Party party.RuntimePartySnapshot
	// Source 是日志用的来源标记，同时决定 refreshTownPartyActorsAfterJoin 的 source。
	Source string
	// Joiner 是本次入队的人，Anchor 是目标队伍一侧（只用于日志）。
	Joiner *gameSession
	Anchor *gameSession
	// Prior 是入队者原先那支队伍的迁移结果（没有旧队伍时为 nil）。
	Prior *party.RuntimePartyResult
	// AfterSynchronize 在 raid 组同步之后、权威快照之前执行（目录入队的 op90 ACK）。
	AfterSynchronize func() error
	// AfterSnapshot 在权威快照之后、城镇同屏刷新之前执行（邀请接受的补发名册）。
	// 返回 error 时收尾中止，与改动前"补发失败即返回"的顺序一致。
	AfterSnapshot func() error
}

// finishManagedPartyJoin 是三个入队入口唯一的收尾实现。
func (s *Service) finishManagedPartyJoin(f partyJoinFinish) error {
	if s == nil || f.Joiner == nil {
		return nil
	}
	// 0. 先在发包之前落一行操作快照，日志顺序读起来就是"操作 → 帧序列"。
	s.logPartyOperation(f.Joiner, "join", f.Source, priorPartyState(f.Joiner, f.Prior), f.State,
		"anchor_char_id", selectedCharacterID(f.Anchor))
	// 1. 普通队伍的攻坚分组投影（返回值在改动前也被忽略，保持原样）。
	s.synchronizeOrdinaryPartyRaidGroups(f.State, f.Source, f.Party)
	// 2. 目录入队的 ACK 位置：同步之后、快照之前。
	if f.AfterSynchronize != nil {
		if err := f.AfterSynchronize(); err != nil {
			return err
		}
	}
	// 3. 先创建城镇同屏 CUser*（mode0/mode1 外观），再发 op11：
	//    op11 先到会让客户端在 CUser* 还没建好时就开始握手 → "连接中.."。
	s.refreshTownPartyActorsAfterJoin(f.State, f.Source+"_pre_snapshot")
	// 4. 权威快照（op11/op153/op9，顺序由 partyFormationOrder 决定）。
	if err := s.sendManagedRuntimePartySnapshots(f.State); err != nil {
		return err
	}
	// 4. 邀请接受的补发名册（op153+op9，不带 op11 握手）。
	if f.AfterSnapshot != nil {
		if err := f.AfterSnapshot(); err != nil {
			return err
		}
	}
	// 5. 城镇同屏 actor 刷新（多人队伍才动作）。
	s.refreshTownPartyActorsAfterJoin(f.State, f.Source)
	return nil
}

// priorPartyState 从入队结果里取出"入队者原来那支队伍"在他自己视角的状态。
// 没有旧队伍（或没有旧快照）时返回零值，与改动前不带 prior 字段的日志一致。
func priorPartyState(joiner *gameSession, prior *party.RuntimePartyResult) alignedcmd.PartyState {
	if prior == nil || prior.Previous == nil || joiner == nil {
		return alignedcmd.PartyState{}
	}
	return prior.Previous.StateFor(joiner.selectedCharacterID)
}

// partyRecordOwnerChain 返回某个接收者要投影的两套 op9 记录 owner：
//
//	副本作用域 owner = 0（currentSceneObjectContext）
//	城镇作用域 owner = 该连接当前的客户端频道
//
// townFirst 决定先发哪一套：**先后顺序是线格式的一部分**，不能统一——副本内退图
// 路径必须先发城镇作用域（先建城镇记录再绑），而成员移除广播先发副本作用域。
// 这里把"两套 owner 是什么"收成一个来源，顺序由调用方按自己的路径声明。
func partyRecordOwnerChain(receiver *gameSession, townFirst bool) []byte {
	if receiver == nil {
		return []byte{currentSceneObjectContext}
	}
	channel := currentClientSelectedChannel(receiver)
	if channel == currentSceneObjectContext {
		return []byte{currentSceneObjectContext}
	}
	if townFirst {
		return []byte{channel, currentSceneObjectContext}
	}
	return []byte{currentSceneObjectContext, channel}
}

// logPartyOperation 记录"一次队伍操作把权威状态改成了什么"。
//
// 这是唯一一条把 op 名、切换前后状态、成员列表放在同一行的日志：现场出问题时，
// 先看这一行对不对，再看它后面跟的那串 op9/op153 帧对不对，就能分清是
// ① 状态迁移错了，还是 ② 投影错了——不用再靠 110 个文件里翻。
func (s *Service) logPartyOperation(session *gameSession, op string, cause string, before, after alignedcmd.PartyState, extra ...any) {
	if s == nil || session == nil {
		return
	}
	fields := make([]any, 0, 16+len(extra))
	fields = append(fields,
		"op", op,
		"cause", cause,
		"char_id", selectedCharacterID(session),
		"party_id_before", before.PartyID,
		"leader_before", before.UserID,
		"members_before", len(runtimePartyMembers(before)),
		"members_before_ids", runtimePartyMemberIDs(before),
		"party_id_after", after.PartyID,
		"leader_after", after.UserID,
		"members_after", len(runtimePartyMembers(after)),
		"members_after_ids", runtimePartyMemberIDs(after),
	)
	fields = append(fields, extra...)
	s.logGameEvent(session, "game-party-operation", fields...)
}

// runtimePartyMemberIDs 是日志用的成员 id 列表（按固定槽位顺序，0 槽在前）。
func runtimePartyMemberIDs(state alignedcmd.PartyState) []uint16 {
	members := runtimePartyMembers(state)
	ids := make([]uint16, 0, len(members))
	for _, member := range members {
		if member.UserID != 0 {
			ids = append(ids, member.UserID)
		}
	}
	return ids
}
