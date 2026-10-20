package raid

import (
	"fmt"
	"maps"
	"sort"
	"strconv"
	"strings"
	"time"
)

type AntonTimerKey struct {
	Kind      byte
	DungeonID uint32
}
type AntonTimer struct {
	End      time.Time
	Duration uint32
}
type antonReservedState struct {
	State byte
	End   time.Time
}
type antonEvent struct {
	kind   string
	id     uint32
	symbol string
}

// AntonProgression is an instance/phase-scoped reducer. The bridge must supply
// only completed, owned dungeon runs. It cannot be driven by a client-supplied
// dungeon-state/symbol packet. Returned copies make unsuccessful updates atomic.
type AntonProgression struct {
	program           *AntonProgram
	phase             int
	dungeons          map[uint32]byte
	clears            map[uint32]uint32
	symbols           map[string]uint32
	symbolSequence    uint64
	symbolWrites      []AntonSymbolWrite
	timers            map[AntonTimerKey]AntonTimer
	reserved          map[uint32]antonReservedState
	runs              map[string]bool
	phaseCleared      bool
	phaseFailed       bool
	enteredRuns       map[string]uint32
	abandonedRuns     map[string]uint32
	completedLukeRuns map[string]uint32
	preparedLukeRuns  map[string]uint32
	epoch             uint64
	infection         uint32
	random            uint64
	last              time.Time
	barrierNext       time.Time
	barrierEvents     map[string]bool
	volcanoOpen       bool
	volcanoStateKnown bool
}

func (p *AntonProgram) BeginPhase(phase int, now time.Time, seed uint64) (*AntonProgression, error) {
	if p == nil || phase < 0 || phase >= len(p.phases) || now.IsZero() {
		return nil, fmt.Errorf("Anton phase context unavailable")
	}
	if seed == 0 {
		return nil, fmt.Errorf("Anton instance random seed unavailable")
	}
	s := &AntonProgression{program: p, phase: phase, dungeons: map[uint32]byte{}, clears: map[uint32]uint32{},
		symbols: map[string]uint32{}, timers: map[AntonTimerKey]AntonTimer{}, reserved: map[uint32]antonReservedState{},
		runs: map[string]bool{}, barrierEvents: map[string]bool{}, random: seed, last: now, epoch: 1}
	for _, d := range p.config.PhaseDungeons[phase] {
		s.dungeons[d.ID] = d.State
	}
	if err := s.evaluate(now, []antonEvent{{kind: "initial"}}); err != nil {
		return nil, err
	}
	if phase == 1 && !p.luke {
		if s.symbols["BLACK VOLCANO BARRIER"] != 10000 {
			return nil, fmt.Errorf("Anton phase-two initial shield must match the actual 10000 range")
		}
		s.barrierNext = now.Add(time.Second)
	}
	return s, nil
}

func (s *AntonProgression) Clone() *AntonProgression {
	if s == nil {
		return nil
	}
	v := *s
	v.dungeons = copyAntonMap(s.dungeons)
	v.clears = copyAntonMap(s.clears)
	v.symbols = copyAntonMap(s.symbols)
	v.symbolWrites = append([]AntonSymbolWrite(nil), s.symbolWrites...)
	v.timers = copyAntonMap(s.timers)
	v.reserved = copyAntonMap(s.reserved)
	v.runs = copyAntonMap(s.runs)
	v.enteredRuns = copyAntonMap(s.enteredRuns)
	v.abandonedRuns = copyAntonMap(s.abandonedRuns)
	v.completedLukeRuns = copyAntonMap(s.completedLukeRuns)
	v.preparedLukeRuns = copyAntonMap(s.preparedLukeRuns)
	v.barrierEvents = copyAntonMap(s.barrierEvents)
	return &v
}

func copyAntonMap[K comparable, V any](source map[K]V) map[K]V {
	result := make(map[K]V, len(source))
	for k, v := range source {
		result[k] = v
	}
	return result
}

func (s *AntonProgression) Dungeons() []DungeonState {
	if s == nil {
		return nil
	}
	result := make([]DungeonState, 0, len(s.dungeons))
	for _, d := range s.program.config.PhaseDungeons[s.phase] {
		result = append(result, DungeonState{ID: d.ID, State: s.dungeons[d.ID]})
	}
	return result
}

