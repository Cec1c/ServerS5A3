package raid

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
)

// LukeProgram compiles the actual Luke graph. Only the low-level literal,
// symbol journal and timer executor is shared; no Anton game rules are loaded.
type LukeProgram struct{ executor *AntonProgram }
type LukeProgression struct{ *AntonProgression }

func LoadLukeProgram(source dnfpvf.Source) (*LukeProgram, error) {
	const ref = "etc/raid/luke/luke.etc"
	config, err := LoadBattleConfiguration(source, ref)
	if err != nil {
		return nil, err
	}
	if len(config.PhaseDungeons) != 2 {
		return nil, fmt.Errorf("Luke must have its configured two phases")
	}
	text, err := source.ReadText(ref)
	if err != nil {
		return nil, err
	}
	doc, err := dnfpvf.Parse(ref, text)
	if err != nil {
		return nil, err
	}
	symbols, err := loadRaidSymbols(source, ref+".symbol")
	if err != nil {
		return nil, err
	}
	p := &AntonProgram{luke: true, config: config, symbols: symbols, sanctumSymbols: map[uint32]string{}}
	phase, dungeon := -1, uint32(0)
	inPhase, inObject := false, false
	for i := 0; i < len(doc.Sections); i++ {
		s := doc.Sections[i]
		name := strings.ToUpper(strings.TrimSpace(s.Name))
		values := sectionValues(doc, s)
		switch name {
		case "PHASE":
			phase++
			inPhase = true
			p.phases = append(p.phases, antonPhaseProgram{hp: map[uint32]uint32{}, partyMax: map[uint32]uint32{}})
		case "/PHASE":
			inPhase = false
		case "DUNGEON INFO":
			dungeon = 0
			inObject = false
		case "/DUNGEON INFO":
			dungeon = 0
			inObject = false
		case "INDEX":
			if inPhase && len(values) == 1 {
				dungeon, err = antonUint(values[0])
				if err != nil {
					return nil, err
				}
			}
		case "DESTROY OBJECT INFO":
			inObject = true
		case "PARTY MAX":
			if inPhase && dungeon != 0 {
				if len(values) != 1 {
					return nil, fmt.Errorf("Luke party limit missing")
				}
				limit, e := antonUint(values[0])
				if e != nil || limit == 0 || limit > 2 || p.phases[phase].partyMax[dungeon] != 0 {
					return nil, fmt.Errorf("Luke party limit invalid")
				}
				p.phases[phase].partyMax[dungeon] = limit
			}
		case "TRIGGER":
			if !inPhase {
				return nil, fmt.Errorf("Luke trigger outside phase")
			}
			rule := antonRule{}
			for i++; i < len(doc.Sections) && !strings.EqualFold(doc.Sections[i].Name, "/TRIGGER"); i++ {
				c, n, e := compileLukeClause(doc, i, false)
				if e != nil {
					return nil, e
				}
				i += n
				if c.kind != "" {
					rule.conditions = append(rule.conditions, c)
				}
			}
			if i+1 >= len(doc.Sections) || !strings.EqualFold(doc.Sections[i+1].Name, "BEHAVIOR") || len(rule.conditions) == 0 {
				return nil, fmt.Errorf("Luke trigger has no behavior")
			}
			for i += 2; i < len(doc.Sections) && !strings.EqualFold(doc.Sections[i].Name, "/BEHAVIOR"); i++ {
				c, n, e := compileLukeClause(doc, i, true)
				if e != nil {
					return nil, e
				}
				i += n
				rule.actions = append(rule.actions, c)
			}
			if i >= len(doc.Sections) || len(rule.actions) == 0 {
				return nil, fmt.Errorf("Luke behavior incomplete")
			}
			p.phases[phase].rules = append(p.phases[phase].rules, rule)
		default:
			if inPhase && dungeon != 0 && name == strconv.FormatUint(uint64(dungeon), 10)+" DGN HP" {
				if len(values) != 1 {
					return nil, fmt.Errorf("Luke dungeon HP missing")
				}
				hp, e := antonUint(values[0])
				if e != nil || hp == 0 || hp > 255 || p.phases[phase].hp[dungeon] != 0 {
					return nil, fmt.Errorf("invalid Luke dungeon HP")
				}
				p.phases[phase].hp[dungeon] = hp
			} else if inObject && dungeon != 0 && strings.HasSuffix(name, " SANCTUM OBJECT") {
				if len(values) != 0 || symbols[name] == 0 || p.sanctumSymbols[dungeon] != "" {
					return nil, fmt.Errorf("invalid Luke sanctum object symbol")
				}
				p.sanctumSymbols[dungeon] = name
			}
		}
	}
	if inPhase || len(p.phases) != 2 {
		return nil, fmt.Errorf("Luke phase graph incomplete")
	}
	known := map[uint32]bool{}
	for n, phase := range config.PhaseDungeons {
		for _, d := range phase {
			known[d.ID] = true
			if p.phases[n].hp[d.ID] == 0 || p.phases[n].partyMax[d.ID] == 0 || symbols[fmt.Sprintf("%d DGN HP", d.ID)] == 0 {
				return nil, fmt.Errorf("Luke HP dependency %d missing", d.ID)
			}
		}
	}
	for n, phase := range p.phases {
		phaseIDs := map[uint32]bool{}
		for _, d := range config.PhaseDungeons[n] {
			phaseIDs[d.ID] = true
		}
		for _, rule := range phase.rules {
			for _, group := range [][]antonClause{rule.conditions, rule.actions} {
				for _, c := range group {
					if c.symbol != "" && symbols[c.symbol] == 0 {
						return nil, fmt.Errorf("Luke unknown native symbol %s", c.symbol)
					}
					if c.id != 0 && c.kind != "ON ENTER MAP" && c.kind != "PHASE END" {
						if c.timer != 0 {
							if !known[c.id] {
								return nil, fmt.Errorf("Luke timer references unknown dungeon %d", c.id)
							}
						} else if !phaseIDs[c.id] {
							return nil, fmt.Errorf("Luke directive references foreign phase dungeon %d", c.id)
						}
					}
					if c.kind == "RESET SANCTUM OBJECT" && p.sanctumSymbols[c.id] == "" {
						return nil, fmt.Errorf("Luke missing sanctum owner")
					}
					if c.kind == "PHASE END" && c.id != uint32(n) {
						return nil, fmt.Errorf("Luke phase-end owner mismatch")
					}
				}
			}
		}
	}
	p.lukeGenerators, err = loadLukeGeneratorConfiguration(source, doc, p.sanctumSymbols, p.symbols)
	if err != nil {
		return nil, err
	}
	return &LukeProgram{executor: p}, nil
}

