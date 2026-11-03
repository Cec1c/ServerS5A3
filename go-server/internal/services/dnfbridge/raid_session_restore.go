package dnfbridge

import (
	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/raid"
)

// A replacement connection retains server membership, but the new client has
// no own-raid object. Progress/584 must wait for the object and roster restore;
// the native participation announcement reads that object's route byte.
func (s *Service) markCurrentRaidSessionRestore(session *gameSession, charID uint16) {
	if s == nil || session == nil || s.options.currentClientVersion != "official-24.9" {
		return
	}
	state, ok := s.runtimeRaidForMember(charID)
	if !ok || !state.Started || state.Battle == nil || state.Battle.FiendWar != nil ||
		!state.CurrentLayout || state.Battle.ChannelID != session.channel.ID ||
		state.Battle.ChannelType != session.channel.Type ||
		state.Battle.MemberAccounts[charID] != s.accountIDForSession(session) {
		return
	}
	session.raidProjection.mu.Lock()
	session.raidProjection.sessionRestorePending = true
	session.raidProjection.mu.Unlock()
}

// Called from the new client's scene/selector event loop. ACK1359 installs an
// idempotent full object before NOTI591 assigns own membership. Mode2 alone
// updates an existing object and cannot create it after a process restart.
func (s *Service) restoreCurrentRaidSessionProjection(session *gameSession, source string) error {
	if s == nil || session == nil || s.options.currentClientVersion != "official-24.9" {
		return nil
	}
	session.raidProjection.mu.Lock()
	defer session.raidProjection.mu.Unlock()
	if !session.raidProjection.sessionRestorePending {
		return nil
	}
	state, ok := s.runtimeRaidForMember(session.selectedCharacterID)
	if !ok || !state.Started || state.Battle == nil || state.Battle.FiendWar != nil ||
		!s.currentRaidAuthenticated(session) || !s.currentRaidInfoVisible(session, state) ||
		state.Battle.MemberAccounts[session.selectedCharacterID] != s.accountIDForSession(session) {
		return nil
	}
	var err error
	state, err = s.latestCurrentRaidProjectionState(session, state)
	if err != nil {
		return err
	}
	reply, err := raid.BuildCurrentInfoReplyBody(currentRuntimeRaidMetadata(state), 2, s.uses90NativeClient())
	if err != nil {
		return err
	}
	roster, err := s.buildRuntimeRaidRosterBody(state)
	if err != nil {
		return err
	}
	if err := s.sendCurrentRuntimeRaidProfiles(session, state); err != nil {
		return err
	}
	if err := s.sendGameUpperRawClassCodec(session, uint16(dnfenum.CmdPacketRequestRaidInfo), reply, 1, true); err != nil {
		return err
	}
	if err := s.sendGameUpperRawClassCodec(session, raid.RaidMemberRefreshMsgID, roster, 0, true); err != nil {
		return err
	}
	if err := s.sendRuntimeRaidMetadata(session, state, 2); err != nil {
		return err
	}
	if err := s.sendCurrentRaidProgressProjectionLocked(session, state, false, true); err != nil {
		return err
	}
	session.raidProjection.sessionRestorePending = false
	s.logGameEvent(session, "game-raid-session-projection-restored", "source", source,
		"raid_key", state.RaidKey, "char_id", session.selectedCharacterID,
		"wire_order", "profiles_752_object_ack1359_roster_591_metadata_591_progress")
	return nil
}
