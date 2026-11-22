package dnfbridge

import (
	"errors"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/party"
)

// An explicit personal giveup or leave-party request leaves the party after
// the town scene is confirmed, including after completion. Result-page team
// returns and automatic deadline returns keep their separate policies.
// The remaining members retain their dungeon runtime and shared instance.
func (s *Service) leaveRuntimePartyOnMemberGiveup(session *gameSession, instance *dungeonPartyInstance) error {
	_, err := s.leaveRuntimePartyDungeonMember(session, instance, "member_giveup_confirmed_town", true)
	return err
}

// A disconnect uses the same retained-instance membership transition as a
// confirmed personal return, but never writes to the socket being retired.
func (s *Service) leaveRuntimePartyDungeonMember(session *gameSession, instance *dungeonPartyInstance, source string, notifyDeparting bool) (bool, error) {
	identity, bound := s.boundGameSessionCharacterSnapshot(session)
	if !bound || !s.boundGameSessionCharacterCurrent(identity) || instance == nil {
		return false, nil
	}
	manager := s.runtimePartyManagerForService()
	before, found := manager.SnapshotByUser(identity.character, identity.generation)
	drifted := !found
	if drifted {
		// Only the current authoritative socket may repair recorded generation
		// drift. An old dungeon return must never remove a new party membership.
		if !s.boundGameSessionCharacterCurrent(identity) {
			return false, nil
		}
		before, found = manager.SnapshotByUser(identity.character, 0)
	}
	if !found || before.ID != instance.key.PartyID {
		return false, nil
	}
	peers := dungeonPartyInstanceMemberSessions(instance, identity.character)
	var next alignedcmd.PartyState
	var removed bool
	if s.options.currentClientVersion == "official-24.9" {
		// Preserve the active instance's identity, slots and surviving lobby.
		// A departing leader transfers ownership without retiring that instance.
		var recordedGeneration uint64
		for _, member := range before.Members {
			if member.UserID == identity.character {
				recordedGeneration = member.SessionGeneration
			}
		}
		var result party.RuntimePartyResult
		if !s.withCurrentDungeonPartyRetirement(identity, instance, func() bool {
			result = manager.LeaveDungeonExpected(identity.character, recordedGeneration, before.ID)
			return result.OK
		}) {
			return false, nil
		}
		removed = result.OK
		if removed {
			s.logRuntimePartyMembershipRemoved(source, identity.character, identity.generation, before, result)
			s.clearRuntimePartyProjection(identity.character, identity.generation)
			if result.Disbanded || result.Party.ID == 0 {
				for _, member := range before.Members {
					if member.UserID != 0 && member.UserID != identity.character {
						s.clearRuntimePartyProjection(member.UserID, member.SessionGeneration)
					}
				}
			}
			next = s.publishRuntimePartySnapshot(result.Party)
		}
	} else if drifted {
		next, _, removed = s.repairManagedRuntimePartyLeave(identity)
	} else {
		next, _, removed = s.leaveManagedRuntimeParty(identity, source)
	}
	if !removed {
		return false, nil
	}
	instance.mu.Lock()
	for _, member := range before.Members {
		if member.Slot < 8 {
			instance.retiredPartyRecordKeys[member.Slot] = member.UserID
		}
	}
	instance.mu.Unlock()
	s.partyDungeonLoadingBarrierForceRelease(before.ID, "member_left_party")
	var errs error
	if s.options.currentClientVersion != "official-24.9" {
		for _, peer := range peers {
			body := buildCurrentSceneOp9ActorRemovalBodyInContext(currentSceneActorObjectKey(identity.character), currentSceneObjectContext, s.currentSceneActorRoute())
			errs = errors.Join(errs, s.sendGameUpperRawClass(peer.Session, uint16(dnfenum.CmdPacketRecoverStamina), body, 0))
		}
	}
	if len(runtimePartyMembers(next)) < 2 {
		s.closeRuntimePartyUDPRelay(int(before.ID))
	} else {
		// 严禁在副本内重建中继：CloseParty 会掐断幸存者已建立的 UDP 通道。
		s.syncRuntimePartyUDPRelay(next)
	}
	beforeLeaderState := before.StateFor(before.Leader)
	beforeMembers := runtimePartyMembers(beforeLeaderState)
	instance.mu.Lock()
	for _, member := range before.Members {
		if member.UserID != 0 {
			instance.retiredMembers = append(instance.retiredMembers, member.UserID)
		}
	}
	if identity.character != 0 {
		instance.retiredMembers = append(instance.retiredMembers, identity.character)
	}
	retiredIDs := append([]uint16(nil), instance.retiredMembers...)
	instance.mu.Unlock()
	seenBeforeMember := make(map[uint16]bool, len(beforeMembers)+len(retiredIDs))
	for _, member := range beforeMembers {
		if member.UserID != 0 {
			seenBeforeMember[member.UserID] = true
		}
	}
	for _, retiredID := range retiredIDs {
		if retiredID != 0 && !seenBeforeMember[retiredID] {
			seenBeforeMember[retiredID] = true
			beforeMembers = append(beforeMembers, alignedcmd.PartyMemberState{UserID: retiredID})
		}
	}
	beforeLeaderState.MaxMembers = 4
	beforeLeaderState.Members = beforeMembers
	if notifyDeparting {
		s.markFiendWarPartyClearOnTownSceneReady(session)
		errs = errors.Join(errs, s.sendRuntimePartyDeparture(session, beforeLeaderState))
	}
	if s.options.currentClientVersion == "official-24.9" {
		if len(runtimePartyMembers(next)) == 0 {
			// 2 -> 1 解散时 next.Members 为空，必须按 before 快照对仍留在图内/在线的幸存者下发完整 kind3 清场与离场移除
			for _, member := range before.Members {
				if member.UserID == 0 || member.UserID == identity.character {
					continue
				}
				peerIdentity, ok := s.onlineGameSessionCharacterSnapshot(member.UserID)
				if !ok || peerIdentity.session == nil {
					continue
				}
				peerSession := peerIdentity.session
				inDungeon := false
				for _, peer := range peers {
					if peer.CharacterID == member.UserID && peer.Session == peerSession {
						inDungeon = true
						break
					}
				}
				if inDungeon {
					errs = errors.Join(errs, s.clearDungeonSurvivorTownPartyRecords(peerSession, beforeMembers, source))
					errs = errors.Join(errs, s.clearDisbandedDungeonPartyRecords(peerSession, beforeMembers, identity.character, false, source))
					errs = errors.Join(errs, s.sendRuntimePartyMemberRemoved(peerSession, identity.character, alignedcmd.PartyState{}))
					errs = errors.Join(errs, s.sendDungeonDepartedActorRemoval(peerSession, identity.character, source))
				} else {
					s.markFiendWarPartyClearOnTownSceneReady(peerSession)
					errs = errors.Join(errs, s.sendRuntimePartyDeparture(peerSession, before.StateFor(member.UserID)))
				}
			}
			if dungeonPartyRunKeyTownResetEnabled() {
				memberIDs := make([]uint16, 0, len(beforeMembers))
				for _, member := range beforeMembers {
					if member.UserID != 0 {
						memberIDs = append(memberIDs, member.UserID)
					}
				}
				s.forgetRuntimePartyRunKeysOnDisband(memberIDs, before.ID, source)
			}
		} else {
			// 把"本次退图的角色"登记到每个幸存者的绑定账本：回城后 session.dungeon.runtime
			// 会被摘除，届时已经无法再推出 D 是谁。
			aliasIDs := make([]uint16, 0, len(beforeMembers)+1)
			for _, m := range beforeMembers {
				if m.UserID != 0 {
					aliasIDs = append(aliasIDs, m.UserID)
				}
			}
			for _, m := range runtimePartyMembers(next) {
				if peerSession, ok := s.onlineGameSession(m.UserID); ok && peerSession != nil {
					s.recordClientPartyAliasKeys(peerSession, aliasIDs, false)
				}
			}
			errs = errors.Join(errs, s.sendMemberGiveupSurvivorRosterAfterDeparture(next, instance, identity.character))
		}
	} else {
		errs = errors.Join(errs, s.broadcastRuntimePartyMemberRemoved(beforeLeaderState, next, identity.character, session))
	}
	s.logGameEvent(session, "game-party-member-giveup-left", "char_id", identity.character,
		"party_id", before.ID, "leader_char_id", before.Leader, "remaining_members", len(next.Members), "source", source)
	return true, errs
}