func compileLukeClause(doc *dnfpvf.Document, index int, action bool) (antonClause, int, error) {
	s := doc.Sections[index]
	name := strings.ToUpper(strings.TrimSpace(s.Name))
	v := sectionValues(doc, s)
	bad := func() (antonClause, int, error) {
		return antonClause{}, 0, fmt.Errorf("unsupported Luke directive %q", name)
	}
	if action && name == "SET" {
		if len(v) != 0 || index+1 >= len(doc.Sections) {
			return bad()
		}
		next := doc.Sections[index+1]
		nv := sectionValues(doc, next)
		c := antonClause{kind: "SET", symbol: strings.ToUpper(strings.TrimSpace(next.Name))}
		used := 1
		if len(nv) == 0 {
			if index+2 >= len(doc.Sections) || doc.Sections[index+2].Name != "+" {
				return bad()
			}
			nv = sectionValues(doc, doc.Sections[index+2])
			c.add = true
			used = 2
		}
		if len(nv) != 1 {
			return bad()
		}
		value, err := antonUint(nv[0])
		if err != nil {
			return bad()
		}
		c.value = value
		return c, used, nil
	}
	switch name {
	case "ON ENTER DUNGEON", "ON GIVEUP DUNGEON", "ON ENTER MAP", "PHASE END", "RESET SANCTUM OBJECT":
		if action != (name == "RESET SANCTUM OBJECT") || len(v) != 1 {
			return bad()
		}
		id, err := antonUint(v[0])
		if err != nil || (id == 0 && name != "PHASE END") {
			return bad()
		}
		return antonClause{kind: name, id: id}, 0, nil
	case "FAIL PHASE":
		if !action || len(v) != 0 {
			return bad()
		}
		return antonClause{kind: name}, 0, nil
	case "OPEN RANDOM HATCHERY", "RESERVE DUNGEON STATE":
		return bad()
	}
	return compileAntonClause(doc, index, action)
}

func (p *LukeProgram) BeginPhase(now time.Time, seed uint64) (*LukeProgression, error) {
	if p == nil || p.executor == nil {
		return nil, fmt.Errorf("Luke program unavailable")
	}
	s, err := p.executor.BeginPhase(0, now, seed)
	if err != nil {
		return nil, err
	}
	return &LukeProgression{s}, nil
}