func (s *AntonProgression) Symbols() map[string]uint32 {
	if s == nil {
		return nil
	}
	return copyAntonMap(s.symbols)
}
func (s *AntonProgression) Timers() map[AntonTimerKey]AntonTimer {
	if s == nil {
		return nil
	}
	return copyAntonMap(s.timers)
}
func (s *AntonProgression) PhaseCleared() bool { return s != nil && s.phaseCleared }
func (s *AntonProgression) PhaseFailed() bool  { return s != nil && s.phaseFailed }
func (s *AntonProgression) IsLuke() bool       { return s != nil && s.program != nil && s.program.luke }
func (s *AntonProgression) Phase() int {
	if s == nil {
		return -1
	}
	return s.phase
}
func (s *AntonProgression) Epoch() uint64 {
	if s == nil {
		return 0
	}
	return s.epoch
}
func (s *AntonProgression) InfectionDungeon() uint32 {
	if s == nil {
		return 0
	}
	return s.infection
}
func (s *AntonProgression) ClearCount(id uint32) uint32 {
	if s == nil {
		return 0
	}
	return s.clears[id]
}

// LastTime orders server callbacks which obtained their timestamps before
// acquiring the aggregate lock. It is never derived from a client packet.
func (s *AntonProgression) LastTime() time.Time {
	if s == nil {
		return time.Time{}
	}
	return s.last
}

// Advancing a clock alone is not a projection change. This prevents a global
// one-second gameplay tick from repeatedly replaying symbols or map packets.
func (s *AntonProgression) SameProjection(other *AntonProgression) bool {
	if s == nil || other == nil {
		return s == other
	}
	return s.phase == other.phase && s.phaseCleared == other.phaseCleared && s.phaseFailed == other.phaseFailed && s.epoch == other.epoch && s.infection == other.infection &&
		s.symbolSequence == other.symbolSequence &&
		s.volcanoOpen == other.volcanoOpen && s.volcanoStateKnown == other.volcanoStateKnown &&
		maps.Equal(s.dungeons, other.dungeons) && maps.Equal(s.symbols, other.symbols) && maps.Equal(s.timers, other.timers)
}

// Tick takes elapsed timers/reservations through the same PVF rule graph as
// clear events. No real clock or timer goroutine lives in the domain reducer.
func (s *AntonProgression) Tick(now time.Time) (*AntonProgression, error) {
	if s == nil || now.IsZero() || now.Before(s.last) {
		return nil, fmt.Errorf("Anton clock moved backwards")
	}
	v := s.Clone()
	if v.phaseCleared || v.phaseFailed {
		v.last = now
		return v, nil
	}
	// Replay due deadlines in order so a delayed callback cannot skip a
	// reopened dungeon's active/effect timer or reset an entire phase late.
	for steps := 0; ; steps++ {
		if steps > 4096 {
			return nil, fmt.Errorf("Anton elapsed timer budget exceeded")
		}
		due := time.Time{}
		if v.barrierActive() && !v.barrierNext.After(now) {
			due = v.barrierNext
		}
		for _, timer := range v.timers {
			if timer.End.After(v.last) && !timer.End.After(now) && (due.IsZero() || timer.End.Before(due)) {
				due = timer.End
			}
		}
		for _, reserved := range v.reserved {
			if !reserved.End.After(now) && (due.IsZero() || reserved.End.Before(due)) {
				due = reserved.End
			}
		}
		if due.IsZero() {
			break
		}
		events := []antonEvent{{kind: "tick"}}
		if v.barrierActive() && !v.barrierNext.After(due) {
			v.chargeBarrier(&events)
			v.barrierNext = due.Add(time.Second)
		}
		ids := make([]uint32, 0)
		for id, reserved := range v.reserved {
			if !reserved.End.After(due) {
				ids = append(ids, id)
			}
		}
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		for _, id := range ids {
			reserved := v.reserved[id]
			delete(v.reserved, id)
			v.setDungeon(id, reserved.State, &events)
		}
		if err := v.evaluate(due, events); err != nil {
			return nil, err
		}
		v.last = due
		if v.phaseCleared || v.phaseFailed {
			break
		}
	}
	if err := v.evaluate(now, []antonEvent{{kind: "tick"}}); err != nil {
		return nil, err
	}
	v.last = now
	return v, nil
}

