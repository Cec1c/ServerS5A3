package dnfbridge

import "longheng.io/server/internal/modules/dnf/buffswap"

// 1368/9985F0 list3 indexes the actual actor equipment getter, not the
// repository key. Keep the resolved instance owner unchanged and translate
// only its detached wire coordinates using the same rule as item/actor rows.
func currentBuffOwnedWireState(projection buffswap.OwnedProjection) buffswap.State {
	state := projection.State
	if state.WireProfile != buffswap.CurrentWireProfile {
		return state
	}
	state.Slots = make(map[byte]buffswap.Binding, len(projection.State.Slots))
	for slot, b := range projection.State.Slots {
		if b.List == 3 {
			item, ok := projection.Items[slot]
			if !ok || !item.HasCurrentEquipment {
				continue
			}
			index, ok := currentEXEActorEquipmentSlot(item.CurrentEquipment)
			if !ok || index >= buffswap.SlotCount {
				continue
			}
			b.Index = uint16(index)
		}
		state.Slots[slot] = b
	}
	return state
}
