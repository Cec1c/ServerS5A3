package raid

import (
	"fmt"
	"strconv"
	"strings"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
)

// LoadSingleAntonHPSymbols binds the actual embedded single catalog to the
// integer-before-tag symbol table. HP values are cumulative dungeon clears,
// as in AntonProgression.ClearDungeon; this owner supports one clear per map.
// Both weekly links must be covered, including maps absent from this run.
func LoadSingleAntonHPSymbols(source dnfpvf.Source, weeklyLimit int) (map[uint32]uint32, error) {
	if weeklyLimit <= 0 || weeklyLimit > 255 {
		return nil, fmt.Errorf("single Anton weekly limit invalid")
	}
	symbols, err := LoadAntonSymbols(source)
	if err != nil {
		return nil, err
	}
	text, err := source.ReadText("etc/raid/anton.etc")
	if err != nil {
		return nil, err
	}
	doc, err := dnfpvf.Parse("etc/raid/anton.etc", text)
	if err != nil {
		return nil, err
	}
	result, usedSymbols := map[uint32]uint32{}, map[uint32]bool{}
	inSingle, inDungeon, sawSingle := false, false, false
	var id, symbol uint32
	hasState, hasHP := false, false
	for _, section := range doc.Sections {
		name := strings.ToUpper(strings.TrimSpace(section.Name))
		values := sectionValues(doc, section)
		switch name {
		case "SINGLE RAID PHASE":
			if inSingle || sawSingle || inDungeon {
				return nil, fmt.Errorf("single Anton catalog repeated/nested")
			}
			inSingle, sawSingle = true, true
		case "/SINGLE RAID PHASE":
			if !inSingle || inDungeon || len(result) == 0 {
				return nil, fmt.Errorf("single Anton catalog incomplete")
			}
			inSingle = false
		default:
			if !inSingle {
				continue
			}
			switch name {
			case "PHASE", "/PHASE":
				return nil, fmt.Errorf("ordinary phase nested in single Anton catalog")
			case "DUNGEON INFO":
				if inDungeon {
					return nil, fmt.Errorf("single Anton dungeon nested")
				}
				inDungeon, id, symbol, hasState, hasHP = true, 0, 0, false, false
			case "INDEX":
				if !inDungeon || id != 0 || len(values) != 1 {
					return nil, fmt.Errorf("single Anton index invalid")
				}
				id, err = antonUint(values[0])
				if err != nil || id == 0 {
					return nil, fmt.Errorf("single Anton index unavailable")
				}
			case "INIT STATE":
				if !inDungeon || hasState || len(values) != 1 {
					return nil, fmt.Errorf("single Anton initial state invalid")
				}
				switch strings.ToLower(values[0].Value) {
				case "open", "close", "hide":
					hasState = true
				default:
					return nil, fmt.Errorf("single Anton initial state unsupported")
				}
			case "HP INFO":
				if !inDungeon || hasHP || id == 0 || len(values) != 0 {
					return nil, fmt.Errorf("single Anton HP declaration invalid")
				}
				hasHP = true
			case "/DUNGEON INFO":
				if !inDungeon || id == 0 || !hasState || !hasHP || symbol == 0 || result[id] != 0 || usedSymbols[symbol] {
					return nil, fmt.Errorf("single Anton dungeon HP incomplete/repeated")
				}
				result[id], usedSymbols[symbol], inDungeon = symbol, true, false
			default:
				if strings.HasSuffix(name, " DGN HP") {
					if !inDungeon || !hasHP || symbol != 0 || name != strconv.FormatUint(uint64(id), 10)+" DGN HP" || len(values) != 1 || values[0].Kind != dnfpvf.TokenInt || values[0].Int != 1 || symbols[name] == 0 {
						return nil, fmt.Errorf("single Anton HP must bind this dungeon's one-clear symbol")
					}
					symbol = symbols[name]
				}
			}
		}
	}
	if !sawSingle || inSingle || inDungeon || len(result) == 0 || len(result) > 255 {
		return nil, fmt.Errorf("single Anton HP catalog unavailable")
	}
	union := map[uint32]bool{}
	for count := 0; count < weeklyLimit; count++ {
		sequence, err := LoadSingleRaidSequence(source, 0, count)
		if err != nil {
			return nil, err
		}
		for _, dungeon := range sequence {
			if result[dungeon] == 0 {
				return nil, fmt.Errorf("single Anton weekly map %d lacks HP symbol", dungeon)
			}
			union[dungeon] = true
		}
	}
	if len(union) != len(result) {
		return nil, fmt.Errorf("single Anton HP catalog differs from weekly link union")
	}
	return result, nil
}
