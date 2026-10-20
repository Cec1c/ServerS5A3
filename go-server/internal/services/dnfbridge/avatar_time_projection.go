package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// The current client copies an avatar's create scalar (0x77 row +0x06)
// into its remaining-seconds metadata. Zero is the permanent sentinel;
// neither the durable item quantity nor its equipment quality seed is time.
// Use the same deadline and expired boundary as the creature time projection.
// This does not alter the independently stored op19 identity or inventory.
func currentAvatarRemainingSecondsAt(expire uint32, now time.Time) uint32 {
	return currentPetRemainingSecondsAt(expire, now)
}

// Scene creation has no inventory-list late pass. Apply that existing
// read-only PVF policy to the temporary avatar row here as well, so a template
// absolute deadline cannot leave scene creation at the permanent sentinel.
// Relative periods are deliberately not generated again at login.
func (r *csharpLegacyUserInfoReader) currentMode1EquipmentTimeOrInstance(equipped dnfrepo.EquipmentEntry, entry *currentItemListEntry) uint32 {
	if _, avatar := currentAvatarEquipmentRemainingSecondsAt(equipped, time.Now().UTC()); !avatar {
		return currentMode1EquipmentGradeOrInstance(equipped)
	}
	rows := []currentItemListEntry{*entry}
	if _, err := r.service.applyCurrentAvatarPVFTimeToEntries(r.ctx, rows); err != nil {
		r.service.logPacketEvent("game-upper-avatar-scene-time-catalog-fallback", "char_id", r.characterID, "item_id", equipped.ItemID, "err", err)
	}
	*entry = rows[0]
	return binary.LittleEndian.Uint32(entry.data[0x06:0x0A])
}

// Incremental item refreshes do not all have the general PVF late pass. Resolve
// only avatar rows here, reuse that policy, and copy back only their time fields.
func (s *Service) applyCurrentAvatarPVFTimeToEntries(ctx context.Context, entries []currentItemListEntry) (int, error) {
	if len(entries) == 0 {
		return 0, nil
	}
	catalog, err := s.currentPVFItemCatalog()
	if err != nil {
		return 0, err
	}
	var rows []currentItemListEntry
	var indexes []int
	var errs []error
	for i := range entries {
		id := binary.LittleEndian.Uint32(entries[i].data[2:6])
		definition, err := s.currentPVFDefinitionForItem(catalog, id)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if definition.Kind == dungeonDropItemEquipment && currentItemListStackExtraIsAvatar(map[string]string{"equipment_type": definition.EquipmentType}) {
			indexes = append(indexes, i)
			rows = append(rows, entries[i])
		}
	}
	count, err := s.applyCurrentPVFUsePeriodsToEntriesWithCatalog(ctx, rows, catalog, time.Now().UTC())
	if err != nil {
		errs = append(errs, err)
	}
	for i, target := range indexes {
		copy(entries[target].data[6:10], rows[i].data[6:10])
		copy(entries[target].data[0x38:0x3c], rows[i].data[0x38:0x3c])
	}
	return count, errors.Join(errs...)
}

func (s *Service) applyCurrentAvatarPVFTimeToEquipmentUpdates(ctx context.Context, entries []currentEquipmentUpdateEntry) (int, error) {
	rows := make([]currentItemListEntry, len(entries))
	for i := range entries {
		copy(rows[i].data[:10], entries[i].data[:10])
		copy(rows[i].data[0x38:0x3c], entries[i].data[currentEquipmentUpdateExpireTimeOffset:currentEquipmentUpdateExpireTimeOffset+4])
	}
	count, err := s.applyCurrentAvatarPVFTimeToEntries(ctx, rows)
	for i := range entries {
		copy(entries[i].data[6:10], rows[i].data[6:10])
		copy(entries[i].data[currentEquipmentUpdateExpireTimeOffset:currentEquipmentUpdateExpireTimeOffset+4], rows[i].data[0x38:0x3c])
	}
	return count, err
}

func currentAvatarEquipmentRemainingSecondsAt(equipped dnfrepo.EquipmentEntry, now time.Time) (uint32, bool) {
	if currentEquippedCreatureInstance(equipped) != 0 || !currentItemListStackExtraIsAvatar(equipped.Extra) {
		return 0, false
	}
	return currentAvatarRemainingSecondsAt(currentItemListEquipmentExpire(equipped), now), true
}
