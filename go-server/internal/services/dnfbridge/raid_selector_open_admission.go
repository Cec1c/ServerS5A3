package dnfbridge

import "longheng.io/server/internal/modules/dnf/dnfenum"

// Opening the selector requires a committed raid start, independently of
// whether a particular dungeon is currently open. The map-entry guard still
// owns phase, deadline and dungeon availability checks.
func (s *Service) currentRaidSelectorOpenAdmission(session *gameSession) (bool, string) {
	if s == nil || s.options.currentClientVersion != "official-24.9" || session == nil ||
		(session.channel.Type != dnfenum.RaidChannelType && session.channel.Type != dnfenum.HiddenRaidType) {
		return true, "outside_anton_luke_selector_scope"
	}
	if !s.currentRaidAuthenticated(session) {
		return false, "raid_selector_owner_unavailable"
	}
	s.raidMu.Lock()
	defer s.raidMu.Unlock()
	state := s.runtimeRaidByMemberLocked(session.selectedCharacterID)
	if state == nil {
		return false, "raid_selector_membership_missing"
	}
	if !state.Started {
		return false, "raid_selector_not_started"
	}
	if state.Battle == nil {
		return false, "raid_selector_battle_missing"
	}
	b := state.Battle
	if b.ChannelID != session.channel.ID || b.ChannelType != session.channel.Type ||
		b.MemberAccounts[session.selectedCharacterID] != s.accountIDForSession(session) {
		return false, "raid_selector_admission_owner_mismatch"
	}
	return true, "raid_selector_started_owner"
}
