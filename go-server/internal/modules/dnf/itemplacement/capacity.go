package itemplacement

import dnfrepo "longheng.io/server/internal/modules/dnf/repository"

// SlotOpen 校验主包分页与个人金库的开放容量。main_list_param16 是新增格数，
// 基础每页32格；个人金库缺扩容记录时只开放已确认的基础8格。
// 快捷栏、徽章及固定材料格另有规则，账号金库由账号所有者单独校验。
func SlotOpen(listType byte, slot int16, state *dnfrepo.CharacterContainerState) bool {
	if slot < 0 {
		return false
	}
	if listType == 0 && slot >= 9 && slot <= 288 {
		expansion := uint16(0)
		if state != nil {
			expansion = state.MainSlotCount
		}
		if expansion > 24 || expansion%8 != 0 {
			return false
		}
		return (slot-9)%56 < int16(32+expansion)
	}
	if listType == 2 {
		capacity := int16(8)
		if state != nil {
			capacity = int16(state.PersonalCargoSlotCount)
		}
		return slot < capacity
	}
	return true
}

func OpenRangeEnd(listType byte, start, end int16, state *dnfrepo.CharacterContainerState) int16 {
	for end >= start && !SlotOpen(listType, end, state) {
		end--
	}
	return end
}
