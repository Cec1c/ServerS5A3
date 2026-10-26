package dnfbridge

import (
	"encoding/binary"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"

	"longheng.io/server/internal/modules/dnf/buffswap"
)

type nativeBuffSwapItemRow struct {
	slot      byte
	binding   buffswap.Binding
	entry     currentItemListEntry
	rawBlocks bool
}

// RVA 1C07B00 (VA 2007B00) clears this actor's buff registration, reads
// u16 selected skill/u8 count, then resolves/creates each item and invokes
// 3CD7F0(kind=0, actor, record, send=false). Remote bags are absent in the
// viewer, so this block includes the full item state, not just coordinates.
func (r *csharpLegacyUserInfoReader) writeNativeBuffSwapUserInfo(w *packetWriter) {
	end := r.beginNativeBuffOwnedSerialization()
	defer end()
	state, rows, err := r.nativeBuffSwapUserInfoRows()
	if err != nil {
		state, rows = buffswap.State{}, nil
	}
	w.writeUint16(state.Skill)
	w.writeByte(byte(len(rows)))
	for _, row := range rows {
		w.writeByte(row.slot)
		w.writeByte(row.binding.List)
		w.writeUint16(row.binding.Index)
		w.writeBytes(row.entry.data[:])
		if row.rawBlocks {
			writeCurrentAvatarItemListOptionalBlobs(w, row.entry)
		}
	}
	if r != nil && r.service != nil && r.session != nil {
		r.service.logGameEvent(r.session, "game-buff-swap-native-userinfo", "char_id", r.characterID, "skill", state.Skill, "rows", len(rows), "error", err)
	}
}

// beginNativeBuffOwnedSerialization bounds a detached projection to one body.
// Nested item/skill/text writers share it; a later body on the same reader
// starts fresh. The reader remains a synchronous, caller-local serializer.
func (r *csharpLegacyUserInfoReader) beginNativeBuffOwnedSerialization() func() {
	if r == nil {
		return func() {}
	}
	if r.buffOwnedDepth == 0 {
		r.clearNativeBuffOwnedProjection()
	}
	r.buffOwnedDepth++
	return func() {
		r.buffOwnedDepth--
		if r.buffOwnedDepth == 0 {
			r.clearNativeBuffOwnedProjection()
		}
	}
}

func (r *csharpLegacyUserInfoReader) clearNativeBuffOwnedProjection() {
	r.buffOwnedLoaded = false
	r.buffOwned = buffswap.OwnedProjection{}
	r.buffOwnedErr = nil
}

// One serialization owns one detached projection, reused by the selected skill
// page and view text. No asset repository is loaded after this transaction returns.
func (r *csharpLegacyUserInfoReader) loadNativeBuffOwnedProjection() (buffswap.OwnedProjection, error) {
	if r == nil || r.service == nil || r.session == nil {
		return buffswap.OwnedProjection{}, nil
	}
	if r.buffOwnedLoaded {
		return r.buffOwned, r.buffOwnedErr
	}
	r.buffOwnedLoaded = true
	repos, ok := r.service.repositoryGroup()
	if !ok {
		return buffswap.OwnedProjection{}, nil
	}
	r.buffOwned, r.buffOwnedErr = buffswap.LoadResolvedOwnedWithOptions(r.ctx, repos, r.characterID, r.service.accountIDForSession(r.session), buffswap.OwnedProjectionOptions{
		MigrateCurrentInstances: true,
		NeedsEquippedPetMetadata: func(item dnfrepo.EquipmentEntry) bool {
			return item.SlotIndex == 26 && currentEquippedCreatureFromEquipment(item).valid() && currentEquippedCreatureEnchantCardID(item) == 0
		},
	})
	return r.buffOwned, r.buffOwnedErr
}

func (r *csharpLegacyUserInfoReader) nativeBuffSwapUserInfoRows() (buffswap.State, []nativeBuffSwapItemRow, error) {
	projection, err := r.loadNativeBuffOwnedProjection()
	if err != nil {
		return buffswap.State{}, nil, err
	}
	state := projection.State
	var rows []nativeBuffSwapItemRow
	for slot := byte(0); slot < buffswap.SlotCount; slot++ {
		item, exists := projection.Items[slot]
		if !exists {
			continue
		}
		b := item.Binding
		var entry currentItemListEntry
		rawBlocks := b.List == 1
		switch {
		case item.HasLegacyItem:
			entry = currentBuffOwnedLegacyItemEntry(item.LegacyItem.Item)
		case item.HasCurrentEquipment:
			equipped := item.CurrentEquipment
			if equipped.SlotIndex == 26 && currentEquippedCreatureFromEquipment(equipped).valid() && currentEquippedCreatureEnchantCardID(equipped) == 0 && projection.PetMetadataAvailable {
				equipped = currentEquippedCreatureWithDurableEnchant(equipped, projection.PetMetadata)
			}
			var valid bool
			entry, valid = currentItemListEntryFromEquipment(equipped)
			if !valid {
				continue
			}
			equipmentType, known := r.currentMode1EquipmentType(equipped)
			if state.WireProfile == buffswap.CurrentWireProfile {
				// The same instance is serialized at its actor slot; the stored
				// legacy PVF key must not make a valid registered row disappear.
				b.Index = binary.LittleEndian.Uint16(entry.data[:2])
			}
			rawBlocks = currentMode1EquipmentReadsRawBlocks(equipped.Extra, equipmentType, known)
		case item.HasCurrentStack:
			entry = currentItemListEntryFromStack(b.List, int16(b.Index), item.CurrentStack)
		default:
			continue
		}
		if binary.LittleEndian.Uint16(entry.data[:2]) != b.Index || int64(binary.LittleEndian.Uint32(entry.data[2:6])) != b.ItemID {
			continue
		}
		rows = append(rows, nativeBuffSwapItemRow{slot, b, entry, rawBlocks})
	}
	return state, rows, nil
}
