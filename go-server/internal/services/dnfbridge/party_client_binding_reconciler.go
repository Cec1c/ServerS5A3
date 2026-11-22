package dnfbridge

// 本文件是"客户端组队绑定协调器"：只负责 DNF.exe 侧 CPartyRecord 绑定([localPlayer+0x4C8])
// 的跟踪与别名记录清理，不承担业务判断。
//
// 客户端铁律（.prsafe 0x06EC5000..0x06EC5373 反汇编结论）：
//   1. 空 kind2 / kind3 只有在 [actor+0x4C8] == this 时才把绑定清零；
//   2. 回城时 op24 重建 localPlayer、[localPlayer+0x4C8] == 0，此时一条 record(self,ch) 的
//      kind0 会由 0x06EC536B 自动绑回（oldUser==newUser 也绑），不需要先发空 kind2；
//   3. 若把同队其他成员的 record(other,ch)（名单含本机）投影给该客户端，0x029738FE 会把
//      [localPlayer+0x4C8] 强行覆写成 record(other,ch)（抢绑）。
// 因此本文件不再发送任何"空 kind2 复位"，只做：登记历史别名 → 需要时用 kind3 清理。

import (
	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
)

// partyClientBindingState 是单个会话的客户端绑定投影账本（受 session.party.mu 保护）。
type partyClientBindingState struct {
	// foreignChannelRecordBound 表示曾出现过"本机记录需要重新绑定"的时机（仅作观测）。
	foreignChannelRecordBound bool
	// retainedAliasKeys 是当前组队周期内向该客户端投影过的非本机角色 ID（如中途退图者）。
	retainedAliasKeys map[uint16]struct{}
}

// recordClientPartyProjectionObserved 在向 receiver 实际发出非空 NOTI 9(kind0/kind2) 后登记。
//
// 只有"名单里包含接收者本人"的非本机城镇记录才登记别名：那类记录才是抢绑源，
// 它们对应的角色（如中途退图者 D）后续才需要用 kind3 静默清理。
// 本机城镇记录（roleID == self 且 owner == ch）说明绑定已回到自己身上，顺带清掉观测标志。
func (s *Service) recordClientPartyProjectionObserved(
	receiver *gameSession,
	recordCharacterID uint16,
	ownerChannel byte,
	localChannelID int,
	projections ...[]currentSceneOp9PartyMemberProjection,
) {
	if s == nil || receiver == nil || recordCharacterID == 0 {
		return
	}
	selfID := receiver.selectedCharacterID
	if selfID == 0 {
		return
	}
	receiver.party.mu.Lock()
	if receiver.party.clientBinding.retainedAliasKeys == nil {
		receiver.party.clientBinding.retainedAliasKeys = make(map[uint16]struct{}, 4)
	}
	if recordCharacterID != selfID && ownerChannel != currentSceneObjectContext {
		for _, batch := range projections {
			if _, listed := currentSceneOp9PartyMemberSlot(batch, selfID); listed {
				receiver.party.clientBinding.retainedAliasKeys[recordCharacterID] = struct{}{}
				break
			}
		}
	}
	if recordCharacterID == selfID && ownerChannel != currentSceneObjectContext {
		receiver.party.clientBinding.foreignChannelRecordBound = false
	}
	receiver.party.mu.Unlock()
}

// recordClientPartyAliasKeys 批量登记该客户端接触过的非本机角色记录键。
func (s *Service) recordClientPartyAliasKeys(receiver *gameSession, characterIDs []uint16, _ bool) {
	if s == nil || receiver == nil || len(characterIDs) == 0 {
		return
	}
	selfID := receiver.selectedCharacterID
	receiver.party.mu.Lock()
	if receiver.party.clientBinding.retainedAliasKeys == nil {
		receiver.party.clientBinding.retainedAliasKeys = make(map[uint16]struct{}, len(characterIDs))
	}
	for _, id := range characterIDs {
		if id != 0 && id != selfID {
			receiver.party.clientBinding.retainedAliasKeys[id] = struct{}{}
		}
	}
	receiver.party.mu.Unlock()
}

// takeForeignChannelRecordHijacked 原子检查并消费"曾被抢绑"标志（仅观测用）。
func takeForeignChannelRecordHijacked(receiver *gameSession) bool {
	if receiver == nil {
		return false
	}
	receiver.party.mu.Lock()
	hijacked := receiver.party.clientBinding.foreignChannelRecordBound
	receiver.party.clientBinding.foreignChannelRecordBound = false
	receiver.party.mu.Unlock()
	return hijacked
}

