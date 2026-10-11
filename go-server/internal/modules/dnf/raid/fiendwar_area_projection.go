package raid

import "fmt"

// AreaStateSymbols projects a complete trusted battlefield snapshot. The six
// values are the owned 24.9 EXE's getStringToFiendWarAreaState (03BE9A40)
// returns, verified by execution, not the intermediate string-registry IDs.
// This does not authorize area changes or accept state from a client packet.
func (c FiendWarConfiguration) AreaStateSymbols(states map[uint32]string) (map[uint32]uint32, error) {
	if len(c.Areas) == 0 || len(states) != len(c.Areas) {
		return nil, fmt.Errorf("Fiend War area snapshot incomplete")
	}
	result := make(map[uint32]uint32, len(c.Areas))
	for _, area := range c.Areas {
		state, exists := states[area.ID]
		if !exists {
			return nil, fmt.Errorf("Fiend War area %d missing", area.ID)
		}
		var value uint32
		switch state {
		case "empty":
			value = 0
		case "enemy":
			value = 1
		case "monster":
			value = 2
		case "user_enemy":
			value = 3
		case "user_monster":
			value = 4
		case "enemy_monster":
			value = 5
		default:
			return nil, fmt.Errorf("Fiend War area %d has unsupported state %q", area.ID, state)
		}
		id := c.Symbols[fmt.Sprintf("%d AREA STATE", area.ID)]
		if id == 0 {
			return nil, fmt.Errorf("Fiend War area %d symbol unavailable", area.ID)
		}
		if _, duplicate := result[id]; duplicate {
			return nil, fmt.Errorf("Fiend War area symbol owner repeated")
		}
		result[id] = value
	}
	return result, nil
}
