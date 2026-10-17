package dnfbridge

import (
	"context"
	"errors"
	"fmt"

	"golang.org/x/text/encoding/simplifiedchinese"
	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

var errDungeonMapQuestRequired = errors.New("dungeon map unlock quest is incomplete")

// Preserve the failing identity through wrapped preparation errors. The
// current client's 0xad message also depends on its local entry counter, so
// a roster slot alone cannot reliably express a missing unlock quest.
type dungeonMapQuestRequiredError struct {
	characterID uint16
	dungeonID   int64
	questID     int64
}

func (e *dungeonMapQuestRequiredError) Error() string {
	return fmt.Sprintf("%s: character=%d dungeon=%d quest=%d", errDungeonMapQuestRequired, e.characterID, e.dungeonID, e.questID)
}

func (e *dungeonMapQuestRequiredError) Unwrap() error { return errDungeonMapQuestRequired }

func (s *Service) sendCurrentRiftUnlockFailure(session *gameSession, err error) error {
	var failure *dungeonMapQuestRequiredError
	if !errors.As(err, &failure) || !s.currentRiftEntryEnabled(failure.dungeonID) {
		return s.sendSelectDungeonFailure(session, currentSelectDungeonMissingPermissionReason)
	}
	// Reuse the established failure ACK and native system-text channel. 0x13
	// does not select a possibly unrelated party member's entry-limit popup.
	if sendErr := s.sendSelectDungeonFailure(session, currentSelectDungeonInvalidSelectionStateReason); sendErr != nil {
		return sendErr
	}
	message := "队伍中有角色尚未完成魔界裂缝的入场前置任务，无法进入。"
	if failure.characterID == sessionSelectedCharacterID(session) {
		message = "尚未完成魔界裂缝的入场前置任务，无法进入。"
	}
	encoded, encodeErr := simplifiedchinese.GBK.NewEncoder().Bytes([]byte(message))
	if encodeErr != nil {
		return encodeErr
	}
	var writer packetWriter
	writer.writeByte(0)
	writer.writeUint16(0xffff)
	writer.writeByte(0)
	writer.writeRawDstr(encoded)
	writer.writeRawDstr(nil)
	return s.sendGameUpperRawClass(session, currentAreaChatNotifyOpcode, writer.bytes(), 0)
}

// Scope is the current client's particular-hell map. Other worldmap marker
// forms need their own evidence before generalising this admission rule.
// The quest ID comes from the PVF's ordinary dungeon/quest pair, not hell quest.
func (s *Service) validateCurrentRiftUnlock(ctx context.Context, session *gameSession, dungeon worldmap.Dungeon) error {
	if !s.currentRiftEntryEnabled(dungeon.ID) {
		return nil
	}
	table, _, err := s.dungeonWorldMap()
	if err != nil {
		return err
	}
	area, ok := table.FindAreaByDungeon(dungeon.ID)
	if !ok {
		return fmt.Errorf("rift unlock worldmap unavailable")
	}
	required := int64(0)
	for _, entry := range area.Dungeons {
		if entry.DungeonID != dungeon.ID || entry.InProgressOnly {
			continue
		}
		if entry.QuestID <= 0 || required != 0 {
			return fmt.Errorf("rift unlock quest definition unavailable or ambiguous")
		}
		required = entry.QuestID
	}
	if required == 0 {
		return fmt.Errorf("rift unlock quest definition missing")
	}
	id := sessionSelectedCharacterID(session)
	if id == 0 {
		return errDungeonCharacterRequired
	}
	record, _, err := s.dungeonQuestRecord(ctx, id)
	if err != nil {
		return err
	}
	_, cleared := dungeonQuestStateSets(record)
	if _, ok := cleared[required]; !ok {
		return &dungeonMapQuestRequiredError{characterID: id, dungeonID: dungeon.ID, questID: required}
	}
	return nil
}

// Read the authoritative roster before any member's prepare/asset phase. Each
// prepare repeats its own check, including direct and leader-planned entry.
func (s *Service) validateCurrentRiftPartyUnlock(ctx context.Context, session *gameSession, state alignedcmd.PartyState, dungeonID int64) error {
	if !s.currentRiftEntryEnabled(dungeonID) {
		return nil
	}
	table, _, err := s.dungeonWorldMap()
	if err != nil {
		return err
	}
	d, ok := table.FindDungeon(dungeonID)
	if !ok {
		return worldmap.ErrDungeonNotIndexed
	}
	if err := s.validateCurrentRiftUnlock(ctx, session, d); err != nil {
		return err
	}
	if state.PartyID <= 0 || state.UserID != sessionSelectedCharacterID(session) {
		return nil
	}
	for _, member := range runtimePartyMembers(state) {
		if member.UserID == 0 || member.UserID == sessionSelectedCharacterID(session) {
			continue
		}
		// Only the selected character ID is needed; no per-session unlock latch.
		if err := s.validateCurrentRiftUnlock(ctx, &gameSession{selectedCharacterID: member.UserID}, d); err != nil {
			return err
		}
	}
	return nil
}
