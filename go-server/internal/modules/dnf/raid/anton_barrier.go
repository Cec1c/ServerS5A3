package raid

import (
	"fmt"
	"time"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
)

// Amounts belong to the formal last-action scripts, never to CMT662 operands.
type AntonBarrierConfiguration struct {
	Charge                                         [2]uint32
	Small, Large, Constructor, InfectedConstructor uint32
}

func LoadAntonBarrierConfiguration(source dnfpvf.Source) (AntonBarrierConfiguration, error) {
	flow, err := LoadAntonFlowConfiguration(source)
	if err != nil {
		return AntonBarrierConfiguration{}, err
	}
	c := AntonBarrierConfiguration{Charge: flow.ShieldChargePerSecond}
	read := func(path string) (*dnfpvf.Document, []uint32, error) {
		text, err := source.ReadText(path)
		if err != nil {
			return nil, nil, err
		}
		doc, err := dnfpvf.Parse(path, text)
		if err != nil {
			return nil, nil, err
		}
		var amounts []uint32
		for i, section := range doc.Sections {
			if section.Name != "RAID SET SYMBOL" {
				continue
			}
			values := sectionValues(doc, section)
			if len(values) != 1 || i+1 >= len(doc.Sections) {
				return nil, nil, fmt.Errorf("invalid barrier action %s", path)
			}
			id, err := antonUint(values[0])
			if err != nil {
				return nil, nil, err
			}
			if id != 110 {
				continue
			}
			next := doc.Sections[i+1]
			operand := sectionValues(doc, next)
			if next.Name != "-" || len(operand) != 1 {
				return nil, nil, fmt.Errorf("unsupported barrier operation %s", path)
			}
			amount, err := antonUint(operand[0])
			if err != nil || amount == 0 || amount > 10000 {
				return nil, nil, fmt.Errorf("invalid barrier amount %s", path)
			}
			amounts = append(amounts, amount)
		}
		return doc, amounts, nil
	}
	for _, entry := range []struct {
		path   string
		target *uint32
	}{
		{"passiveobject/actionobject/map/anton/phase2/transmission/action/transmission_s_last.act", &c.Small},
		{"passiveobject/actionobject/map/anton/phase2/transmission/action/transmission_l_last.act", &c.Large},
	} {
		_, amounts, err := read(entry.path)
		if err != nil {
			return c, err
		}
		if len(amounts) != 1 {
			return c, fmt.Errorf("ambiguous transmission barrier action %s", entry.path)
		}
		*entry.target = amounts[0]
	}
	for i, suffix := range []string{"", "_1", "_2", "_3"} {
		path := "monster/newmonsters/anton/phase3/constructor/action/last" + suffix + ".act"
		doc, amounts, err := read(path)
		if err != nil {
			return c, err
		}
		checks := 0
		for n, section := range doc.Sections {
			if section.Name != "CHECK RAID SYMBOL" {
				continue
			}
			values := sectionValues(doc, section)
			if len(values) != 1 || values[0].Int != 8 || n+1 >= len(doc.Sections) {
				return c, fmt.Errorf("unsupported constructor condition %s", path)
			}
			next := doc.Sections[n+1]
			equal := sectionValues(doc, next)
			if next.Name != "=" || len(equal) != 1 || equal[0].Int != int64(221+i) {
				return c, fmt.Errorf("incorrect constructor hatchery %s", path)
			}
			checks++
		}
		if len(amounts) != 2 || checks != 1 {
			return c, fmt.Errorf("incomplete constructor barrier action %s", path)
		}
		if i == 0 {
			c.Constructor, c.InfectedConstructor = amounts[0], amounts[1]
		} else if c.Constructor != amounts[0] || c.InfectedConstructor != amounts[1] {
			return c, fmt.Errorf("inconsistent constructor barrier actions")
		}
	}
	return c, nil
}

type AntonBarrierSource byte