// consumeNonMemberClientPartyAliasKeys 取出并消费所有已不在当前队伍里的历史别名键。
func consumeNonMemberClientPartyAliasKeys(receiver *gameSession, activeMembers []alignedcmd.PartyMemberState) []uint16 {
	if receiver == nil {
		return nil
	}
	selfID := receiver.selectedCharacterID
	memberSet := make(map[uint16]bool, len(activeMembers)+1)
	if selfID != 0 {
		memberSet[selfID] = true
	}
	for _, m := range activeMembers {
		if m.UserID != 0 {
			memberSet[m.UserID] = true
		}
	}
	receiver.party.mu.Lock()
	defer receiver.party.mu.Unlock()
	if len(receiver.party.clientBinding.retainedAliasKeys) == 0 {
		return nil
	}
	var retired []uint16
	for id := range receiver.party.clientBinding.retainedAliasKeys {
		if id == 0 || id == selfID {
			delete(receiver.party.clientBinding.retainedAliasKeys, id)
			continue
		}
		if !memberSet[id] {
			retired = append(retired, id)
			delete(receiver.party.clientBinding.retainedAliasKeys, id)
		}
	}
	return retired
}

// consumeAllClientPartyAliasKeysOnDeparture 在本机彻底离队/被踢时取出全部历史别名键并复位。
func consumeAllClientPartyAliasKeysOnDeparture(receiver *gameSession) []uint16 {
	if receiver == nil {
		return nil
	}
	selfID := receiver.selectedCharacterID
	receiver.party.mu.Lock()
	defer receiver.party.mu.Unlock()
	receiver.party.clientBinding.foreignChannelRecordBound = false
	if len(receiver.party.clientBinding.retainedAliasKeys) == 0 {
		return nil
	}
	out := make([]uint16, 0, len(receiver.party.clientBinding.retainedAliasKeys))
	for id := range receiver.party.clientBinding.retainedAliasKeys {
		if id != 0 && id != selfID {
			out = append(out, id)
		}
	}
	receiver.party.clientBinding.retainedAliasKeys = nil
	return out
}

// resetUnboundSelfChannelRecordIfHijacked 不再发送空 kind2。
//
// 客户端铁律 2：回城时 [localPlayer+0x4C8] 已被 op24 重建清零，随后任意一条
// record(self,ch) 的 kind0 都会由 0x06EC536B 自动绑回；而在城镇里对已绑定的记录发空 kind2
// 反而会命中 0x06EC50BE 把绑定清零 → wasBound1→0 → "已退出队伍" + P2P->closeAll → 重组/连接中。
// 因此这里只消费观测标志，不发任何包。
func (s *Service) resetUnboundSelfChannelRecordIfHijacked(
	_ *gameSession,
	_ alignedcmd.PartyState,
	_ string,
) error {
	return nil
}

// retireDepartedPartyAliasRecordsAfterSelfRebind 用 kind3 静默退役已不在队伍中的历史记录
// （如中途退图者 D）的两套 owner。kind3 在旧码 0x029733B0 里把旧主用户写回，
// 只要 [localPlayer+0x4C8] 指向 record(self,ch)，就不会在销毁别条记录时触发退队。
func (s *Service) retireDepartedPartyAliasRecordsAfterSelfRebind(
	receiver *gameSession,
	state alignedcmd.PartyState,
	source string,
) error {
	if s == nil || receiver == nil || receiver.selectedCharacterID == 0 {
		return nil
	}
	if s.options.currentClientVersion != "official-24.9" {
		return nil
	}
	members := runtimePartyMembers(state)
	retiredIDs := consumeNonMemberClientPartyAliasKeys(receiver, members)
	if len(retiredIDs) == 0 {
		return nil
	}
	route := s.currentSceneActorRoute()
	owners := partyRecordOwnerChain(receiver, false)
	for _, owner := range owners {
		for _, id := range retiredIDs {
			objectKey := currentSceneActorObjectKey(id)
			if objectKey == 0 {
				continue
			}
			body := buildCurrentSceneOp9ActorRemovalBodyInContext(objectKey, owner, route)
			if _, err := s.sendRetiredPartyPacket(
				receiver,
				uint16(dnfenum.CmdPacketRecoverStamina),
				body,
				&state,
				nil,
				false,
			); err != nil {
				return err
			}
		}
	}
	s.logGameEvent(receiver, "game-party-client-departed-alias-records-retired",
		"source", source,
		"char_id", receiver.selectedCharacterID,
		"retired_alias_ids", retiredIDs,
		"party_id", state.PartyID,
		"reason", "retire_mid_dungeon_departed_member_records_after_self_channel_record_is_rebound")
	return nil
}
