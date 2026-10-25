package dnfbridge

import (
	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/raid"
)

// restoreCurrentRaidSelectorProjection republishes only the missing current
// client projection before the first selector acknowledgement. The successful
// 668 path in raid_online.go remains the owner of a requested rejoin; this
// helper mirrors its presentation tail without inventing an ACK or replaying
// 587's start/cinematic transition.
func (s *Service) restoreCurrentRaidSelectorProjection(session *gameSession, source string) error {
	if s == nil || session == nil || s.options.currentClientVersion != "official-24.9" ||
		session.channel.Type != dnfenum.HiddenRaidType || !s.currentRaidAuthenticated(session) {
		return nil
	}
	characterID := selectedCharacterID(session)
	state, found := s.runtimeRaidForMember(characterID)
	if !found || !state.CurrentLayout || !state.Started || state.RouteOrRaidType != 1 || state.Battle == nil ||
		!s.currentRaidInfoVisible(session, state) {
		return nil
	}
	battle := state.Battle
	if battle.Mode != 2 || battle.Single || battle.FiendWar != nil ||
		battle.ChannelID != session.channel.ID || battle.ChannelType != session.channel.Type ||
		battle.MemberAccounts[characterID] == "" ||
		battle.MemberAccounts[characterID] != s.accountIDForSession(session) {
		return nil
	}

	identity := currentRaidProjectionIdentity{
		InstanceID: battle.InstanceID,
		Phase:      battle.Phase,
		CharID:     characterID,
		Generation: session.characterGeneration,
	}
	session.raidProjection.mu.Lock()
	// initialized alone is insufficient: the raid clock may have initialized the
	// progress owner before this connection received its profile/metadata set.
	// metadataReady is recorded only by the existing initial/resubscribe progress
	// tail, whose production callers send metadata immediately beforehand.
	metadataReady := session.raidProjection.metadataReady &&
		session.raidProjection.metadataReadyIdentity == identity
	session.raidProjection.mu.Unlock()
	if metadataReady {
		return nil
	}

	// Keep the verified successful-rejoin tail order: profiles, member roster,
	// mode2 metadata, then a forced full progress snapshot. A background clock
	// may initialize progress first, so the selector-specific marker is recorded
	// only after this complete batch has been written successfully.
	if err := s.sendCurrentRuntimeRaidProfiles(session, state); err != nil {
		return err
	}
	roster, err := s.buildRuntimeRaidRosterBody(state)
	if err != nil {
		return err
	}
	if err := s.sendGameUpperRawClassCodec(session, raid.RaidMemberRefreshMsgID, roster, 0, true); err != nil {
		return err
	}
	if err := s.sendRuntimeRaidMetadata(session, state, 2); err != nil {
		return err
	}
	if err := s.sendCurrentRaidProgressProjection(session, state, false, true); err != nil {
		return err
	}
	s.logGameEvent(session, "game-raid-selector-self-projection-restored",
		"source", source,
		"char_id", characterID,
		"raid_key", state.RaidKey,
		"phase", battle.Phase,
		"dungeon_count", len(battle.Dungeons),
		"wire_order", "profiles_752_roster_591_mode3_metadata_591_mode2_progress_585")
	return nil
}