const (
	AntonSmallTransmission AntonBarrierSource = iota + 1
	AntonLargeTransmission
	AntonHatcheryConstructor
)

// Identifies the actual last-action producer; ApplyBarrierDamage still chooses
// the damage from the loaded program and never consumes a client amount.
func (s *AntonProgression) TransmissionBarrierOperand(source AntonBarrierSource) (uint32, bool) {
	if s == nil || s.program == nil {
		return 0, false
	}
	switch source {
	case AntonSmallTransmission:
		return s.program.barrier.Small, s.program.barrier.Small != 0
	case AntonLargeTransmission:
		return s.program.barrier.Large, s.program.barrier.Large != 0
	}
	return 0, false
}

func (s *AntonProgression) barrierActive() bool {
	return s != nil && s.program != nil && s.phase == 1 && !s.phaseCleared && s.dungeons[219] == 0 && !s.barrierNext.IsZero()
}

func (s *AntonProgression) changeBarrier(value uint32, events *[]antonEvent) {
	old := s.symbols["BLACK VOLCANO BARRIER"]
	if value == old {
		return
	}
	s.setSymbol("BLACK VOLCANO BARRIER", value, events)
	// The loaded ON CHANGE rules own the 126/127 movie writes. Do not
	// duplicate them or invent a reset absent from the formal script.
}

func (s *AntonProgression) chargeBarrier(events *[]antonEvent) {
	index := 0
	if s.symbols["IS EXIST INFECTION DUNGEON"] == 1 {
		index = 1
	}
	value := s.symbols["BLACK VOLCANO BARRIER"] + s.program.barrier.Charge[index]
	if value > 10000 {
		value = 10000
	}
	s.changeBarrier(value, events)
}

// The bridge supplies a unique accepted run/room/object death identity and its
// authoritative PVF source. A client symbol, amount or finished dungeon alone
// is insufficient. Failed updates leave the original progression unchanged.
func (s *AntonProgression) ApplyBarrierDamage(eventID string, source AntonBarrierSource, dungeonID uint32, epoch uint64, now time.Time) (*AntonProgression, bool, error) {
	if s == nil || eventID == "" || len(eventID) > 256 || epoch == 0 || epoch != s.epoch {
		return nil, false, fmt.Errorf("barrier event owner unavailable")
	}
	if s.barrierEvents[eventID] {
		return s.Clone(), true, nil
	}
	v, err := s.Tick(now)
	if err != nil {
		return nil, false, err
	}
	if !v.barrierActive() || v.epoch != epoch || v.dungeons[dungeonID] != 0 {
		return nil, false, fmt.Errorf("barrier event phase/dungeon unavailable")
	}
	var amount uint32
	switch source {
	case AntonSmallTransmission, AntonLargeTransmission:
		if dungeonID != 218 {
			return nil, false, fmt.Errorf("transmission outside energy dungeon")
		}
		amount = v.program.barrier.Small
		if source == AntonLargeTransmission {
			amount = v.program.barrier.Large
		}
	case AntonHatcheryConstructor:
		if dungeonID < 221 || dungeonID > 224 {
			return nil, false, fmt.Errorf("constructor outside hatchery")
		}
		amount = v.program.barrier.Constructor
		if v.symbols["INFECTION DUNGEON INDEX"] == dungeonID {
			amount += v.program.barrier.InfectedConstructor
		}
	default:
		return nil, false, fmt.Errorf("unsupported barrier event source")
	}
	if amount == 0 {
		return nil, false, fmt.Errorf("barrier damage dependency unavailable")
	}
	value := v.symbols["BLACK VOLCANO BARRIER"]
	if amount >= value {
		value = 0
	} else {
		value -= amount
	}
	events := []antonEvent{}
	v.changeBarrier(value, &events)
	if err := v.evaluate(now, events); err != nil {
		return nil, false, err
	}
	v.barrierEvents[eventID] = true
	return v, false, nil
}
