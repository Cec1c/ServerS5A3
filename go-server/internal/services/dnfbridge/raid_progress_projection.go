package dnfbridge

import (
	"encoding/binary"
	"fmt"
	"math"
	"sort"
	"sync"
	"time"

	"longheng.io/server/internal/modules/dnf/raid"
)

// Actual 01A5D790 uses absolute end/duration for channel23, but for channel32
// it ignores the transmitted end and starts duration at the client's current
// server clock. Luke must therefore publish remaining duration on restores.
func currentRaidNativeDuration(mode byte, end time.Time, configured uint32, now time.Time) uint32 {
	if mode != 2 {
		return configured
	}
	seconds := math.Ceil(end.Sub(now).Seconds())
	if seconds <= 0 {
		return 0
	}
	return uint32(min(seconds, float64(configured)))
}

func currentRaidTimerProjectionForMode(mode byte, previous, next map[raid.AntonTimerKey]raid.AntonTimer, now time.Time) ([]currentRaidProjectionPacket, error) {
	packets, err := currentRaidTimerProjection(previous, next)
	if err != nil || mode != 2 {
		return packets, err
	}
	for i := range packets {
		body := packets[i].body
		key := raid.AntonTimerKey{Kind: body[0], DungeonID: binary.LittleEndian.Uint32(body[1:5])}
		if timer, ok := next[key]; ok {
			binary.LittleEndian.PutUint32(body[9:13], currentRaidNativeDuration(mode, timer.End, timer.Duration, now))
		}
	}
	return packets, nil
}

type currentRaidProjectionIdentity struct {
	InstanceID string
	Phase      byte
	CharID     uint16
	Generation uint64
}

// This mutex serializes the complete raid projection, including the initial
// send from another member's command loop. wireMu still owns socket encoding.
// Neither mutex is acquired while holding the raid aggregate mutex.
type currentRaidProjectionOwner struct {
	mu                     sync.Mutex
	identity               currentRaidProjectionIdentity
	version                uint64
	symbolCursor           uint64
	timers                 map[raid.AntonTimerKey]raid.AntonTimer
	initialized            bool
	failed                 bool
	startTransitionSent    bool
	metadataReadyIdentity  currentRaidProjectionIdentity
	metadataReady          bool
	resultIdentity         currentRaidProjectionIdentity
	resultSent             bool
	movieIdentity          currentRaidProjectionIdentity
	moviePromptSent        bool
	movieCompleted         bool
	rewardScreenIdentity   currentRaidProjectionIdentity
	rewardScreenSent       bool
	rewardFlowIdentity     currentRaidProjectionIdentity
	rewardFlowSignature    [32]byte
	rewardDisplayIdentity  currentRaidProjectionIdentity
	rewardDisplaySignature [32]byte
	participation          map[uint16]currentRaidParticipationPosition
	mapProjection          currentRaidMapProjection
	clearEvents            map[currentRaidClearEvent]struct{}
}

type currentRaidProjectionPacket struct {
	opcode       uint16
	body         []byte
	symbolCursor uint64
}

// Luke 585 carries a variable row list; the native Luke controller emits a
// cleared notice whenever a row's new state is 3. Publish only changed rows so
// a finished sanctum does not repeat its banner on an unrelated progress bump.
// First subscription, reconnect and identity changes still receive a full map.
// Other raid modes keep the historical full-map publication.
type currentRaidMapProjection struct {
	rows      map[uint32]byte
	infection uint32
	identity  currentRaidProjectionIdentity
	valid     bool
}

func (c currentRaidMapProjection) packets(mode byte, resubscribe bool, identity currentRaidProjectionIdentity, rows []raid.DungeonState, infection uint32, fullBody []byte) ([]currentRaidProjectionPacket, currentRaidMapProjection, error) {
	full := mode != 2 || resubscribe || !c.valid || c.identity != identity
	delta, cache := currentRaidMapRowDelta(c.rows, rows)
	next := currentRaidMapProjection{rows: cache, infection: infection, identity: identity, valid: true}
	if full {
		return []currentRaidProjectionPacket{{opcode: 585, body: fullBody}}, next, nil
	}
	if len(delta) > 0 {
		body, err := raid.BuildDungeonStateBody(delta, infection)
		if err != nil {
			return nil, c, err
		}
		return []currentRaidProjectionPacket{{opcode: 585, body: body}}, next, nil
	}
	if c.infection != infection {
		body, err := raid.BuildDungeonStateBody(nil, infection)
		if err != nil {
			return nil, c, err
		}
		return []currentRaidProjectionPacket{{opcode: 585, body: body}}, next, nil
	}
	return nil, next, nil
}

