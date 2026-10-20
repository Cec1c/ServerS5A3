package raid

import (
	"fmt"
	"strings"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
)

// FiendWarProgram keeps battlefield events and string area states separate
// from the normal raid's dungeon state bytes. These literals do not establish
// a client packet representation or authorize a client to change symbols.
type FiendWarProgram struct {
	configuration FiendWarConfiguration
	rules         []fiendWarRule
}

type fiendWarClause struct {
	kind, symbol, text string
	id, target, value  uint32
}

type fiendWarRule struct{ conditions, actions []fiendWarClause }

func LoadFiendWarProgram(source dnfpvf.Source) (*FiendWarProgram, error) {
	const ref = "etc/raid/fiendwar/fiendwar.etc"
	configuration, err := LoadFiendWarConfiguration(source)
	if err != nil {
		return nil, err
	}
	text, err := source.ReadText(ref)
	if err != nil {
		return nil, err
	}
	doc, err := dnfpvf.Parse(ref, text)
	if err != nil {
		return nil, err
	}
	p := &FiendWarProgram{configuration: configuration}
	areas, dungeons := map[uint32]bool{}, map[uint32]bool{}
	for _, a := range configuration.Areas {
		areas[a.ID] = true
		if a.DungeonID > 0 {
			dungeons[uint32(a.DungeonID)] = true
		}
	}
	inPhase, phases := false, 0
	for i := 0; i < len(doc.Sections); i++ {
		name := strings.ToUpper(strings.TrimSpace(doc.Sections[i].Name))
		switch name {
		case "PHASE":
			if inPhase || phases != 0 {
				return nil, fmt.Errorf("Fiend War requires one phase")
			}
			inPhase, phases = true, phases+1
		case "/PHASE":
			if !inPhase {
				return nil, fmt.Errorf("Fiend War phase close without phase")
			}
			inPhase = false
		case "TRIGGER":
			if !inPhase {
				return nil, fmt.Errorf("Fiend War trigger outside phase")
			}
			rule := fiendWarRule{}
			for i++; i < len(doc.Sections) && !strings.EqualFold(doc.Sections[i].Name, "/TRIGGER"); i++ {
				c, used, e := compileFiendWarClause(doc, i, false, configuration.Symbols, areas, dungeons)
				if e != nil {
					return nil, e
				}
				i += used
				rule.conditions = append(rule.conditions, c)
			}
			if len(rule.conditions) == 0 || i+1 >= len(doc.Sections) || !strings.EqualFold(doc.Sections[i+1].Name, "BEHAVIOR") {
				return nil, fmt.Errorf("Fiend War trigger missing behavior")
			}
			for i += 2; i < len(doc.Sections) && !strings.EqualFold(doc.Sections[i].Name, "/BEHAVIOR"); i++ {
				c, used, e := compileFiendWarClause(doc, i, true, configuration.Symbols, areas, dungeons)
				if e != nil {
					return nil, e
				}
				i += used
				rule.actions = append(rule.actions, c)
			}
			if i >= len(doc.Sections) || len(rule.actions) == 0 {
				return nil, fmt.Errorf("Fiend War behavior incomplete")
			}
			p.rules = append(p.rules, rule)
		case "BEHAVIOR", "/TRIGGER", "/BEHAVIOR":
			return nil, fmt.Errorf("Fiend War unmatched rule boundary %s", name)
		}
	}
	if inPhase || phases != 1 || len(p.rules) == 0 {
		return nil, fmt.Errorf("Fiend War phase program unavailable")
	}
	return p, nil
}

