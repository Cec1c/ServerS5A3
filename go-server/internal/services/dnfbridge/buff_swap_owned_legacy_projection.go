package dnfbridge

import (
	"encoding/binary"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"time"
)

// This pure BUFF carrier projection retains the imported full row/Extra.
// It never creates an Inventory asset or assigns a current physical identity.
func currentBuffOwnedLegacyItemEntry(item repo.LegacyInventoryItem) currentItemListEntry {
	entry := currentItemListEntryFromLegacyItem(item)
	if item.ListType == 1 {
		entry.avatarSocketData = currentItemListAvatarSocketData(item.Extra)
		entry.avatarColorData = currentItemListAvatarColorData(item.Extra)
	}
	if item.ListType == currentPetInventoryListType {
		// Current creature rows use real serial/handle at +06, never stack quantity
		// or SQL ItemUID. +0E remains explicit value_a, with no serial mirroring.
		serial := sceneInventoryUint32FromInt64(item.PetSerialOrHandle)
		if serial == 0 {
			serial = sceneInventoryExtraUint32(item.Extra, "creature_serial_or_handle", "serial", "handle")
		}
		binary.LittleEndian.PutUint32(entry.data[6:10], serial)
		binary.LittleEndian.PutUint32(entry.data[0x0E:0x12], sceneInventoryExtraUint32(item.Extra, "value_a"))
		expire := currentItemListStackExpire(repo.ItemStack{Extra: item.Extra})
		binary.LittleEndian.PutUint32(entry.data[currentPetRemainSecondsOffset:currentPetRemainSecondsOffset+4], currentPetRemainingSecondsAt(expire, time.Now().UTC()))
		entry.clearLegacyWrongExpiration(expire)
		binary.LittleEndian.PutUint32(entry.data[currentItemListExpireTimeOffset:currentItemListExpireTimeOffset+4], expire)
	}
	return entry
}