func (s *LukeProgression) Clone() *LukeProgression {
	if s == nil {
		return nil
	}
	return &LukeProgression{s.AntonProgression.Clone()}
}
func (s *LukeProgression) Tick(now time.Time) (*LukeProgression, error) {
	if s == nil {
		return nil, fmt.Errorf("Luke instance unavailable")
	}
	n, e := s.AntonProgression.Tick(now)
	if e != nil {
		return nil, e
	}
	return &LukeProgression{n}, nil
}
func (s *LukeProgression) ClearDungeon(run string, id uint32, epoch uint64, now time.Time) (*LukeProgression, bool, error) {
	if s == nil || !s.IsLuke() || epoch != s.Epoch() {
		return nil, false, fmt.Errorf("Luke instance unavailable")
	}
	if old, ok := s.completedLukeRuns[run]; ok {
		if old != id {
			return nil, false, fmt.Errorf("Luke clear replay changed dungeon")
		}
		return s.Clone(), true, nil
	}
	if !s.HasEnteredRun(run, id) {
		return nil, false, fmt.Errorf("Luke clear has no accepted owned entry")
	}
	n, replay, e := s.AntonProgression.ClearDungeon(run, id, epoch, now)
	if e != nil {
		return nil, false, e
	}
	delete(n.enteredRuns, run)
	if n.completedLukeRuns == nil {
		n.completedLukeRuns = map[string]uint32{}
	}
	n.completedLukeRuns[run] = id
	return &LukeProgression{n}, replay, nil
}

func (s *LukeProgression) event(kind string, id uint32, now time.Time) (*LukeProgression, error) {
	if s == nil || !s.IsLuke() || s.PhaseCleared() || s.PhaseFailed() {
		return nil, fmt.Errorf("Luke event has no active phase")
	}
	n, err := s.Tick(now)
	if err != nil {
		return nil, err
	}
	if n.PhaseCleared() || n.PhaseFailed() {
		return nil, fmt.Errorf("Luke event phase ended while advancing clock")
	}
	if kind == "ON ENTER DUNGEON" {
		if state, ok := n.dungeons[id]; !ok || state != 0 {
			return nil, fmt.Errorf("Luke entry dungeon closed while advancing clock")
		}
	}
	if err := n.evaluate(now, []antonEvent{{kind: kind, id: id}}); err != nil {
		return nil, err
	}
	return n, nil
}

func (s *LukeProgression) EnterDungeon(run string, id uint32, epoch uint64, now time.Time) (*LukeProgression, bool, error) {
	if s == nil || !s.IsLuke() || run == "" || len(run) > 128 || epoch != s.Epoch() {
		return nil, false, fmt.Errorf("Luke entry identity unavailable")
	}
	if existing, ok := s.enteredRuns[run]; ok {
		if existing != id {
			return nil, false, fmt.Errorf("Luke entry replay changed dungeon")
		}
		return s.Clone(), true, nil
	}
	if len(s.enteredRuns)+len(s.abandonedRuns)+len(s.completedLukeRuns)+len(s.preparedLukeRuns) >= 4096 && s.preparedLukeRuns[run] == 0 || s.abandonedRuns[run] != 0 || s.completedLukeRuns[run] != 0 {
		return nil, false, fmt.Errorf("Luke retired entry or journal limit")
	}
	var active uint32
	for _, d := range s.enteredRuns {
		if d == id {
			active++
		}
	}
	for key, d := range s.preparedLukeRuns {
		if key != run && d == id {
			active++
		}
	}
	if limit := s.program.phases[s.phase].partyMax[id]; limit == 0 || active >= limit {
		return nil, false, fmt.Errorf("Luke configured dungeon party limit reached")
	}
	if state, ok := s.dungeons[id]; !ok || state != 0 {
		return nil, false, fmt.Errorf("Luke entry dungeon is not open")
	}
	n, err := s.event("ON ENTER DUNGEON", id, now)
	if err != nil {
		return nil, false, err
	}
	if n.enteredRuns == nil {
		n.enteredRuns = map[string]uint32{}
	}
	n.enteredRuns[run] = id
	delete(n.preparedLukeRuns, run)
	return n, false, nil
}