func currentRaidMapRowDelta(previous map[uint32]byte, rows []raid.DungeonState) ([]raid.DungeonState, map[uint32]byte) {
	next := make(map[uint32]byte, len(rows))
	delta := make([]raid.DungeonState, 0, len(rows))
	for _, row := range rows {
		next[row.ID] = row.State
		if previous == nil {
			delta = append(delta, row)
			continue
		}
		if old, ok := previous[row.ID]; !ok || old != row.State {
			delta = append(delta, row)
		}
	}
	return delta, next
}

func currentRaidMapProjectionRows(body []byte) ([]raid.DungeonState, uint32, error) {
	if len(body) < 6 || body[0] != 0 {
		return nil, 0, fmt.Errorf("raid map projection header invalid")
	}
	count := int(body[1])
	if len(body) != 2+count*5+4 {
		return nil, 0, fmt.Errorf("raid map projection length mismatch")
	}
	rows := make([]raid.DungeonState, 0, count)
	for i := 0; i < count; i++ {
		base := 2 + i*5
		rows = append(rows, raid.DungeonState{ID: binary.LittleEndian.Uint32(body[base:]), State: body[base+4]})
	}
	return rows, binary.LittleEndian.Uint32(body[2+count*5:]), nil
}

func (o *currentRaidProjectionOwner) recordCurrentRaidMapProjection(identity currentRaidProjectionIdentity, body []byte) error {
	rows, infection, err := currentRaidMapProjectionRows(body)
	if err != nil {
		return err
	}
	cache := make(map[uint32]byte, len(rows))
	for _, row := range rows {
		cache[row.ID] = row.State
	}
	o.mapProjection = currentRaidMapProjection{rows: cache, infection: infection, identity: identity, valid: true}
	return nil
}

func currentRaidTimerProjection(previous, next map[raid.AntonTimerKey]raid.AntonTimer) ([]currentRaidProjectionPacket, error) {
	keys := make(map[raid.AntonTimerKey]bool, len(previous)+len(next))
	for key := range previous {
		keys[key] = true
	}
	for key := range next {
		keys[key] = true
	}
	ordered := make([]raid.AntonTimerKey, 0, len(keys))
	for key := range keys {
		ordered = append(ordered, key)
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].Kind != ordered[j].Kind {
			return ordered[i].Kind < ordered[j].Kind
		}
		return ordered[i].DungeonID < ordered[j].DungeonID
	})
	var packets []currentRaidProjectionPacket
	for _, key := range ordered {
		if key.Kind < 1 || key.Kind > 3 || key.DungeonID == 0 {
			return nil, fmt.Errorf("raid dungeon timer key invalid")
		}
		old, existed := previous[key]
		now, present := next[key]
		if !present {
			packets = append(packets, currentRaidProjectionPacket{opcode: 596, body: raid.BuildTimerBody(key.Kind, key.DungeonID, 0, 0)})
			continue
		}
		if now.Duration == 0 || now.End.IsZero() || now.End.Unix() < int64(now.Duration) || now.End.Unix() > math.MaxUint32 {
			return nil, fmt.Errorf("raid dungeon timer deadline invalid")
		}
		if existed && old == now {
			continue
		}
		packets = append(packets, currentRaidProjectionPacket{opcode: 596, body: raid.BuildTimerBody(key.Kind, key.DungeonID, uint32(now.End.Unix()), now.Duration)})
	}
	return packets, nil
}

func currentRaidOrderedSymbolProjection(state *raid.AntonProgression, cursor uint64) ([]currentRaidProjectionPacket, error) {
	if state == nil {
		return nil, nil
	}
	writes, err := state.SymbolWritesAfter(cursor)
	if err != nil {
		return nil, err
	}
	var packets []currentRaidProjectionPacket
	for len(writes) > 0 {
		n := min(len(writes), 255)
		values := make([]raid.SymbolValue, n)
		for i := range values {
			values[i] = writes[i].SymbolValue
		}
		body, err := raid.BuildOrderedSymbolBody(values)
		if err != nil {
			return nil, err
		}
		packets = append(packets, currentRaidProjectionPacket{opcode: 583, body: body, symbolCursor: writes[n-1].Sequence})
		writes = writes[n:]
	}
	return packets, nil
}

