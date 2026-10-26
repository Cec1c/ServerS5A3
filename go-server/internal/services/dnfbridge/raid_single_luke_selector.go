package dnfbridge

import (
	"fmt"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/raid"
)

// The current area40 WDM and LukeStatusBoard catalogue use the team dungeon
// identities. Native 01A65380 looks up those exact identities; absent rows are
// constructed as hidden(2) by 01A64C20. Keep the single dungeon as the gameplay
// owner and project only its matching catalogue slot. The catalogue roles come
// from dungeon.lst and etc/raid/luke/luke.etc, not numeric offsets.
func currentSingleLukeSelectorAlias(native uint32) (uint32, bool) {
	switch native {
	case 5323, 5329:
		return 5054, true
	case 5324:
		return 5055, true
	case 5330:
		return 5056, true
	case 5325:
		return 5057, true
	case 5331:
		return 5058, true
	case 5326:
		return 5059, true
	case 5332:
		return 5060, true
	case 5328:
		return 5061, true
	case 5334:
		return 5062, true
	case 5327:
		return 5064, true
	case 5333:
		return 5065, true
	default:
		return 0, false
	}
}

func validateCurrentSingleLukeSelector(b *runtimeRaidBattle) error {
	if b == nil || !b.Single || b.Mode != 2 || b.Phase != 0 || len(b.SingleSequence) != 6 || len(b.Dungeons) != 6 || b.SingleIndex < 0 || b.SingleIndex > 6 {
		return fmt.Errorf("single Luke selector owner unavailable")
	}
	light := [...]uint32{5323, 5324, 5325, 5326, 5327, 5328}
	dark := [...]uint32{5329, 5330, 5331, 5332, 5333, 5334}
	want := light
	if b.SingleSequence[0] == dark[0] {
		want = dark
	}
	if b.PhaseCompleted != (b.SingleIndex == 6) || b.TimedOut && b.PhaseCompleted {
		return fmt.Errorf("single Luke selector completion owner mismatch")
	}
	for i, native := range b.SingleSequence {
		state := byte(1)
		if b.TimedOut {
			state = 2
		} else if i == b.SingleIndex {
			state = 0
		}
		if native != want[i] || b.Dungeons[i].ID != native || b.Dungeons[i].State != state {
			return fmt.Errorf("single Luke selector weekly gate mismatch")
		}
	}
	return nil
}

func currentSingleLukeDungeonProjectionBody(b *runtimeRaidBattle, infection uint32) ([]byte, error) {
	if err := validateCurrentSingleLukeSelector(b); err != nil {
		return nil, err
	}
	// Union of the actual area40 WDM and LukeStatusBoard dungeon catalogues.
	// Explicitly hide every old slot on each projection so a previous team/weekly
	// route cannot remain selectable. These IDs never become clear/reward owners.
	ids := [...]uint32{5054, 5055, 5056, 5057, 5058, 5059, 5060, 5061, 5062, 5063, 5064, 5065, 5066,
		5090, 5091, 5092, 5093, 5094, 5095, 5099, 5300, 5310, 5311, 5312, 5313, 5340, 5341,
		321, 322, 323, 334, 335, 328, 329, 330, 324, 325, 326, 327, 331, 332, 333}
	rows := make([]raid.DungeonState, 0, len(ids)+len(b.Dungeons))
	var current uint32
	if !b.TimedOut && b.SingleIndex < len(b.SingleSequence) {
		current, _ = currentSingleLukeSelectorAlias(b.SingleSequence[b.SingleIndex])
	}
	for _, id := range ids {
		state := byte(2)
		if id == current {
			state = 0
		}
		rows = append(rows, raid.DungeonState{ID: id, State: state})
	}
	rows = append(rows, b.Dungeons...)
	return raid.BuildDungeonStateBody(rows, infection)
}

// Resolve only the currently open slot of a frozen authenticated single-Luke
// owner. The existing op16 guard still rechecks admission clocks, route, account,
// generation and the native dungeon before it creates a runtime or writes assets.
func (s *Service) resolveCurrentSingleLukeSelector(session *gameSession, requested uint32) uint32 {
	if s == nil || session == nil || s.options.currentClientVersion != "official-24.9" || session.channel.Type != dnfenum.HiddenRaidType || !s.currentRaidAuthenticated(session) {
		return requested
	}
	s.raidMu.Lock()
	defer s.raidMu.Unlock()
	state := s.runtimeRaidByMemberLocked(session.selectedCharacterID)
	if state == nil || !state.Started || state.Battle == nil || !state.CurrentLayout || state.RouteOrRaidType != 1 || state.CurrentUIFlags[0] != 1 {
		return requested
	}
	b := state.Battle
	if validateCurrentSingleLukeSelector(b) != nil || b.TimedOut || b.PhaseCompleted || b.ChannelID != session.channel.ID || b.ChannelType != session.channel.Type || b.MemberAccounts[session.selectedCharacterID] != s.accountIDForSession(session) {
		return requested
	}
	native := b.SingleSequence[b.SingleIndex]
	if alias, ok := currentSingleLukeSelectorAlias(native); ok && requested == alias {
		return native
	}
	return requested
}
