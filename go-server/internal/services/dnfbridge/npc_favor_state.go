package dnfbridge

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// Current client class0/743 is registered at 020327C1 to 02009AB0.
// It clears the prior actor's favor maps, then reads u8 count and rows:
// u32 NPC, u32 absolute points, u8 gifts used, u8 operation-pair count,
// followed by that many (u8 operation, u8 value) pairs. The zero pair count
// preserves an empty operation state without inventing an action permission.
// CMD246 is a separate debug request and is never used as a notification.
const currentNPCFavorStateMsgID uint16 = 743

func buildCurrentNPCFavorState(character dnfrepo.CharacterRecord, now time.Time) ([]byte, error) {
	ids := make([]uint32, 0)
	points := make(map[uint32]uint32)
	for key, value := range character.Stats {
		if !strings.HasPrefix(key, "npc_favor_") || !strings.HasSuffix(key, "_points") {
			continue
		}
		raw := strings.TrimSuffix(strings.TrimPrefix(key, "npc_favor_"), "_points")
		id, err := strconv.ParseUint(raw, 10, 32)
		if err != nil || id == 0 || strconv.FormatUint(id, 10) != raw || value < 0 || value > int64(^uint32(0)) {
			return nil, fmt.Errorf("invalid persisted NPC favor")
		}
		ids = append(ids, uint32(id))
		points[uint32(id)] = uint32(value)
	}
	if len(ids) > 255 {
		return nil, fmt.Errorf("NPC favor snapshot exceeds native count")
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	day := now.In(time.FixedZone("UTC+8", 8*3600)).Add(-6 * time.Hour).Format("20060102")
	dayID, _ := strconv.ParseInt(day, 10, 64)
	var w packetWriter
	w.writeByte(byte(len(ids)))
	for _, id := range ids {
		prefix := "npc_favor_" + strconv.FormatUint(uint64(id), 10) + "_"
		used := int64(0)
		if character.Stats[prefix+"day"] == dayID {
			used = character.Stats[prefix+"gifts"]
		}
		if used < 0 || used > 255 {
			return nil, fmt.Errorf("NPC favor usage exceeds native count")
		}
		w.writeUint32(id)
		w.writeUint32(points[id])
		w.writeByte(byte(used))
		w.writeByte(0)
	}
	return w.bytes(), nil
}

func (s *Service) sendCurrentNPCFavorState(session *gameSession, character dnfrepo.CharacterRecord) error {
	if session == nil || session.selectedCharacterID == 0 || character.CharacterID != strconv.Itoa(int(session.selectedCharacterID)) || character.AccountID != s.accountIDForSession(session) {
		return fmt.Errorf("NPC favor selected owner mismatch")
	}
	body, err := buildCurrentNPCFavorState(character, s.gameplayNow())
	if err != nil {
		return err
	}
	return s.sendGameUpperRawClass(session, currentNPCFavorStateMsgID, body, 0)
}
