package dnfbridge

import (
	"encoding/binary"
	"strconv"
	"strings"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

const (
	currentAikenSupportTitleOwnedID  int64 = 400339004
	currentAikenSupportTitleCombatID int64 = 400339005
	currentAikenSupportTitleSlot           = 13
)

// Reject both definitions by authoritative inventory ID, including titles
// bought before this rule and clients still carrying the old PVF metadata.
func currentAikenSupportTitleDisjointForbidden(itemID int64) bool {
	return itemID == currentAikenSupportTitleOwnedID || itemID == currentAikenSupportTitleCombatID
}

// The PVF job tokens cover priest families. The second-awakening branch is
// checked against the authoritative character record before equipping or
// projecting the combat-only definition.
func currentAikenSupportTitleEligible(character dnfrepo.CharacterRecord) bool {
	job, err := strconv.Atoi(strings.TrimSpace(character.Job))
	if err != nil || (job != 4 && job != 14) {
		return false
	}
	growth := characterGrowType(character)
	return growth.Present && growth.InDomain && growth.Branch == 1 && growth.Awakening == 2
}

func currentAikenSupportTitleDungeon(dungeonID int64) bool {
	switch dungeonID {
	case 5097, 5098, 3570, 3571:
		return true
	default:
		return false
	}
}

func setCurrentAikenSupportTitleProjection(session *gameSession, runtime *runtimeDungeonState) {
	if session == nil {
		return
	}
	active := runtime != nil &&
		currentAikenSupportTitleDungeon(runtime.Dungeon.ID) &&
		runtime.Character.CharacterID == strconv.Itoa(int(session.selectedCharacterID)) &&
		currentAikenSupportTitleEligible(runtime.Character)
	session.aikenSupportTitleActive.Store(active)
}

func projectCurrentAikenSupportTitleItemRows(session *gameSession, entries []currentItemListEntry) {
	if session == nil || !session.aikenSupportTitleActive.Load() {
		return
	}
	for i := range entries {
		row := &entries[i].data
		if binary.LittleEndian.Uint16(row[0:2]) == currentAikenSupportTitleSlot &&
			binary.LittleEndian.Uint32(row[2:6]) == uint32(currentAikenSupportTitleOwnedID) {
			binary.LittleEndian.PutUint32(row[2:6], uint32(currentAikenSupportTitleCombatID))
		}
	}
}

func projectCurrentAikenSupportTitleEquipmentUpdates(session *gameSession, entries []currentEquipmentUpdateEntry) {
	if session == nil || !session.aikenSupportTitleActive.Load() {
		return
	}
	for i := range entries {
		row := &entries[i].data
		if binary.LittleEndian.Uint16(row[0:2]) == currentAikenSupportTitleSlot &&
			binary.LittleEndian.Uint32(row[2:6]) == uint32(currentAikenSupportTitleOwnedID) {
			binary.LittleEndian.PutUint32(row[2:6], uint32(currentAikenSupportTitleCombatID))
		}
	}
}
