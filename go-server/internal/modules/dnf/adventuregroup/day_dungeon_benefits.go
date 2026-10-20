package adventuregroup

import (
	"fmt"
	"strings"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
)

// DayDungeonAccountCharacterLimit is the original growthguide's five distinct
// characters plus the exact current-level [etc] benefit from adventurersystem2018.
// Counts of entries and the identity set of admitted characters are separate.
func (c RuntimeConfig) DayDungeonAccountCharacterLimit(manageLevel int) (int64, error) {
	if len(c.DayDungeonCharacterBonuses) == 0 || manageLevel < 0 {
		return 0, ErrTableEmpty
	}
	bonus, found := c.DayDungeonCharacterBonuses[manageLevel]
	if manageLevel == 0 {
		bonus, found = 0, true
	}
	if !found || bonus < 0 || bonus > 5 {
		return 0, ErrTableMalformed
	}
	return 5 + bonus, nil
}

func parseDayDungeonCharacterBonuses(doc *dnfpvf.Document) (map[int]int64, error) {
	out := make(map[int]int64)
	if doc == nil {
		return out, nil
	}
	for index := 0; index < len(doc.Sections); index++ {
		section := doc.Sections[index]
		if !strings.EqualFold(section.Name, "attribute") {
			continue
		}
		tokens := sectionTokens(doc, section)
		if len(tokens) != 1 || tokens[0].Kind != dnfpvf.TokenInt || tokens[0].Int < 1 || tokens[0].Int > 255 {
			return nil, fmt.Errorf("%w: day-dungeon attribute level", ErrTableMalformed)
		}
		level := int(tokens[0].Int)
		if _, exists := out[level]; exists {
			return nil, fmt.Errorf("%w: duplicate attribute level %d", ErrTableMalformed, level)
		}
		bonus := int64(0)
		found := false
		end := index + 1
		for end < len(doc.Sections) && !strings.EqualFold(doc.Sections[end].Name, "/attribute") {
			row := doc.Sections[end]
			if strings.EqualFold(row.Name, "attribute") {
				return nil, fmt.Errorf("%w: unclosed attribute", ErrTableMalformed)
			}
			if strings.EqualFold(row.Name, "etc") {
				values := sectionTokens(doc, row)
				if len(values) >= 2 && values[1].Kind == dnfpvf.TokenString && values[1].Value == "add day of dungeon enter count" {
					if len(values) != 5 || values[0].Kind != dnfpvf.TokenInt || values[0].Int < 1 || values[0].Int > int64(level) || values[2].Kind != dnfpvf.TokenInt || values[2].Int < 0 || values[2].Int > 5 || values[3].Kind != dnfpvf.TokenString || values[4].Kind != dnfpvf.TokenInt || values[4].Int != 1 {
						return nil, fmt.Errorf("%w: day-dungeon benefit at level %d", ErrTableMalformed, level)
					}
					if found {
						return nil, fmt.Errorf("%w: duplicate day-dungeon benefit", ErrTableMalformed)
					}
					bonus, found = values[2].Int, true
				}
			}
			end++
		}
		if end == len(doc.Sections) {
			return nil, fmt.Errorf("%w: missing attribute end", ErrTableMalformed)
		}
		out[level] = bonus
		index = end
	}
	return out, nil
}
