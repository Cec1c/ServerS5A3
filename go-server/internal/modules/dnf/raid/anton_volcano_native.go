package raid

// The shipped v96/v97 Anton controller (01877E20) dispatches ID 111 to
// 018780FA/0187819D: closed/open edges select the 2-2/2-1 cutscenes. This
// native state has no name in anton.etc.symbol; do not invent a PVF entry.
const AntonVolcanoOpenSymbolID uint32 = 111

func (s *AntonProgression) updateVolcanoNativeState(barrier uint32) {
	if s.phase != 1 || s.program.luke {
		return
	}
	open := s.volcanoOpen
	switch barrier {
	case 0:
		open = true
	case 10000:
		open = false
	default:
		return // Intermediate recharge values retain the last native edge.
	}
	if s.volcanoStateKnown && s.volcanoOpen == open {
		return
	}
	s.volcanoOpen, s.volcanoStateKnown = open, true
	value := uint32(0)
	if open {
		value = 1
	}
	s.recordNativeSymbolWrite(AntonVolcanoOpenSymbolID, value)
}

func (s *AntonProgression) appendVolcanoNativeSymbol(values map[uint32]uint32) {
	if s.phase != 1 || s.program.luke || !s.volcanoStateKnown {
		return
	}
	value := uint32(0)
	if s.volcanoOpen {
		value = 1
	}
	values[AntonVolcanoOpenSymbolID] = value
}
