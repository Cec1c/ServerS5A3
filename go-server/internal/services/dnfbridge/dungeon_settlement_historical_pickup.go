package dnfbridge

import (
	"encoding/binary"
	"strconv"
	"strings"
)

// A frozen op39 plus the exact op14 row is produced only after a pickup
// transaction commits. This proves a historical pickup for display purposes;
// it does not prove present ownership, authorize a grant or rebuild a bag row.
func currentDungeonHistoricalPickupReceipt(drop *runtimeDungeonDrop, characterID string, self uint16) bool {
	if self == 0 || characterID != strconv.FormatUint(uint64(self), 10) || drop == nil ||
		drop.Amount == 0 || currentDungeonPickupListType(drop.Item) != 0 {
		return false
	}
	picker, proven := currentDungeonCommittedPickupProof(drop)
	if !proven || picker != self {
		return false
	}
	body := drop.PickupItemUpdateBody
	if len(body) != 3+currentItemListEntryWireSize || body[0] != 0 || binary.LittleEndian.Uint16(body[1:3]) != 1 ||
		binary.LittleEndian.Uint16(body[3:5]) != drop.DestinationSlot || binary.LittleEndian.Uint32(body[5:9]) != drop.Item.ItemID {
		return false
	}
	switch drop.Item.Kind {
	case dungeonDropItemEquipment:
		return drop.Amount == 1 && strings.HasPrefix(strings.ToLower(drop.Item.PVFPath), "equipment/")
	case dungeonDropItemStackable:
		return strings.HasPrefix(strings.ToLower(drop.Item.PVFPath), "stackable/") && binary.LittleEndian.Uint32(body[9:13]) >= drop.Amount
	default:
		return false
	}
}