// initial=true is exclusively the newly started phase. Reconnection restores
// values and subscribes at the current cursor, never replaying old instructions.
func (s *Service) sendCurrentRaidProgressProjectionLocked(session *gameSession, state runtimeRaidState, initial, resubscribe bool) error {
	o := &session.raidProjection
	b := state.Battle
	if b != nil && b.FiendWar != nil {
		return s.sendCurrentFiendWarProjectionLocked(session, state, initial, resubscribe)
	}
	identity := currentRaidProjectionIdentity{InstanceID: b.InstanceID, Phase: b.Phase, CharID: session.selectedCharacterID, Generation: session.characterGeneration}
	if b.ChannelID != session.channel.ID || b.ChannelType != session.channel.Type {
		return fmt.Errorf("raid projection frozen channel mismatch")
	}
	if o.failed {
		return fmt.Errorf("raid projection requires a replacement connection after transport failure")
	}
	if !resubscribe && o.initialized && o.identity == identity && b.ProgressVersion <= o.version {
		return nil
	}
	fresh := resubscribe || !o.initialized || o.identity != identity
	var singleHP []byte
	if b.Single && b.Mode == 1 {
		var err error
		singleHP, err = currentSingleAntonHPBody(b)
		if err != nil {
			return err
		}
	}
	infection := uint32(0)
	if b.Anton != nil {
		infection = b.Anton.InfectionDungeon()
	}
	mapBody, err := currentRaidDungeonProjectionBody(b, infection)
	if err != nil {
		return err
	}
	rows, mapInfection, err := currentRaidMapProjectionRows(mapBody)
	if err != nil {
		return err
	}
	mapPackets, nextMapProjection, err := o.mapProjection.packets(b.Mode, resubscribe, identity, rows, mapInfection, mapBody)
	if err != nil {
		return err
	}
	packets := make([]currentRaidProjectionPacket, 0, len(mapPackets)+len(rows))
	packets = append(packets, mapPackets...)
	if b.Mode == 2 && !b.Single {
		coinBody, err := currentLukePhaseCoinBody(b)
		if err != nil {
			return err
		}
		// Absolute snapshot on initial phase, reconnect and committed progress.
		packets = append(packets, currentRaidProjectionPacket{opcode: 775, body: coinBody})
	}
	cursor := o.symbolCursor
	previousTimers := o.timers
	if fresh {
		cursor = 0
		// Clear known timers from the previous phase as well as all symbol
		// values. The reset sentinel consumes exactly one pair in native583.
		clearTimers, err := currentRaidTimerProjection(previousTimers, nil)
		if err != nil {
			return err
		}
		packets = append(packets, clearTimers...)
		previousTimers = nil
		if b.Anton != nil || singleHP != nil {
			packets = append(packets, currentRaidProjectionPacket{opcode: 583, body: []byte{1, 0, 0, 0, 0, 0, 0, 0, 0}})
		}
		if b.Anton != nil {
			values := b.Anton.ReconnectSymbols()
			body, err := raid.BuildSymbolBody(values)
			if err != nil {
				return err
			}
			packets = append(packets, currentRaidProjectionPacket{opcode: 583, body: body})
			if !initial {
				cursor = b.Anton.SymbolSequence()
			}
		}
		if !initial {
			if b.Phase >= byte(len(b.Configuration.PhaseLimitSeconds)) || b.Deadline.Unix() < 0 || b.Deadline.Unix() > math.MaxUint32 {
				return fmt.Errorf("raid reconnect phase clock unavailable")
			}
			clockEnd := b.Deadline
			configured := b.Configuration.PhaseLimitSeconds[b.Phase] + b.Configuration.StartDelaySeconds
			remainingKind := byte(0)
			// A retained subscriber already has the finished reward projection's
			// signature. Rejoin must restore its current break clock here, not
			// overwrite it with the completed phase's old combat deadline.
			if b.Mode == 2 && b.PhaseCompleted && !b.Single && b.Phase == 0 {
				if flow := b.RewardFlow[0]; flow != nil && flow.InstanceID == b.InstanceID && flow.Phase == b.Phase && flow.Mode == b.Mode && !flow.FinishedAt.IsZero() && !flow.BreakUntil.IsZero() {
					clockEnd, configured, remainingKind = flow.BreakUntil, flow.Configuration.PhaseBreakSeconds, 1
				}
			}
			seconds := int64(math.Ceil(time.Until(clockEnd).Seconds()))
			if seconds < 0 {
				seconds = 0
			}
			end, duration := uint32(clockEnd.Unix()), currentRaidNativeDuration(b.Mode, clockEnd, configured, time.Now())
			if b.TimedOut {
				end, duration, seconds = 0, 0, 0
			}
			packets = append(packets, currentRaidProjectionPacket{opcode: 596, body: raid.BuildTimerBody(0, 0, end, duration)}, currentRaidProjectionPacket{opcode: 597, body: raid.BuildRemainingTimeBody(remainingKind, uint32(seconds))})
		}
	}
	if singleHP != nil {
		packets = append(packets, currentRaidProjectionPacket{opcode: 583, body: singleHP})
	}
	symbols, err := currentRaidOrderedSymbolProjection(b.Anton, cursor)
	if err != nil {
		o.failed = true
		if session.conn != nil {
			_ = session.conn.Close()
		}
		return err
	}
	packets = append(packets, symbols...)
	var nextTimers map[raid.AntonTimerKey]raid.AntonTimer
	if b.Anton != nil && !b.TimedOut {
		nextTimers = b.Anton.Timers()
	}
	timers, err := currentRaidTimerProjectionForMode(b.Mode, previousTimers, nextTimers, time.Now())
	if err != nil {
		return err
	}
	packets = append(packets, timers...)
	nextParticipation, err := currentRaidParticipationPositions(b, state.Members)
	if err != nil {
		return err
	}
	previousClearEvents := o.clearEvents
	if !o.initialized || o.identity != identity {
		previousClearEvents = nil
	}
	participationPackets, clearEvents := currentRaidParticipationProjection(o.participation, nextParticipation, fresh, previousClearEvents)
	packets = append(packets, participationPackets...)
	for _, packet := range packets {
		if err := s.sendGameUpperRawClassCodec(session, packet.opcode, packet.body, 0, true); err != nil {
			o.failed = true
			// A partial event frame cannot be safely replayed on this socket.
			if session.conn != nil {
				_ = session.conn.Close()
			}
			return err
		}
		s.traceCurrentAntonBarrierProjection(session, b, packet, fresh, cursor)
		if packet.symbolCursor != 0 {
			cursor = packet.symbolCursor
		}
	}
	o.participation, o.clearEvents = nextParticipation, clearEvents
	o.mapProjection = nextMapProjection
	o.identity, o.version, o.symbolCursor, o.timers, o.initialized = identity, b.ProgressVersion, cursor, nextTimers, true
	if b.Mode == 2 && !b.Single && b.FiendWar == nil && (initial || resubscribe) {
		// For current Luke, every production caller of initial/resubscribe
		// progress sends metadata immediately before it. Ordinary background
		// progress (false,false) must not claim that the new client has metadata.
		o.metadataReadyIdentity, o.metadataReady = identity, true
	}
	if fresh {
		o.startTransitionSent = initial || resubscribe
	}
	return nil
}

