package dnfbridge

import (
	"context"
	"fmt"

	"longheng.io/server/internal/modules/dnf/dnfenum"
)

// Selecting another dungeon has the same whole-party retirement boundary as
// retry. Otherwise the follower still owns the old boss room and fails the
// same-area selector and fresh-instance entry checks.
func (s *Service) selectOtherDungeonForCompletedParty(leader *gameSession, runtime *runtimeDungeonState) error {
	state := runtimePartyStateSnapshot(leader)
	if state.PartyID <= 0 || len(runtimePartyMembers(state)) < 2 {
		return s.sendEnterSelectDungeonState(leader, "current_exe_op72_completed_select_other_dungeon", false, true)
	}
	if state.UserID != leader.selectedCharacterID {
		return nil
	}
	retired, err := s.retireCompletedPartyForAction(leader, runtime, false)
	if err != nil || !retired {
		return fmt.Errorf("retire completed party for dungeon selector: retired=%t error=%v", retired, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	for _, member := range runtimePartyMembers(state) {
		if member.UserID == leader.selectedCharacterID {
			continue
		}
		peer, online := s.onlineGameSession(member.UserID)
		if !online || peer == nil {
			return fmt.Errorf("selector member %d is offline", member.UserID)
		}
		if err := s.callGameSession(ctx, peer, "completed-party-selector-exit", func() error {
			// Leave the native result window before the ordinary passive op15 /
			// fatigue / op27 transition. The leader already received this ACK.
			return s.sendGameUpperRawClass(peer, uint16(dnfenum.CmdPacketEplpCommand),
				buildCurrentDungeonOp72SuccessBody(1, 1), currentDungeonCardResponseClass)
		}); err != nil {
			return err
		}
	}
	return s.sendEnterSelectDungeonState(leader, "current_exe_op72_completed_select_other_dungeon", false, true)
}