func compileFiendWarClause(doc *dnfpvf.Document, index int, action bool, symbols map[string]uint32, areas, dungeons map[uint32]bool) (fiendWarClause, int, error) {
	s := doc.Sections[index]
	name := strings.ToUpper(strings.TrimSpace(s.Name))
	v := sectionValues(doc, s)
	c, used := fiendWarClause{kind: name}, 0
	bad := func() (fiendWarClause, int, error) {
		return fiendWarClause{}, 0, fmt.Errorf("unsupported Fiend War directive %q", name)
	}
	if name == "IF" || name == "SET" || name == "ON CHANGE SYMBOL" {
		if action != (name == "SET") || len(v) != 0 || index+1 >= len(doc.Sections) {
			return bad()
		}
		next := doc.Sections[index+1]
		c.symbol = strings.ToUpper(strings.TrimSpace(next.Name))
		if _, exists := symbols[c.symbol]; !exists {
			return bad()
		}
		used, v = 1, sectionValues(doc, next)
		if name == "ON CHANGE SYMBOL" {
			if len(v) != 0 {
				return bad()
			}
			return c, used, nil
		}
		if name == "IF" && len(v) == 0 {
			if index+2 >= len(doc.Sections) || strings.TrimSpace(doc.Sections[index+2].Name) != "<" {
				return bad()
			}
			c.kind, used, v = "IF LESS", 2, sectionValues(doc, doc.Sections[index+2])
		}
		if len(v) != 1 {
			return bad()
		}
		value, err := antonUint(v[0])
		if err != nil {
			return bad()
		}
		c.value = value
		return c, used, nil
	}
	if name == "SET TIMER" || name == "RESET TIMER" || name == "CHECK TIMER END" {
		n := 2
		if name == "SET TIMER" {
			n = 3
		}
		if action != (name != "CHECK TIMER END") || len(v) != n || v[0].Kind != dnfpvf.TokenString || strings.TrimSpace(v[0].Value) == "" {
			return bad()
		}
		id, err := antonUint(v[1])
		if err != nil || !areas[id] {
			return bad()
		}
		c.text, c.id = v[0].Value, id
		if n == 3 {
			c.value, err = antonUint(v[2])
			if err != nil || c.value == 0 {
				return bad()
			}
		}
		return c, 0, nil
	}
	if name == "CHECK AREA STATE" || name == "CHECK DUNGEON STATE" {
		if action || len(v) != 2 || v[1].Kind != dnfpvf.TokenString {
			return bad()
		}
		id, err := antonUint(v[0])
		if err != nil {
			return bad()
		}
		c.id, c.text = id, v[1].Value
		if name == "CHECK AREA STATE" {
			if !areas[id] || (c.text != "enemy" && c.text != "enemy_monster" && c.text != "empty") {
				return bad()
			}
		} else if !dungeons[id] || c.text != "clear" {
			return bad()
		}
		return c, 0, nil
	}
	if name == "MOVE ENEMY" {
		if !action || len(v) != 2 {
			return bad()
		}
		var err error
		c.id, err = antonUint(v[0])
		if err != nil || !areas[c.id] {
			return bad()
		}
		c.target, err = antonUint(v[1])
		if err != nil || !areas[c.target] || c.id == c.target {
			return bad()
		}
		return c, 0, nil
	}
	switch name {
	case "ON CHANGE AREA STATE", "ON GIVEUP AREA", "CREATE ENEMY", "WARNING ENEMY", "DELETE MONSTER":
		if action != (name == "CREATE ENEMY" || name == "WARNING ENEMY" || name == "DELETE MONSTER") || len(v) != 1 {
			return bad()
		}
		id, err := antonUint(v[0])
		if err != nil || !areas[id] {
			return bad()
		}
		c.id = id
	case "ON ENTER DUNGEON", "ON GIVEUP DUNGEON", "ON CHANGE DUNGEON STATE", "ON ENTER MAP":
		if action || len(v) != 1 {
			return bad()
		}
		id, err := antonUint(v[0])
		if err != nil || id == 0 || (name != "ON ENTER MAP" && !dungeons[id]) {
			return bad()
		}
		c.id = id
	case "BOSS PATTERN INIT", "CLEAR PHASE", "CREATE MERCHANT", "CREATE MONSTERS", "DELETE ALL ENEMY", "FAIL FIENDWAR":
		if !action || len(v) != 0 {
			return bad()
		}
	default:
		return bad()
	}
	return c, 0, nil
}