// A survivor can still be bound to a pre-entry selected-channel party record.
// Update that record and owner-0 dungeon records with the same retained roster.
func (s *Service) sendMemberGiveupSurvivorRoster(state alignedcmd.PartyState, instance *dungeonPartyInstance) error {
	return s.sendMemberGiveupSurvivorRosterAfterDeparture(state, instance, 0)
}

func (s *Service) sendMemberGiveupSurvivorRosterAfterDeparture(state alignedcmd.PartyState, instance *dungeonPartyInstance, departingID uint16) error {
	var errs error
	members := runtimePartyMembers(state)
	// The return caller holds its own dungeon lock. Never acquire a peer's
	// session lock: simultaneous returns would acquire these in opposite order.
	peers := dungeonPartyInstanceMemberSessions(instance, 0)
	for _, member := range members {
		identity, ok := s.onlineGameSessionCharacterSnapshot(member.UserID)
		if !ok {
			continue
		}
		receiver := identity.session
		current := s.currentPartyRetirementGuard(receiver, &state)
		if !current() {
			continue
		}
		active := false
		for _, peer := range peers {
			if peer.CharacterID == identity.character && peer.Session == receiver {
				active = true
				break
			}
		}
		if !active {
			errs = errors.Join(errs, s.sendRuntimePartyMemberRemoved(receiver, 0, state))
			continue
		}
		if departingID != 0 && len(members) == 1 {
			// Updating an active roster directly from two rows to one does not
			// retire the native party HUD/network's previous peer entries. This
			// is visible when a four-player party shrinks to its last fixed slot.
			// There are no surviving remote actors to preserve now: retire the
			// local client binding before restoring its server-owned singleton.
			// Keep the manager, fixed slot and dungeon instance unchanged.
			sent, err := s.resetDungeonSingletonPartyDisplay(receiver, state, instance, departingID, current)
			if err != nil || !sent {
				errs = errors.Join(errs, err)
				continue
			}
		}
		// owner 记录链统一由 partyRecordOwnerChain 给出：城镇作用域优先（先建城镇记录）。
		owners := partyRecordOwnerChain(receiver, true)
		complete := true
		for _, owner := range owners {
			sent, err := s.sendMemberGiveupSurvivorRosterInContext(receiver, state, departingID, owner, current)
			if err != nil || !sent {
				errs = errors.Join(errs, err)
				complete = false
				break
			}
		}
		if !complete {
			continue
		}
		// NOTI9 diffs the old manager slots to retire missing network peers.
		// NOTI6 clears those slots before erasing the actor. Sending it first
		// loses the only identity that the roster diff can use for removal.
		// Keep each receiver's actor until its complete roster has been sent.
		if departingID != 0 && current() {
			errs = errors.Join(errs, s.sendDungeonDepartedActorRemoval(receiver, departingID, "member_giveup_confirmed_town", state))
		}
		_, err := s.sendRetiredPartyPacket(receiver, 0x0099, runtimePartyRealtimeInfoBody(state), &state, current, false)
		errs = errors.Join(errs, err)
	}
	return errs
}

