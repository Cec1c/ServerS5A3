package dnfbridge

import (
	"context"
	"os"
	"strings"
)

const partySelectorPreserveActorEnv = "DNFBRIDGE_PARTY_SELECTOR_PRESERVE_TOWN_ACTOR"

func partySelectorPreserveActorEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(partySelectorPreserveActorEnv))) {
	case "off", "0", "false", "legacy":
		return false
	default:
		return true
	}
}

// Opening the selector does not enter a dungeon. Use the same native opening
// sequence as the leader, retaining the follower's town actor, party owner and
// permission cache until the real dungeon entry creates a dungeon actor.
func (s *Service) prepareRuntimePartyFollowerSelectorWithTownActor(ctx context.Context, follower *gameSession, leaderID uint16, partyID int, source string) (bool, error) {
	state := runtimePartyStateSnapshot(follower)
	if state.PartyID != partyID || state.UserID != leaderID {
		return false, nil
	}
	_, _, character, found := s.selectedCharacterForEnter(ctx, follower)
	if err := s.sendGameUpperSuccess(follower, 15, buildEnterSelectDungeonAckBody()); err != nil {
		return false, err
	}
	if err := s.sendGameUpperRawClass(follower, currentFatigueMsgID, buildCurrentFatigueBody(character, found), 0); err != nil {
		return false, err
	}
	if err := s.sendCurrentTownEnterSelectContext(follower, source+"_preserved_town_actor"); err != nil {
		return false, err
	}
	if !follower.enterSelectDungeonContextSent {
		return false, nil
	}
	follower.enterSelectDungeonSent = true
	follower.enterSelectDungeonAckSent = true
	s.logGameEvent(follower, "game-party-enter-select-follower-synchronized", "source", source,
		"leader_char_id", leaderID, "char_id", follower.selectedCharacterID, "party_id", partyID,
		"sequence", "preserve_town_actor_and_party_owner_class1_op15_op36_op27",
		"reason", "selector_open_is_not_dungeon_entry_no_actor_replacement_or_late_permission_reset")
	return true, nil
}
