package dnfbridge

import (
	"context"
	"encoding/binary"
	"fmt"
	"strconv"
	"strings"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

const currentLukeSelectorProgressMsgID uint16 = 602

// The current native op869 producer writes one u32 sequence ID. Class0/op602
// consumes u32 sequence, u8 sequential cursor, u32 individual-clear bitmask.
// This route intentionally owns only ordinary Luke (42), whose entry authority
// already uses the same character's persisted DungeonPermission.ClearState.
func (s *Service) handleCurrentLukeSelectorProgress(session *gameSession, body []byte) error {
	if len(body) != 4 || binary.LittleEndian.Uint32(body) != uint32(currentLukeNormalSequenceID) {
		s.logGameEvent(session, "game-luke-selector-progress-rejected", "reason", "unsupported_sequence_or_shape", "body_len", len(body))
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	progress, err := s.currentLukeSelectorProgress(ctx, session)
	if err != nil {
		s.logGameEvent(session, "game-luke-selector-progress-rejected", "reason", err)
		return nil
	}
	if err := s.sendGameUpperRawClass(session, currentLukeSelectorProgressMsgID, progress, 0); err != nil {
		return err
	}
	session.lukeSelectorProgressCharacterID = session.selectedCharacterID
	s.logGameEvent(session, "game-luke-selector-progress-sent", "character_id", session.selectedCharacterID,
		"sequence_id", currentLukeNormalSequenceID, "clear_mask", binary.LittleEndian.Uint32(progress[5:]), "source", "character_persisted_clears")
	return nil
}

func (s *Service) currentLukeSelectorProgress(ctx context.Context, session *gameSession) ([]byte, error) {
	if session == nil || session.selectedCharacterID == 0 {
		return nil, fmt.Errorf("Luke progress character required")
	}
	repos, ok := s.repositoryGroupFor(ctx)
	if !ok || repos.Character == nil || repos.DungeonPermission == nil {
		return nil, fmt.Errorf("Luke progress repository unavailable")
	}
	id := strconv.Itoa(int(session.selectedCharacterID))
	character, found, err := repos.Character.Load(ctx, id)
	if err != nil {
		return nil, err
	}
	account := strings.TrimSpace(s.accountIDForSession(session))
	if !found || character.CharacterID != id || account == "" || strings.TrimSpace(character.AccountID) != account {
		return nil, fmt.Errorf("Luke progress character owner mismatch")
	}
	source, err := s.currentDungeonRuleSource()
	if err != nil {
		return nil, err
	}
	sequence, err := loadCurrentLukeNormalSequence(source)
	if err != nil {
		return nil, err
	}
	if len(sequence.Dungeons) == 0 || len(sequence.Dungeons) > 8 {
		return nil, fmt.Errorf("Luke progress sequence exceeds native mask")
	}
	record, found, err := repos.DungeonPermission.Load(ctx, id)
	if err != nil {
		return nil, err
	}
	if found && record.CharacterID != id {
		return nil, fmt.Errorf("Luke progress clear owner mismatch")
	}
	if !found {
		record = dnfrepo.DungeonPermissionRecord{}
	}
	return buildCurrentLukeSelectorProgress(sequence, record), nil
}

func buildCurrentLukeSelectorProgress(sequence currentLukeNormalSequence, record dnfrepo.DungeonPermissionRecord) []byte {
	cleared := make(map[uint32]bool)
	for _, entry := range record.Entries {
		if entry.ClearState != 0 {
			cleared[entry.DungeonID] = true
		}
	}
	var mask uint32
	// Native cursor is the inclusive last enterable index. After clearing a
	// contiguous prefix, its successor must be enterable; the separate mask
	// owns the individual tooltip 0/1 values.
	var cursor byte
	prefix := true
	for i, id := range sequence.Dungeons {
		if cleared[id] {
			mask |= uint32(1) << uint(i)
			if prefix {
				cursor = byte(i + 1)
			}
		} else {
			prefix = false
		}
	}
	if len(sequence.Dungeons) > 0 && int(cursor) >= len(sequence.Dungeons) {
		cursor = byte(len(sequence.Dungeons) - 1)
	}
	body := make([]byte, 9)
	binary.LittleEndian.PutUint32(body, uint32(currentLukeNormalSequenceID))
	body[4] = cursor
	binary.LittleEndian.PutUint32(body[5:], mask)
	return body
}
