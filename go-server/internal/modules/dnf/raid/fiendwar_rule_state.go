package raid

import (
	"fmt"
	"sort"
	"time"
)

// This is the server-side literal rule evaluator, not a packet handler or an
// admission API. Battlefield ownership, combat and generated enemies must
// validate an event before supplying it here. Nonliteral behavior is returned
// in order for its owner to execute; it is never guessed from a state byte.
type fiendWarRuleState struct {
	program         *FiendWarProgram
	symbols         map[string]uint32
	areas, dungeons map[uint32]string
	timers          map[fiendWarTimerKey]time.Time
	last, deadline  time.Time
	cleared, failed bool
}

type fiendWarTimerKey struct {
	name string
	area uint32
}

type fiendWarEvent struct {
	kind, symbol string
	id           uint32
	timer        fiendWarTimerKey
}

func (p *FiendWarProgram) beginRules(now time.Time) (*fiendWarRuleState, []fiendWarClause, error) {
	if p == nil || now.IsZero() {
		return nil, nil, fmt.Errorf("Fiend War program or start clock missing")
	}
	s := &fiendWarRuleState{program: p, symbols: map[string]uint32{}, areas: map[uint32]string{}, dungeons: map[uint32]string{}, timers: map[fiendWarTimerKey]time.Time{}, last: now,
		deadline: now.Add(time.Duration(p.configuration.Battle.PhaseLimitSeconds[0]) * time.Second)}
	for name := range p.configuration.Symbols {
		s.symbols[name] = 0
	}
	for _, a := range p.configuration.Areas {
		s.areas[a.ID] = a.InitialState
	}
	for _, d := range p.configuration.Battle.PhaseDungeons[0] {
		// The configuration already validates the normal dungeon states.
		for name, value := range map[string]byte{"open": 0, "close": 1, "hide": 2, "clear": 3} {
			if d.State == value {
				s.dungeons[d.ID] = name
			}
		}
	}
	commands, err := s.evaluate(now, []fiendWarEvent{{kind: "initial"}})
	return s, commands, err
}

func (s *fiendWarRuleState) clone() *fiendWarRuleState {
	n := *s
	n.symbols, n.areas, n.dungeons, n.timers = map[string]uint32{}, map[uint32]string{}, map[uint32]string{}, map[fiendWarTimerKey]time.Time{}
	for k, v := range s.symbols {
		n.symbols[k] = v
	}
	for k, v := range s.areas {
		n.areas[k] = v
	}
	for k, v := range s.dungeons {
		n.dungeons[k] = v
	}
	for k, v := range s.timers {
		n.timers[k] = v
	}
	return &n
}

func (s *fiendWarRuleState) tick(now time.Time) (*fiendWarRuleState, []fiendWarClause, error) {
	if s == nil || now.Before(s.last) {
		return nil, nil, fmt.Errorf("Fiend War clock moved backwards")
	}
	n := s.clone()
	var commands []fiendWarClause
	for !n.cleared && !n.failed {
		due := n.deadline
		for _, end := range n.timers {
			if end.Before(due) {
				due = end
			}
		}
		if due.After(now) {
			break
		}
		if !due.Before(n.deadline) {
			n.failed = true
			n.timers = map[fiendWarTimerKey]time.Time{}
			break
		}
		var keys []fiendWarTimerKey
		for key, end := range n.timers {
			if end.Equal(due) {
				keys = append(keys, key)
			}
		}
		sort.Slice(keys, func(i, j int) bool {
			if keys[i].name == keys[j].name {
				return keys[i].area < keys[j].area
			}
			return keys[i].name < keys[j].name
		})
		var events []fiendWarEvent
		for _, key := range keys {
			delete(n.timers, key)
			events = append(events, fiendWarEvent{kind: "CHECK TIMER END", timer: key})
		}
		effects, err := n.evaluate(due, events)
		if err != nil {
			return nil, nil, err
		}
		commands = append(commands, effects...)
		if len(commands) > 4096 {
			return nil, nil, fmt.Errorf("Fiend War timer effect limit")
		}
	}
	n.last = now
	return n, commands, nil
}

func (s *fiendWarRuleState) evaluate(now time.Time, events []fiendWarEvent) ([]fiendWarClause, error) {
	var commands []fiendWarClause
	fired := map[int]bool{}
	for changed := true; changed && !s.cleared && !s.failed; {
		changed = false
		for index, rule := range s.program.rules {
			if fired[index] || !s.matches(rule.conditions, events) {
				continue
			}
			fired[index], changed = true, true
			for _, c := range rule.actions {
				switch c.kind {
				case "SET":
					if s.symbols[c.symbol] != c.value {
						s.symbols[c.symbol] = c.value
						events = append(events, fiendWarEvent{kind: "ON CHANGE SYMBOL", symbol: c.symbol})
					}
				case "SET TIMER":
					s.timers[fiendWarTimerKey{c.text, c.id}] = now.Add(time.Duration(c.value) * time.Second)
				case "RESET TIMER":
					delete(s.timers, fiendWarTimerKey{c.text, c.id})
				case "CLEAR PHASE", "FAIL FIENDWAR":
					s.cleared, s.failed = c.kind == "CLEAR PHASE", c.kind == "FAIL FIENDWAR"
					s.timers = map[fiendWarTimerKey]time.Time{}
				default:
					commands = append(commands, c)
				}
			}
			if s.cleared || s.failed {
				break
			}
		}
	}
	return commands, nil
}

func (s *fiendWarRuleState) matches(conditions []fiendWarClause, events []fiendWarEvent) bool {
	for _, c := range conditions {
		switch c.kind {
		case "IF":
			if s.symbols[c.symbol] != c.value {
				return false
			}
		case "IF LESS":
			if s.symbols[c.symbol] >= c.value {
				return false
			}
		case "CHECK AREA STATE":
			if s.areas[c.id] != c.text {
				return false
			}
		case "CHECK DUNGEON STATE":
			if s.dungeons[c.id] != c.text {
				return false
			}
		default:
			found := false
			for _, event := range events {
				if event.kind != c.kind {
					continue
				}
				if c.kind == "ON CHANGE SYMBOL" {
					found = event.symbol == c.symbol
				} else if c.kind == "CHECK TIMER END" {
					found = event.timer == (fiendWarTimerKey{c.text, c.id})
				} else {
					found = event.id == c.id
				}
				if found {
					break
				}
			}
			if !found {
				return false
			}
		}
	}
	return true
}
