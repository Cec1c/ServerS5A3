package dnfbridge

import (
	"strings"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// currentPVFRelativeAvatarExpirySlotMax deliberately stops below the placement
// table's slot 11. Slot 11 is the separate aura-skin target, not a PVF avatar
// category, so a relative item deadline must never remove its occupant.
const currentPVFRelativeAvatarExpirySlotMax int16 = 10

// currentPVFRelativeAvatarEquipmentExpiry identifies only a worn avatar whose
// persisted relative grant has reached its recorded deadline. It requires the
// PVF avatar placement class, the persisted instance identity (kind, equipment
// type and PVF path), actor slot 0..10, and the complete three-part relative
// provenance checked by currentPVFRelativeGrantExpired. The ordinary 12..25
// branch is not widened, and an item ID alone never authorizes deletion.
func currentPVFRelativeAvatarEquipmentExpiry(
	definition dungeonDropItemDefinition,
	entry dnfrepo.EquipmentEntry,
	now time.Time,
) (int16, bool) {
	if definition.Kind != dungeonDropItemEquipment || definition.UsableExpiredItem ||
		!currentItemListStackExtraIsAvatar(entry.Extra) {
		return 0, false
	}
	kind := strings.ToLower(strings.TrimSpace(entry.Extra["item_kind"]))
	if kind != "avatar" && kind != "equipment" {
		return 0, false
	}
	if !strings.EqualFold(strings.TrimSpace(entry.Extra["equipment_type"]), strings.TrimSpace(definition.EquipmentType)) {
		return 0, false
	}
	entryPath := strings.ReplaceAll(strings.TrimSpace(entry.Extra["pvf_path"]), "\\", "/")
	definitionPath := strings.ReplaceAll(strings.TrimSpace(definition.PVFPath), "\\", "/")
	if definitionPath == "" || !strings.EqualFold(entryPath, definitionPath) {
		return 0, false
	}
	rule, supported := currentEquipmentPlacementRuleForPVFType(definition.EquipmentType)
	if !supported || rule.class != currentEquipmentPlacementClassAvatar ||
		rule.targetSlot < currentEquipmentPlacementAvatarSlotMin || rule.targetSlot > currentPVFRelativeAvatarExpirySlotMax {
		return 0, false
	}
	actorSlot, valid := currentEXEActorEquipmentSlot(entry)
	if !valid || actorSlot != uint64(rule.targetSlot) || actorSlot > uint64(currentPVFRelativeAvatarExpirySlotMax) {
		return 0, false
	}
	stack := dnfrepo.ItemStack{ItemID: entry.ItemID, ExpireAt: entry.ExpireAt, Extra: entry.Extra}
	if !currentPVFRelativeGrantExpired(stack, definition, now) {
		return 0, false
	}
	return rule.targetSlot, true
}
