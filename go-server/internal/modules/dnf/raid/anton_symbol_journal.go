package raid

import "fmt"

const AntonSymbolJournalLimit = 4096

type SymbolValue struct{ ID, Value uint32 }
type AntonSymbolWrite struct {
	Sequence uint64
	SymbolValue
}

func (s *AntonProgression) SymbolSequence() uint64 {
	if s == nil {
		return 0
	}
	return s.symbolSequence
}

func (s *AntonProgression) recordSymbolWrite(name string, value uint32) {
	s.recordNativeSymbolWrite(s.program.symbols[name], value)
}

func (s *AntonProgression) recordNativeSymbolWrite(id, value uint32) {
	s.symbolSequence++
	s.symbolWrites = append(s.symbolWrites, AntonSymbolWrite{Sequence: s.symbolSequence, SymbolValue: SymbolValue{ID: id, Value: value}})
	if len(s.symbolWrites) > AntonSymbolJournalLimit {
		s.symbolWrites = append([]AntonSymbolWrite(nil), s.symbolWrites[len(s.symbolWrites)-AntonSymbolJournalLimit:]...)
	}
}

// Counter resets/increments need projection when their value changes; explicit
// PVF SET instructions above additionally preserve unchanged native callbacks.
func (s *AntonProgression) assignSymbolValue(name string, value uint32) {
	if s.symbols[name] != value {
		s.recordSymbolWrite(name, value)
	}
	s.symbols[name] = value
}

// Ordered writes preserve pulses such as FAIL=1 then FAIL=0 and repeated
// KILL=1. Snapshot differencing loses both. A subscriber cursor belongs to this
// exact instance/phase and must be advanced only after successful transport.
// Falling behind the bounded journal is explicit, never a silent full replay.
func (s *AntonProgression) SymbolWritesAfter(cursor uint64) ([]AntonSymbolWrite, error) {
	if s == nil || cursor > s.symbolSequence {
		return nil, fmt.Errorf("Anton symbol cursor outside phase")
	}
	if cursor == s.symbolSequence {
		return nil, nil
	}
	if len(s.symbolWrites) == 0 || cursor+1 < s.symbolWrites[0].Sequence {
		return nil, fmt.Errorf("Anton symbol journal gap requires controlled resubscription")
	}
	for i, write := range s.symbolWrites {
		if write.Sequence > cursor {
			return append([]AntonSymbolWrite(nil), s.symbolWrites[i:]...), nil
		}
	}
	return nil, fmt.Errorf("Anton symbol journal is inconsistent")
}

// Current583 processes each entry in wire order; repeated IDs must remain
// repeated. A map builder cannot encode transient or same-value SET events.
func BuildOrderedSymbolBody(writes []SymbolValue) ([]byte, error) {
	if len(writes) > 255 {
		return nil, fmt.Errorf("raid ordered symbols exceed native count")
	}
	var w packetWriter
	w.writeByte(byte(len(writes)))
	for _, symbol := range writes {
		if symbol.ID == 0 {
			return nil, fmt.Errorf("raid ordered symbol zero is reserved for reset")
		}
		w.writeUint32(symbol.ID)
		w.writeUint32(symbol.Value)
	}
	return w.bytes(), nil
}
