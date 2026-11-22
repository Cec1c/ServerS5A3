package dnfbridge

import (
	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
)

// The matched official client's NOTI9 kind2 reads memberCount directly after
// the ten-byte header, then four control bytes. Kind3 additionally announces
// DSTR 627 (disband); ordinary departures must use the empty kind2 roster.
func (s *Service) buildCurrentPartyDepartureBody(characterID uint16, owner byte) []byte {
	body := buildCurrentSceneOp9ActorRemovalBodyInContext(currentSceneActorObjectKey(characterID), owner, s.currentSceneActorRoute())
	body[9] = 2
	return append(body, 0, 0, 0, 0, 0)
}

func (s *Service) sendRuntimePartyDeparture(receiver *gameSession, previous alignedcmd.PartyState) error {
	if s.options.currentClientVersion != "official-24.9" {
		return s.sendRuntimePartyMemberRemoved(receiver, selectedCharacterID(receiver), alignedcmd.PartyState{})
	}
	identity, bound := s.boundGameSessionCharacterSnapshot(receiver)
	if !bound {
		return nil
	}
	current := s.currentPartyRetirementGuard(receiver, nil)
	members := runtimePartyMembers(previous)
	route := s.currentSceneActorRoute()

	// 客户端 .prsafe (0x6EC5000 / 0x6EC50DC) 在处理 kind3 时严格校验
	// cmp [actor+0x4C8], esi，非当前绑定的记录不会误清 [local_actor+0x4C8]，
	// 命中绑定记录时满足 [ebp-0x356]==1 且 [ebp-0x354]==0，从而进入
	// 0x201786B..0x2017D77 执行完整退队清场并释放 CPartyRecord。
	// 顺序必须为：先 owner=0（副本作用域，不依赖当前场景号），后 owner=channel；
	// 每个作用域内先退役所有队友（peers），最后退役自己（self）。
	// 本机彻底离队时，把"曾经投影给他、但已不在队伍里"的历史记录（如中途退图者 D）
	// 一并纳入 kind3 清场集合：他可能正绑在 record(D,ch) 上，不清就退不干净。
	aliasKeys := consumeAllClientPartyAliasKeysOnDeparture(receiver)
	ordered := make([]uint16, 0, len(aliasKeys)+len(members)+len(previous.Members)+1)
	seen := make(map[uint16]bool, len(aliasKeys)+len(members)+len(previous.Members)+1)
	for _, aliasID := range aliasKeys {
		if aliasID != 0 && aliasID != identity.character && !seen[aliasID] {
			seen[aliasID] = true
			ordered = append(ordered, aliasID)
		}
	}
	for _, member := range members {
		if member.UserID != 0 && member.UserID != identity.character && !seen[member.UserID] {
			seen[member.UserID] = true
			ordered = append(ordered, member.UserID)
		}
	}
	for _, member := range previous.Members {
		if member.UserID != 0 && member.UserID != identity.character && !seen[member.UserID] {
			seen[member.UserID] = true
			ordered = append(ordered, member.UserID)
		}
	}
	if identity.character != 0 && !seen[identity.character] {
		seen[identity.character] = true
		ordered = append(ordered, identity.character)
	}

	// owner 记录链统一由 partyRecordOwnerChain 给出：副本作用域优先（先清副本记录）。
	owners := partyRecordOwnerChain(receiver, false)
	for _, owner := range owners {
		for _, id := range ordered {
			if id == 0 {
				continue
			}
			body := buildCurrentSceneOp9ActorRemovalBodyInContext(currentSceneActorObjectKey(id), owner, route)
			sent, err := s.sendRetiredPartyPacket(receiver, uint16(dnfenum.CmdPacketRecoverStamina), body, nil, current, false)
			if err != nil || !sent {
				return err
			}
		}
	}
	_, err := s.sendRetiredPartyPacket(receiver, 0x99, runtimePartyRealtimeInfoBody(alignedcmd.PartyState{}), nil, current, false)
	if err != nil {
		return err
	}
	if s.onlinePlayers == nil {
		return nil
	}
	selfKey := currentSceneActorObjectKey(identity.character)
	for _, peer := range s.onlinePlayers.PeersInSameArea(identity.character) {
		peerIdentity, bound := s.boundGameSessionCharacterSnapshot(peer.Session)
		if !bound {
			continue
		}
		guard := func() bool { return current() && s.boundGameSessionCharacterCurrent(peerIdentity) }
		owner := currentClientSelectedChannel(peer.Session)
		body := buildCurrentSceneOp9ActorRemovalBodyInContext(selfKey, owner, route)
		if _, err := s.sendRetiredPartyPacket(peer.Session, uint16(dnfenum.CmdPacketRecoverStamina), body, nil, guard, false); err != nil {
			return err
		}
	}
	return nil
}
