package raid

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
)

// AntonProgram compiles the actual normal-raid PVF directives. It deliberately
// has no fallback maps, clear thresholds or timer durations. Transport and
// eligibility/reward transactions are separate owners.
type AntonProgram struct {
	// Luke uses the shared literal/timer/symbol executor with its own compiler.
	// None of the Anton hatchery or barrier rules are loaded for that family.
	luke           bool
	sanctumSymbols map[uint32]string
	lukeGenerators *lukeGeneratorConfiguration
	config         BattleConfiguration
	phases         []antonPhaseProgram
	hatcheries     []uint32
	hatcheryOpen   int
	symbols        map[string]uint32
	barrier        AntonBarrierConfiguration
}

type antonPhaseProgram struct {
	hp       map[uint32]uint32
	partyMax map[uint32]uint32
	rules    []antonRule
}

type antonClause struct {
	add    bool
	kind   string
	id     uint32
	state  byte
	timer  byte
	value  uint32
	symbol string
}

type antonRule struct{ conditions, actions []antonClause }

func antonState(value string) (byte, error) {
	switch strings.ToLower(value) {
	case "open":
		return 0, nil
	case "close":
		return 1, nil
	case "hide":
		return 2, nil
	case "clear":
		return 3, nil
	default:
		return 0, fmt.Errorf("unknown Anton dungeon state %q", value)
	}
}

func antonUint(token dnfpvf.Token) (uint32, error) {
	if token.Kind != dnfpvf.TokenInt || token.Int < 0 || token.Int > math.MaxUint32 {
		return 0, fmt.Errorf("invalid Anton integer %q", token.Raw)
	}
	return uint32(token.Int), nil
}

