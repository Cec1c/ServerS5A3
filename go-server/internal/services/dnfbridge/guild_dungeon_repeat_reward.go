package dnfbridge

import (
	"encoding/binary"
	"errors"
	"strconv"

	"longheng.io/server/internal/modules/dnf/guild"
)

// Current client 013A4C10 consumes one u32; 013A3100 renders it as percent.
// Class 1 opcode 742 is the unrelated guild creation permission reply.
const currentGuildDungeonRepeatRewardNotification uint16 = 742

func buildCurrentGuildDungeonRepeatRewardBody(progress uint32) ([]byte, error) {
	if progress > 100 {
		return nil, errors.New("guild dungeon reward progress exceeds 100 percent")
	}
	body := make([]byte, 4)
	binary.LittleEndian.PutUint32(body, progress)
	return body, nil
}

// The client resets the bar on dungeon selection. Restore it after START_MAP,
// using the selected character snapshot already loaded for this admission.
func (s *Service) sendCurrentGuildDungeonRepeatRewardOnEntry(session *gameSession, runtime *runtimeDungeonState) error {
	if !currentGuildDungeon(runtime) {
		return nil
	}
	if session == nil || runtime.guildDungeonClear == nil || runtime.Character.CharacterID != runtime.guildDungeonClear.Entry.Owner.CharacterID || runtime.Character.AccountID != runtime.guildDungeonClear.Entry.Owner.AccountID || runtime.Character.CharacterID != strconv.Itoa(int(session.selectedCharacterID)) || runtime.Character.AccountID != s.accountIDForSession(session) {
		return errCurrentDungeonSettlementStateInvalid
	}
	progress, err := guild.DungeonRepeatRewardProgress(runtime.Character)
	if err != nil {
		return err
	}
	body, err := buildCurrentGuildDungeonRepeatRewardBody(progress)
	if err != nil {
		return err
	}
	return s.sendGameUpperRawClass(session, currentGuildDungeonRepeatRewardNotification, body, 0)
}
