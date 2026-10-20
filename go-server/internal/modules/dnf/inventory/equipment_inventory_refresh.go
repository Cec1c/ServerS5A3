package inventory

import (
	"longheng.io/server/internal/modules/dnf/alignedcmd"
	dnfequip "longheng.io/server/internal/modules/dnf/equip"
)

// The owner reports committed endpoints, including a redirected unequip
// destination. Project only inventory rows; worn state has separate actor
// senders and must never be treated as an empty inventory slot.
func equipmentMoveInventorySlotRefreshes(move dnfequip.MoveResult) []alignedcmd.ItemSlotRefresh {
	if !move.Changed {
		return nil
	}
	var rows []alignedcmd.ItemSlotRefresh
	appendRow := func(list byte, slot int16) {
		if slot < 0 || (list != listTypeMain && list != listTypeAvatar && list != listTypePersonalCargo && list != listTypeGuildMedal) {
			return
		}
		for _, row := range rows {
			if row.ListType == list && row.SlotIndex == slot {
				return
			}
		}
		rows = append(rows, alignedcmd.ItemSlotRefresh{ListType: list, SlotIndex: slot})
	}
	appendRow(move.SourceListType, move.SourceSlotIndex)
	appendRow(move.DestinationListType, move.DestinationSlotIndex)
	return rows
}
