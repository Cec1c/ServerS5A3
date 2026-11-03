package dnfbridge

import (
	"fmt"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/raid"
)

// Native591 mode2 only updates a cached object (1D720DA); it cannot
// create the joining member's metadata. Mode3 may create a roster-only
// object, which must not become the first own-raid object. Reuse the full
// read-only object reply already used by session restore, before mode3 binds
// own membership and before any 585/587 battle projection is delivered.
func (s *Service) sendNative90AntonAdmissionObject(session *gameSession, state runtimeRaidState) error {
	if !s.uses90NativeClient() || !state.CurrentLayout || state.RouteOrRaidType != 0 {
		return nil
	}
	if !s.currentRaidAuthenticated(session) || runtimeRaidMemberIndex(state, selectedCharacterID(session)) < 0 || !s.currentRaidInfoVisible(session, state) {
		return fmt.Errorf("native Anton admission object owner unavailable")
	}
	body, err := raid.BuildCurrentInfoReplyBody(currentRuntimeRaidMetadata(state), 2, true)
	if err != nil {
		return err
	}
	if err := s.sendCurrentRuntimeRaidProfiles(session, state); err != nil {
		return err
	}
	if err := s.sendGameUpperRawClassCodec(session, uint16(dnfenum.CmdPacketRequestRaidInfo), body, 1, true); err != nil {
		return err
	}
	s.logGameEvent(session, "game-native90-anton-admission-object-sent", "raid_key", state.RaidKey, "char_id", selectedCharacterID(session), "member_count", len(state.Members))
	return nil
}