func (s *Service) resetDungeonSingletonPartyDisplay(receiver *gameSession, state alignedcmd.PartyState, instance *dungeonPartyInstance, departingID uint16, current func() bool) (bool, error) {
	if s.options.currentClientVersion != "official-24.9" || len(runtimePartyMembers(state)) != 1 {
		return false, nil
	}
	// owner 记录链统一由 partyRecordOwnerChain 给出：城镇作用域优先（先建城镇记录）。
	owners := partyRecordOwnerChain(receiver, true)
	instance.mu.Lock()
	keys := instance.retiredPartyRecordKeys
	instance.mu.Unlock()
	ordered := []uint16{selectedCharacterID(receiver), departingID}
	for _, key := range keys {
		seen := key == 0
		for _, old := range ordered {
			seen = seen || old == key
		}
		if !seen {
			ordered = append(ordered, key)
		}
	}
	for _, key := range ordered {
		for _, owner := range owners {
			// The paired kind3 retirement helper preserves the living local
			// drawable. Empty kind2 would run the destructive ordinary slot setter.
			sent, err := s.sendRetiredPartyPacket(receiver, 9,
				buildCurrentSceneOp9ActorRemovalBodyInContext(currentSceneActorObjectKey(key), owner, s.currentSceneActorRoute()), &state, current, false)
			if err != nil || !sent {
				return false, err
			}
		}
	}
	return s.sendRetiredPartyPacket(receiver, 0x99, runtimePartyRealtimeInfoBody(alignedcmd.PartyState{}), &state, current, false)
}

func (s *Service) sendMemberGiveupSurvivorRosterInContext(receiver *gameSession, state alignedcmd.PartyState, departingID uint16, owner byte, current func() bool) (bool, error) {
	selection := currentPartyFrameOwnerSelection{Mode: currentPartyFrameOwnerExplicit, Owner: owner}
	if departingID != 0 && owner == currentSceneObjectContext {
		// 仅在副本作用域(owner=0)下给退图者记录发 kind2（保留同批留存者名单），
		// 避免 kind3 清空存活者的 [dungeon_actor+0x4C8]；绝不在城镇作用域(owner=ch)下投影
		// departingID —— 那会在城镇作用域新建 record(D,ch) 抢走存活者的 [localPlayer+0x4C8]。
		sent, err := s.sendCurrentPartyRecordProjectionInContext(receiver, receiver, state, "member_giveup_retained_record", nil, selection, departingID)
		if err != nil || !sent {
			return false, err
		}
		if !current() {
			return false, nil
		}
	}
	// 先发所有幸存队友（peers），最后发收包者自己（self），确保在 owner=channel 下
	// 原生 SetMemberUser 最终把 [local_actor+0x4C8] 绑回 receiver 自己的记录。
	if owner == currentSceneObjectContext {
		members := runtimePartyMembers(state)
		for _, row := range members {
			if row.UserID == 0 || row.UserID == receiver.selectedCharacterID {
				continue
			}
			actor, found := s.onlineGameSession(row.UserID)
			if !found {
				continue
			}
			sent, err := s.sendCurrentPartyActorFrameProjectionInContext(receiver, actor, state, "member_giveup_survivor_dungeon_roster", nil, selection)
			if err != nil || !sent {
				return false, err
			}
		}
	}
	selfSent, err := s.sendCurrentPartyActorFrameProjectionInContext(receiver, receiver, state, "member_giveup_survivor_dungeon_roster", nil, selection)
	if err != nil || !selfSent {
		return false, err
	}
	return true, nil
}
