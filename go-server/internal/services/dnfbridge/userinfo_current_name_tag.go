package dnfbridge

import (
	"strings"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// currentUserInfoNameTagState owns the raw8 after the mode1/mode3 equipment
// block. In client 927634, 0x22C32B0 looks up/removes endpoint 30 and creates a
// descriptor with only its expiry at +0x70. This is a name tag, not a creature.
// Sending the slot-26 pet here makes the default attachment infer type 26 and
// replace its fully initialized, enchanted object with an empty-card object.
func (r *csharpLegacyUserInfoReader) currentUserInfoNameTagState(character dnfrepo.CharacterRecord, row dnfrepo.LegacyUserInfoRow) (uint32, uint32) {
	if r != nil && r.service != nil && strings.TrimSpace(r.characterID) != "" {
		if repos, ok := r.service.repositoryGroupFor(r.ctx); ok && repos.Equipment != nil {
			record, found, err := repos.Equipment.Load(r.ctx, r.characterID)
			if err != nil {
				if r.session != nil {
					r.service.logGameEvent(r.session, "game-userinfo-name-tag-load-failed", "character_id", r.characterID, "error", err)
				}
				return 0, 0
			}
			if found {
				entry, worn := record.Entries["30"]
				if !worn || entry.SlotIndex != currentNameTagEquipmentSlot || entry.ItemID <= 0 {
					return 0, 0
				}
				return sceneInventoryUint32FromInt64(entry.ItemID), currentItemListEquipmentExpire(entry)
			}
		}
	}
	// Imported characters can predate the equipment repository. Retain the
	// original typed legacy fields, never borrow a pet identity as fallback.
	itemID := rowStatU32(row, character, "name_tag_item_id", 0)
	if itemID == 0 {
		return 0, 0
	}
	return itemID, rowStatU32(row, character, "name_tag_expire_time", 0)
}