func LoadAntonProgram(source dnfpvf.Source) (*AntonProgram, error) {
	const path = "etc/raid/anton.etc"
	config, err := LoadBattleConfiguration(source, path)
	if err != nil {
		return nil, err
	}
	text, err := source.ReadText(path)
	if err != nil {
		return nil, err
	}
	doc, err := dnfpvf.Parse(path, text)
	if err != nil {
		return nil, err
	}
	p := &AntonProgram{config: config}
	p.symbols, err = LoadAntonSymbols(source)
	if err != nil {
		return nil, err
	}
	p.barrier, err = LoadAntonBarrierConfiguration(source)
	if err != nil {
		return nil, err
	}
	for name, id := range map[string]uint32{"BLACK VOLCANO BARRIER": 110, "INFECTION DUNGEON INDEX": 8, "IS EXIST INFECTION DUNGEON": 7, "MOVIE BARRIER BREAK": 126, "MOVIE BARRIER RECOVER": 127} {
		if p.symbols[name] != id {
			return nil, fmt.Errorf("Anton barrier action symbol %s does not match formal action ID %d", name, id)
		}
	}
	hatch := doc.Ints("HATCHERY INFO")
	if len(hatch) < 3 || hatch[0] != int64(len(hatch)-2) || hatch[1] <= 0 || hatch[1] > hatch[0] {
		return nil, fmt.Errorf("Anton hatchery configuration unavailable")
	}
	seen := map[uint32]bool{}
	for _, id := range hatch[2:] {
		if id <= 0 || id > math.MaxUint32 || seen[uint32(id)] {
			return nil, fmt.Errorf("invalid Anton hatchery")
		}
		seen[uint32(id)] = true
		p.hatcheries = append(p.hatcheries, uint32(id))
	}
	p.hatcheryOpen = int(hatch[1])
	phase, dungeon := -1, uint32(0)
	inPhase := false
	for i := 0; i < len(doc.Sections); i++ {
		section := doc.Sections[i]
		name := strings.ToUpper(strings.TrimSpace(section.Name))
		values := sectionValues(doc, section)
		switch name {
		case "PHASE":
			phase++
			inPhase = true
			p.phases = append(p.phases, antonPhaseProgram{hp: map[uint32]uint32{}})
		case "/PHASE":
			inPhase = false
		case "DUNGEON INFO":
			dungeon = 0
		case "/DUNGEON INFO":
			dungeon = 0
		case "INDEX":
			if inPhase && len(values) == 1 {
				dungeon, err = antonUint(values[0])
				if err != nil {
					return nil, err
				}
			}
		case "TRIGGER":
			if !inPhase {
				return nil, fmt.Errorf("Anton trigger outside phase")
			}
			rule := antonRule{}
			for i++; i < len(doc.Sections) && !strings.EqualFold(doc.Sections[i].Name, "/TRIGGER"); i++ {
				clause, used, err := compileAntonClause(doc, i, false)
				if err != nil {
					return nil, err
				}
				i += used
				if clause.kind != "" {
					rule.conditions = append(rule.conditions, clause)
				}
			}
			if i+1 >= len(doc.Sections) || !strings.EqualFold(doc.Sections[i+1].Name, "BEHAVIOR") || len(rule.conditions) == 0 {
				return nil, fmt.Errorf("incomplete Anton trigger/behavior")
			}
			for i += 2; i < len(doc.Sections) && !strings.EqualFold(doc.Sections[i].Name, "/BEHAVIOR"); i++ {
				clause, used, err := compileAntonClause(doc, i, true)
				if err != nil {
					return nil, err
				}
				i += used
				rule.actions = append(rule.actions, clause)
			}
			if i >= len(doc.Sections) || len(rule.actions) == 0 {
				return nil, fmt.Errorf("incomplete Anton behavior")
			}
			p.phases[phase].rules = append(p.phases[phase].rules, rule)
		default:
			if inPhase && dungeon != 0 && name == strconv.FormatUint(uint64(dungeon), 10)+" DGN HP" {
				if len(values) != 1 {
					return nil, fmt.Errorf("Anton clear threshold unavailable")
				}
				hp, err := antonUint(values[0])
				if err != nil || hp == 0 || hp > 255 || p.phases[phase].hp[dungeon] != 0 {
					return nil, fmt.Errorf("invalid Anton clear threshold")
				}
				p.phases[phase].hp[dungeon] = hp
			}
		}
	}
	if len(p.phases) != len(config.PhaseDungeons) {
		return nil, fmt.Errorf("Anton program phase mismatch")
	}
	for index, phase := range p.phases {
		ids := map[uint32]bool{}
		for _, d := range config.PhaseDungeons[index] {
			ids[d.ID] = true
			if phase.hp[d.ID] == 0 {
				return nil, fmt.Errorf("Anton dungeon %d lacks clear threshold", d.ID)
			}
			if p.symbols[strconv.FormatUint(uint64(d.ID), 10)+" DGN HP"] == 0 {
				return nil, fmt.Errorf("Anton dungeon %d lacks native HP symbol", d.ID)
			}
		}
		for _, rule := range phase.rules {
			for _, group := range [][]antonClause{rule.conditions, rule.actions} {
				for _, clause := range group {
					if clause.symbol != "" && p.symbols[clause.symbol] == 0 {
						return nil, fmt.Errorf("Anton directive references unknown symbol %q", clause.symbol)
					}
					if clause.id != 0 && !ids[clause.id] {
						return nil, fmt.Errorf("Anton directive references foreign phase dungeon %d", clause.id)
					}
				}
			}
		}
	}
	for _, id := range p.hatcheries {
		if p.phases[len(p.phases)-1].hp[id] == 0 {
			return nil, fmt.Errorf("Anton hatchery outside final phase")
		}
		for _, suffix := range []string{" HATCHERY FAIL", " HATCHERY NAMED CREATE"} {
			if p.symbols[strconv.FormatUint(uint64(id), 10)+suffix] == 0 {
				return nil, fmt.Errorf("Anton hatchery %d lacks native symbol %s", id, suffix)
			}
		}
	}
	for _, name := range []string{"IS EXIST INFECTION DUNGEON", "INFECTION DUNGEON INDEX"} {
		if p.symbols[name] == 0 {
			return nil, fmt.Errorf("Anton native symbol missing: %s", name)
		}
	}
	return p, nil
}