// Reservation enforces the actual PARTY MAX before any entry commitment. It
// never executes ON ENTER DUNGEON. Only a committed scene accepts that event.
func (s *LukeProgression) ReserveDungeon(run string, id uint32, epoch uint64, now time.Time) (*LukeProgression, error) {
	if s == nil || !s.IsLuke() || run == "" || len(run) > 128 || epoch != s.Epoch() || s.PhaseCleared() || s.PhaseFailed() || now.Before(s.LastTime()) {
		return nil, fmt.Errorf("Luke reservation identity unavailable")
	}
	if previous := s.preparedLukeRuns[run]; previous != 0 {
		if previous != id {
			return nil, fmt.Errorf("Luke prepared replay changed dungeon")
		}
		return s.Clone(), nil
	}
	if previous := s.enteredRuns[run]; previous != 0 {
		if previous != id {
			return nil, fmt.Errorf("Luke accepted replay changed dungeon")
		}
		return s.Clone(), nil
	}
	if s.abandonedRuns[run] != 0 || s.completedLukeRuns[run] != 0 || len(s.preparedLukeRuns)+len(s.enteredRuns)+len(s.abandonedRuns)+len(s.completedLukeRuns) >= 4096 {
		return nil, fmt.Errorf("Luke reservation retired or journal full")
	}
	if state, ok := s.dungeons[id]; !ok || state != 0 {
		return nil, fmt.Errorf("Luke reservation dungeon not open")
	}
	var active uint32
	for _, d := range s.enteredRuns {
		if d == id {
			active++
		}
	}
	for _, d := range s.preparedLukeRuns {
		if d == id {
			active++
		}
	}
	if limit := s.program.phases[s.phase].partyMax[id]; limit == 0 || active >= limit {
		return nil, fmt.Errorf("Luke configured dungeon party limit reached")
	}
	n := s.Clone()
	if n.preparedLukeRuns == nil {
		n.preparedLukeRuns = map[string]uint32{}
	}
	n.preparedLukeRuns[run] = id
	return n, nil
}

func (s *LukeProgression) CancelReservation(run string, id uint32) (*LukeProgression, error) {
	if s == nil || !s.IsLuke() || run == "" {
		return nil, fmt.Errorf("Luke cancellation identity unavailable")
	}
	if old := s.preparedLukeRuns[run]; old != 0 && old != id {
		return nil, fmt.Errorf("Luke cancellation owner changed")
	}
	n := s.Clone()
	delete(n.preparedLukeRuns, run)
	return n, nil
}

func (s *LukeProgression) GiveUpDungeon(run string, id uint32, now time.Time) (*LukeProgression, error) {
	if s != nil && s.abandonedRuns[run] == id && id != 0 {
		return s.Clone(), nil
	}
	if s == nil || run == "" || s.enteredRuns[run] != id {
		return nil, fmt.Errorf("Luke giveup has no owned entry")
	}
	n, err := s.event("ON GIVEUP DUNGEON", id, now)
	if err != nil {
		return nil, err
	}
	delete(n.enteredRuns, run)
	if n.abandonedRuns == nil {
		n.abandonedRuns = map[string]uint32{}
	}
	n.abandonedRuns[run] = id
	return n, nil
}

func (s *LukeProgression) HasEnteredRun(run string, id uint32) bool {
	return s != nil && run != "" && id != 0 && s.enteredRuns[run] == id
}

// The bridge must supply only maps from its authenticated, committed runtime.
// The compiler accepts only the two explicit ON ENTER MAP directives, avoiding
// a client-supplied symbol mutation API.
func (s *LukeProgression) EnterMap(id uint32, now time.Time) (*LukeProgression, error) {
	if s == nil || id == 0 {
		return nil, fmt.Errorf("Luke map context unavailable")
	}
	known := false
	for _, r := range s.program.phases[s.phase].rules {
		for _, c := range r.conditions {
			if c.kind == "ON ENTER MAP" && c.id == id {
				known = true
			}
		}
	}
	if !known {
		return s.Clone(), nil
	}
	return s.event("ON ENTER MAP", id, now)
}

func (s *LukeProgression) EndPhase(now time.Time) (*LukeProgression, error) {
	if s == nil {
		return nil, fmt.Errorf("Luke phase unavailable")
	}
	return s.event("PHASE END", uint32(s.phase), now)
}

func (s *LukeProgression) NextPhase(now time.Time) (*LukeProgression, error) {
	if s == nil || s.phase != 0 || !s.PhaseCleared() || s.PhaseFailed() || now.Before(s.last) {
		return nil, fmt.Errorf("Luke first phase incomplete")
	}
	n, err := s.program.BeginPhase(1, now, s.random)
	if err != nil {
		return nil, err
	}
	// The chosen light/dark path is an authoritative first-phase outcome.
	// Carry it as state, not as a replay of prior instruction symbols.
	n.assignSymbolValue("2PHASE DUNGEON TYPE", s.symbols["2PHASE DUNGEON TYPE"])
	if err := carryLukeGeneratorTier(s.AntonProgression, n); err != nil {
		return nil, err
	}
	return &LukeProgression{n}, nil
}