func (s *AntonProgression) ClearDungeon(runID string, dungeonID uint32, expectedEpoch uint64, now time.Time) (*AntonProgression, bool, error) {
	if runID == "" || len(runID) > 128 {
		return nil, false, fmt.Errorf("Anton clear run identity unavailable")
	}
	if s == nil {
		return nil, false, fmt.Errorf("Anton clear owner unavailable")
	}
	if expectedEpoch == 0 || expectedEpoch != s.epoch {
		return nil, false, fmt.Errorf("Anton clear belongs to an old phase attempt")
	}
	if s.runs[runID] {
		return s.Clone(), true, nil
	}
	v, err := s.Tick(now)
	if err != nil {
		return nil, false, err
	}
	if v.epoch != expectedEpoch {
		return nil, false, fmt.Errorf("Anton phase attempt expired before clear")
	}
	state, exists := v.dungeons[dungeonID]
	if !exists || state != 0 || v.phaseCleared || v.phaseFailed {
		return nil, false, fmt.Errorf("Anton dungeon %d is not open", dungeonID)
	}
	v.runs[runID] = true
	v.clears[dungeonID]++
	v.assignSymbolValue(strconv.FormatUint(uint64(dungeonID), 10)+" DGN HP", v.clears[dungeonID])
	if v.clears[dungeonID] >= v.program.phases[v.phase].hp[dungeonID] {
		events := []antonEvent{}
		for _, id := range v.program.hatcheries {
			if dungeonID == id {
				// Reference ClearHatcheryAsync removes both effect timers and
				// their symbols immediately, including the infected hatchery.
				delete(v.timers, AntonTimerKey{1, id})
				delete(v.timers, AntonTimerKey{3, id})
				v.setSymbol(strconv.FormatUint(uint64(id), 10)+" HATCHERY FAIL", 0, &events)
				v.setSymbol(strconv.FormatUint(uint64(id), 10)+" HATCHERY NAMED CREATE", 0, &events)
				if id == v.infection {
					v.setSymbol("IS EXIST INFECTION DUNGEON", 0, &events)
				}
			}
		}
		v.setDungeon(dungeonID, 3, &events)
		if err := v.evaluate(now, events); err != nil {
			return nil, false, err
		}
	}
	return v, false, nil
}

func (s *AntonProgression) setDungeon(id uint32, state byte, events *[]antonEvent) {
	previous := s.dungeons[id]
	if previous == state {
		return
	}
	s.dungeons[id] = state
	if state == 0 {
		s.clears[id] = 0
		s.assignSymbolValue(strconv.FormatUint(uint64(id), 10)+" DGN HP", 0)
		if id == s.infection {
			s.setSymbol("IS EXIST INFECTION DUNGEON", 1, events)
		}
	}
	if state == 2 {
		if id == s.infection {
			s.infection = 0
			s.setSymbol("IS EXIST INFECTION DUNGEON", 0, events)
			s.setSymbol("INFECTION DUNGEON INDEX", 0, events)
		}
		delete(s.reserved, id)
		for key := range s.timers {
			if key.DungeonID == id {
				delete(s.timers, key)
			}
		}
	}
	*events = append(*events, antonEvent{kind: "dungeon", id: id})
}

func (s *AntonProgression) setSymbol(name string, value uint32, events *[]antonEvent) {
	// An explicit SET is also a native callback, including an unchanged
	// value. Keep its ordered write even when ON CHANGE rules do not fire.
	s.recordSymbolWrite(name, value)
	if name == "BLACK VOLCANO BARRIER" {
		s.updateVolcanoNativeState(value)
	}
	if s.symbols[name] == value {
		return
	}
	s.symbols[name] = value
	if name == "1PHASE FAIL" && value == 1 && s.phase == 0 && !s.program.luke {
		// The reference aggregate resets every first-phase clear counter on
		// failure (RaidHandler.Progression.ResetPhaseOneAsync). PVF resets the
		// selector states but does not repeat the HP=0 directives here.
		s.epoch++
		for id := range s.clears {
			s.clears[id] = 0
			s.assignSymbolValue(strconv.FormatUint(uint64(id), 10)+" DGN HP", 0)
		}
	}
	if strings.HasSuffix(name, " DGN HP") {
		id, err := strconv.ParseUint(strings.TrimSuffix(name, " DGN HP"), 10, 32)
		if err == nil {
			s.clears[uint32(id)] = value
		}
	}
	*events = append(*events, antonEvent{kind: "symbol", symbol: name})
}