func compileAntonClause(doc *dnfpvf.Document, index int, action bool) (antonClause, int, error) {
	section := doc.Sections[index]
	name := strings.ToUpper(strings.TrimSpace(section.Name))
	values := sectionValues(doc, section)
	c := antonClause{kind: name}
	bad := func() (antonClause, int, error) {
		return antonClause{}, 0, fmt.Errorf("unsupported Anton %s directive %q", map[bool]string{true: "behavior", false: "trigger"}[action], name)
	}
	if !action && name == "IF" && len(values) == 0 {
		return antonClause{}, 0, nil
	}
	if name == "SET" && action || name == "ON CHANGE SYMBOL" && !action {
		if len(values) != 0 || index+1 >= len(doc.Sections) {
			return bad()
		}
		next := doc.Sections[index+1]
		c.symbol = strings.ToUpper(strings.TrimSpace(next.Name))
		n := sectionValues(doc, next)
		if c.symbol == "" || strings.ContainsAny(c.symbol, "/[]") {
			return bad()
		}
		if action {
			if len(n) != 1 {
				return bad()
			}
			value, err := antonUint(n[0])
			if err != nil {
				return bad()
			}
			c.value = value
		} else if len(n) != 0 {
			return bad()
		}
		return c, 1, nil
	}
	switch name {
	case "CHECK DUNGEON STATE", "SET DUNGEON STATE", "RESERVE DUNGEON STATE":
		if (action && name == "CHECK DUNGEON STATE") || (!action && name != "CHECK DUNGEON STATE") {
			return bad()
		}
		count := 2
		if name == "RESERVE DUNGEON STATE" {
			count = 3
		}
		if len(values) != count {
			return bad()
		}
		var err error
		c.id, err = antonUint(values[0])
		if err != nil || c.id == 0 {
			return bad()
		}
		c.state, err = antonState(values[1].Value)
		if err != nil {
			return bad()
		}
		if count == 3 {
			c.value, err = antonUint(values[2])
			if err != nil || c.value == 0 || c.value > 86400 {
				return bad()
			}
		}
	case "ON CHANGE DUNGEON STATE":
		if action || len(values) != 1 {
			return bad()
		}
		var err error
		c.id, err = antonUint(values[0])
		if err != nil || c.id == 0 {
			return bad()
		}
	case "SET TIMER", "RESET TIMER", "CHECK TIMER END":
		if action == (name == "CHECK TIMER END") {
			return bad()
		}
		count := 2
		if name == "SET TIMER" {
			count = 3
		}
		if len(values) != count {
			return bad()
		}
		timer, err := antonUint(values[0])
		if err != nil || timer < 1 || timer > 3 {
			return bad()
		}
		c.timer = byte(timer)
		c.id, err = antonUint(values[1])
		if err != nil || c.id == 0 {
			return bad()
		}
		if count == 3 {
			c.value, err = antonUint(values[2])
			if err != nil || c.value == 0 || c.value > 86400 {
				return bad()
			}
		}
	case "CLEAR PHASE", "OPEN RANDOM HATCHERY":
		if !action || len(values) != 0 {
			return bad()
		}
	default:
		// In this PVF's IF block, a named symbol followed by one literal is
		// an equality predicate. Arithmetic/operators must not be ignored.
		if action || len(values) != 1 || strings.ContainsAny(name, "/[]+") {
			return bad()
		}
		value, err := antonUint(values[0])
		if err != nil {
			return bad()
		}
		c.kind, c.symbol, c.value = "SYMBOL EQUAL", name, value
	}
	return c, 0, nil
}
