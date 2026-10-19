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
	resultIdentity         currentRaidProjectionIdentity
	resultSent             bool
	rewardScreenIdentity   currentRaidProjectionIdentity
	rewardScreenSent       bool
	rewardFlowIdentity     currentRaidProjectionIdentity
	rewardFlowSignature    [32]byte
	rewardDisplayIdentity  currentRaidProjectionIdentity
	rewardDisplaySignature [32]byte
	participation          map[uint16]currentRaidParticipationPosition
}

type currentRaidProjectionPacket struct {
	opcode       uint16
	body         []byte
	symbolCursor uint64
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
		return s.sendCurrentFiendWarProjectionLocked(session, state, false)
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
	packets := []currentRaidProjectionPacket{{opcode: 585, body: mapBody}}
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
			seconds := int64(math.Ceil(time.Until(b.Deadline).Seconds()))
			if seconds < 0 {
				seconds = 0
			}
			end, duration := uint32(b.Deadline.Unix()), currentRaidNativeDuration(b.Mode, b.Deadline, b.Configuration.PhaseLimitSeconds[b.Phase]+b.Configuration.StartDelaySeconds, time.Now())
			if b.TimedOut {
				end, duration, seconds = 0, 0, 0
			}
			packets = append(packets, currentRaidProjectionPacket{opcode: 596, body: raid.BuildTimerBody(0, 0, end, duration)}, currentRaidProjectionPacket{opcode: 597, body: raid.BuildRemainingTimeBody(0, uint32(seconds))})
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
	packets = append(packets, currentRaidParticipationProjection(o.participation, nextParticipation, fresh)...)
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
	o.participation = nextParticipation
	o.identity, o.version, o.symbolCursor, o.timers, o.initialized = identity, b.ProgressVersion, cursor, nextTimers, true
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