func (s *AntonProgression) matches(c antonClause, event antonEvent, now time.Time) bool {
	switch c.kind {
	case "ON ENTER DUNGEON", "ON GIVEUP DUNGEON", "ON ENTER MAP", "PHASE END":
		return event.kind == c.kind && event.id == c.id
	case "ON CHANGE DUNGEON STATE":
		return event.kind == "dungeon" && event.id == c.id
	case "ON CHANGE SYMBOL":
		return event.kind == "symbol" && event.symbol == c.symbol
	case "CHECK DUNGEON STATE":
		return s.dungeons[c.id] == c.state
	case "SYMBOL EQUAL":
		return s.symbols[c.symbol] == c.value
	case "CHECK TIMER END":
		timer, ok := s.timers[AntonTimerKey{c.timer, c.id}]
		return ok && !now.Before(timer.End)
	}
	return false
}

func (s *AntonProgression) evaluate(now time.Time, events []antonEvent) error {
	for index := 0; index < len(events) && !s.phaseCleared && !s.phaseFailed; index++ {
		if index > 2048 {
			return fmt.Errorf("Anton cyclic directive budget exceeded")
		}
		event := events[index]
		for _, rule := range s.program.phases[s.phase].rules {
			matched := true
			for _, c := range rule.conditions {
				if !s.matches(c, event, now) {
					matched = false
					break
				}
			}
			if !matched {
				continue
			}
			for _, action := range rule.actions {
				switch action.kind {
				case "SET DUNGEON STATE":
					s.setDungeon(action.id, action.state, &events)
				case "RESERVE DUNGEON STATE":
					s.reserved[action.id] = antonReservedState{action.state, now.Add(time.Duration(action.value) * time.Second)}
				case "SET":
					value := action.value
					if action.add {
						if uint64(s.symbols[action.symbol])+uint64(value) > uint64(^uint32(0)) {
							return fmt.Errorf("raid symbol addition overflow")
						}
						value += s.symbols[action.symbol]
					}
					s.setSymbol(action.symbol, value, &events)
				case "RESET SANCTUM OBJECT":
					name := s.program.sanctumSymbols[action.id]
					if !s.program.luke || name == "" {
						return fmt.Errorf("raid sanctum object owner unavailable")
					}
					s.setSymbol(name, 0, &events)
				case "FAIL PHASE":
					s.phaseFailed = true
					s.timers = map[AntonTimerKey]AntonTimer{}
					s.reserved = map[uint32]antonReservedState{}
				case "SET TIMER":
					s.timers[AntonTimerKey{action.timer, action.id}] = AntonTimer{now.Add(time.Duration(action.value) * time.Second), action.value}
				case "RESET TIMER":
					delete(s.timers, AntonTimerKey{action.timer, action.id})
				case "CLEAR PHASE":
					s.phaseCleared = true
					s.timers = map[AntonTimerKey]AntonTimer{}
					s.reserved = map[uint32]antonReservedState{}
				case "OPEN RANDOM HATCHERY":
					ids := append([]uint32(nil), s.program.hatcheries...)
					for i := len(ids) - 1; i > 0; i-- {
						j := s.nextRandom(i + 1)
						ids[i], ids[j] = ids[j], ids[i]
					}
					ids = ids[:s.program.hatcheryOpen]
					s.infection = ids[s.nextRandom(len(ids))]
					s.setSymbol("IS EXIST INFECTION DUNGEON", 1, &events)
					s.setSymbol("INFECTION DUNGEON INDEX", s.infection, &events)
					for _, id := range ids {
						s.setDungeon(id, 0, &events)
					}
				default:
					return fmt.Errorf("uncompiled Anton action %s", action.kind)
				}
			}
			if s.phaseCleared || s.phaseFailed {
				break
			}
		}
	}
	return nil
}

func (s *AntonProgression) nextRandom(bound int) int {
	s.random ^= s.random << 13
	s.random ^= s.random >> 7
	s.random ^= s.random << 17
	return int(s.random % uint64(bound))
}
