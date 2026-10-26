package dnfbridge

import (
	"fmt"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/raid"
)

// Current v96's area27 selector queries the existing 210..224 WDM identities.
// Its absent-state constructor is hide(2), so native 5314..5321 rows alone
// cannot expose a slot. These aliases reuse the real Anton WDM/dungeon roles;
// all dungeon topology, admission, settlement and HP owners keep native IDs.
func currentSingleAntonSelectorAlias(native uint32) (uint32, bool) {
	switch native {
	case 5314:
		return 210, true
	case 5315:
		return 212, true
	case 5316:
		return 216, true
	case 5317:
		return 213, true
	case 5318, 5321:
		return 218, true
	case 5319:
		return 219, true
	case 5320:
		return 220, true
	default:
		return 0, false
	}
}

func currentRaidDungeonProjectionBody(b *runtimeRaidBattle, infection uint32) ([]byte, error) {
	if b == nil {
		return nil, fmt.Errorf("raid map projection owner unavailable")
	}
	if b.Single && b.Mode == 2 {
		return currentSingleLukeDungeonProjectionBody(b, infection)
	}
	if !b.Single || b.Mode != 1 {
		return raid.BuildDungeonStateBody(b.Dungeons, infection)
	}
	if _, err := currentSingleAntonHPBody(b); err != nil {
		return nil, err
	}
	if len(b.Dungeons) != len(b.SingleSequence) {
		return nil, fmt.Errorf("single selector native row ownership mismatch")
	}
	ids := []uint32{210, 211, 212, 213, 214, 215, 216, 218, 219, 220, 221, 222, 223, 224}
	rows := make([]raid.DungeonState, 0, len(ids)+len(b.Dungeons))
	for _, id := range ids {
		rows = append(rows, raid.DungeonState{ID: id, State: 2})
	}
	if b.TimedOut && (b.PhaseCompleted || b.SingleIndex == len(b.SingleSequence)) {
		return nil, fmt.Errorf("single selector timeout has a completed owner")
	}
	seen := map[uint32]bool{}
	for index, native := range b.SingleSequence {
		alias, ok := currentSingleAntonSelectorAlias(native)
		if !ok || seen[alias] || b.Dungeons[index].ID != native {
			return nil, fmt.Errorf("single selector weekly mapping unavailable")
		}
		seen[alias] = true
		want := byte(1)
		if b.TimedOut {
			// The phase deadline reducer closes every native gate with state2.
			// Failure must project the same closed owner, with no alias admitted.
			want = 2
		} else if index == b.SingleIndex {
			want = 0
		}
		if b.Dungeons[index].State != want {
			return nil, fmt.Errorf("single selector native gate mismatch")
		}
		if !b.TimedOut && index == b.SingleIndex {
			for i := range rows {
				if rows[i].ID == alias {
					rows[i].State = 0
				}
			}
		}
	}
	rows = append(rows, b.Dungeons...)
	return raid.BuildDungeonStateBody(rows, infection)
}

// Translate only an authenticated frozen single-Anton member. The existing
// entry guard then checks the real open map, clocks and frozen runtime again.
func (s *Service) resolveCurrentSingleAntonSelector(session *gameSession, requested uint32) uint32 {
	if s == nil || session == nil || s.options.currentClientVersion != "official-24.9" || session.channel.Type != dnfenum.RaidChannelType || !s.currentRaidAuthenticated(session) {
		return requested
	}
	s.raidMu.Lock()
	defer s.raidMu.Unlock()
	state := s.runtimeRaidByMemberLocked(session.selectedCharacterID)
	if state == nil || !state.Started || state.Battle == nil {
		return requested
	}
	b := state.Battle
	if !b.Single || b.Mode != 1 || b.ChannelID != session.channel.ID || b.ChannelType != session.channel.Type || b.MemberAccounts[session.selectedCharacterID] != s.accountIDForSession(session) {
		return requested
	}
	for _, native := range b.SingleSequence {
		if alias, ok := currentSingleAntonSelectorAlias(native); ok && alias == requested {
			return native
		}
	}
	return requested
}
