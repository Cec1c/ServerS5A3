package dnfbridge

import (
	"context"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// A channel connection and its roster flags belong to a login flow, not to
// every character on the account. Re-read the selected owner's completed
// entry quest before either selection route can bind it or publish a scene.
// The roster projection already owns PVF quest IDs and completion semantics;
// use that same read-only policy, never a flag retained from another role.
func (s *Service) currentLukeChannelSelectionAllowed(ctx context.Context, session *gameSession, character dnfrepo.CharacterRecord) bool {
	if s == nil || session == nil {
		return false
	}
	if s.options.currentClientVersion != "official-24.9" || session.channel.Type != dnfenum.HiddenRaidType {
		return true
	}
	if character.CharacterID == "" || character.AccountID != s.accountIDForSession(session) {
		return false
	}
	permissions := s.currentRosterSpecialChannelPermissions(ctx, []dnfrepo.CharacterRecord{character})
	return permissions[character.CharacterID].Luke
}

func (s *Service) rejectCurrentLukeChannelSelection(session *gameSession, character dnfrepo.CharacterRecord) error {
	s.logGameEvent(session, "game-luke-channel-select-rejected",
		"character_id", character.CharacterID,
		"channel_id", session.channel.ID,
		"reason", "selected_character_entry_quest_incomplete_or_unavailable")
	// Reuse the native op7 selector transition, so refusal leaves an operable
	// selector instead of binding the denied character or silently stalling.
	return s.handleUpperReturnSelectCharacter(session, nil)
}