// Caller holds only the subscriber mutex. Aggregate locking ends before IO.
func (s *Service) latestCurrentRaidProjectionState(session *gameSession, state runtimeRaidState) (runtimeRaidState, error) {
	s.raidMu.Lock()
	defer s.raidMu.Unlock()
	live := s.runtimeRaidByMemberLocked(session.selectedCharacterID)
	valid := live != nil && live.Started && live.RaidKey == state.RaidKey && live.Battle != nil && live.Battle.InstanceID == state.Battle.InstanceID && live.Battle.Phase == state.Battle.Phase && live.Battle.MemberAccounts[session.selectedCharacterID] == s.accountIDForSession(session)
	if !valid {
		return runtimeRaidState{}, fmt.Errorf("raid projection instance/phase replaced")
	}
	if live.Battle.ChannelID != session.channel.ID || live.Battle.ChannelType != session.channel.Type {
		return runtimeRaidState{}, fmt.Errorf("raid projection frozen channel mismatch")
	}
	return cloneRuntimeRaidState(*live), nil
}

func (s *Service) sendCurrentRaidProgressProjection(session *gameSession, state runtimeRaidState, initial, resubscribe bool) error {
	if session == nil || state.Battle == nil {
		return fmt.Errorf("raid projection unavailable")
	}
	session.raidProjection.mu.Lock()
	defer session.raidProjection.mu.Unlock()
	if !s.currentRaidAuthenticated(session) || runtimeRaidMemberIndex(state, session.selectedCharacterID) < 0 ||
		state.Battle.MemberAccounts[session.selectedCharacterID] != s.accountIDForSession(session) {
		return fmt.Errorf("raid projection frozen owner unavailable")
	}
	// Re-read after acquiring the subscriber mutex: a callback that waited
	// behind a newer send must neither roll back maps nor replay old effects.
	var err error
	state, err = s.latestCurrentRaidProjectionState(session, state)
	if err != nil {
		return err
	}
	return s.sendCurrentRaidProgressProjectionLocked(session, state, initial, resubscribe)
}
