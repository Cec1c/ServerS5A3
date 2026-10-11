package dnfbridge

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	"longheng.io/server/internal/modules/dnf/raid"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// D33 sidecar: Anton phase-2 black-volcano barrier cross-squad OUTBOUND proof.
//
// SCOPE LIMIT: these tests exercise only the server projection chain
//   energy squad accepted transmission -> recordCurrentAntonEnergySymbolLocked
//   -> ApplyBarrierDamage(symbol 110) -> queueCurrentRaidDungeonProgress
//   -> per-member goroutine -> callGameSession -> sendCurrentRaidProgressProjection
// and nothing else. They neither prove nor claim a barrier/immunity fix: the
// reported symptom is an in-map damage-immunity plus a missing top-right release
// animation, which a projection test can only rule the server chain in or out.
//
// COVERAGE LIMIT: the volcano peer is a real onlineGameSession and raid member
// (discovered through the same gameSessions index the service uses) but it has
// no dungeon runtime in 219. The projection dispatch is instance-membership
// scoped, so this is the production recipient of the barrier symbols, yet the
// 219-runtime-side consumption of those symbols is NOT covered here.
//
// Real PVF facts (pinned by internal/modules/dnf/raid/anton_barrier_test.go):
// SHIELD CHARGE RATE 17/34, large transmission 3000, small transmission 100.
// The PVF ON CHANGE rules write MOVIE BARRIER BREAK (126)=1 on 110==0 and
// MOVIE BARRIER RECOVER (127)=1 on 110==10000; 127 is never written back to 0.

// volcanoNativeEvidenceEnv optionally dumps the classification-0 NOTI583 frames
// a scenario put on the wire for the target profile's sender, so the wire form
// can be compared with the native transport model.
const volcanoNativeEvidenceEnv = "DNF90_ANTON_NATIVE_DIR"

// The signed v96/v97 native sender entry uses a 16-byte upper header (proven
// ABI), and the historical r98 read-only profile receipt binds the server16
// header explicitly (DNFBRIDGE_GAME_UPPER_HEADER=server16; current state pinned
// too). options.New alone defaults to channel13, so this fixture's zero-valued
// options would fall back to that legacy 13-byte header, which is NOT production
// raw wire. This file therefore selects server16 explicitly and decodes with the
// shared header-aware helper plus a protocol CRC check. The earlier 13-byte
// output was a fixture default artifact, not production raw wire; production
// profile binding is recorded separately by the main agent, and the global test
// helpers stay untouched.
const volcanoUpperHeaderSize = dnfproto.GameServerUpperHeaderSize16

// volcanoVerifyFrameChecksum recomputes the frame CRC with the protocol's own
// routine over bytes [11,total) and compares it with the stored u32, so every
// frame used below is CRC-verified rather than length-checked only.
func volcanoVerifyFrameChecksum(t *testing.T, frame []byte) {
	t.Helper()
	if len(frame) < volcanoUpperHeaderSize {
		t.Fatalf("volcano frame too short: %d", len(frame))
	}
	sum, err := dnfproto.ChecksumRange(frame, 11, len(frame)-11)
	if err != nil {
		t.Fatalf("volcano frame checksum range: %v", err)
	}
	if want := binary.LittleEndian.Uint32(frame[7:11]); sum != want {
		t.Fatalf("volcano frame checksum mismatch: stored=%08x computed=%08x", want, sum)
	}
}

// splitVolcanoRawFrames cuts a sink buffer into the exact complete frames that
// were written, using the repo's header-aware helper for the 16-byte server16
// layout and verifying each frame's CRC. Global helpers are not modified.
func splitVolcanoRawFrames(t *testing.T, data []byte) [][]byte {
	t.Helper()
	var out [][]byte
	rest := data
	for len(rest) > 0 {
		packet, remaining := splitGameServerUpperPacketWithHeader(t, rest, volcanoUpperHeaderSize)
		total := int(packet.Header.Length)
		frame := append([]byte(nil), rest[:total]...)
		volcanoVerifyFrameChecksum(t, frame)
		out = append(out, frame)
		rest = remaining
	}
	return out
}

// splitVolcanoUpperFrames decodes the verified 16-byte-header frames into the
// msgID/body form the structural assertions use. It replaces this file's earlier
// use of splitUpperFrames, which assumes the 13-byte legacy header.
func splitVolcanoUpperFrames(t *testing.T, data []byte) []upperFrame {
	t.Helper()
	frames := make([]upperFrame, 0, 4)
	for _, frame := range splitVolcanoRawFrames(t, data) {
		packet, _ := splitGameServerUpperPacketWithHeader(t, frame, volcanoUpperHeaderSize)
		frames = append(frames, upperFrame{msgID: packet.Header.MsgID, body: append([]byte(nil), packet.Body...)})
	}
	return frames
}

// raidVolcanoPeerConn is a mutex-protected net.Conn sink. The broadcast
// dispatches from a per-member goroutine, so a bare bytes.Buffer would race
// with the test reader.
type raidVolcanoPeerConn struct {
	mu  sync.Mutex
	buf []byte
}

func (c *raidVolcanoPeerConn) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.buf = append(c.buf, p...)
	return len(p), nil
}
func (c *raidVolcanoPeerConn) Read([]byte) (int, error)         { return 0, io.EOF }
func (c *raidVolcanoPeerConn) Close() error                     { return nil }
func (c *raidVolcanoPeerConn) LocalAddr() net.Addr              { return fakeAddr("local") }
func (c *raidVolcanoPeerConn) RemoteAddr() net.Addr             { return fakeAddr("remote") }
func (c *raidVolcanoPeerConn) SetDeadline(time.Time) error      { return nil }
func (c *raidVolcanoPeerConn) SetReadDeadline(time.Time) error  { return nil }
func (c *raidVolcanoPeerConn) SetWriteDeadline(time.Time) error { return nil }

func (c *raidVolcanoPeerConn) snapshot() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]byte(nil), c.buf...)
}

func (c *raidVolcanoPeerConn) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.buf = nil
}

type raidVolcanoCrossSquadFixture struct {
	service *Service
	energy  *gameSession
	peer    *gameSession
	state   *runtimeRaidState
	battle  *runtimeRaidBattle
	at      time.Time
}

func newRaidVolcanoCrossSquadFixture(t *testing.T) *raidVolcanoCrossSquadFixture {
	t.Helper()
	s, energy, _, b, at := actualEnergySymbolFixture(t, false)
	// The target profile's sender uses the 16-byte upper header; options.New's
	// channel13 default is not production raw wire.
	s.options.gameUpperHeader = gameUpperHeaderServer16
	// Deterministic gameplay clock exactly at the frozen attempt start so the
	// handler Tick cannot add a charge second before the transmission.
	s.gameplayTimers = &fakeCurrentDungeonDeathTimerQueue{
		now:    at,
		active: make(map[string]*fakeCurrentDungeonDeathTimerTask),
	}
	s.raidMu.Lock()
	state := s.runtimeRaidByMemberLocked(energy.selectedCharacterID)
	s.raidMu.Unlock()
	if state == nil || state.Battle != b {
		t.Fatal("energy raid state unavailable")
	}
	f := &raidVolcanoCrossSquadFixture{service: s, energy: energy, state: state, battle: b, at: at}
	f.peer = f.addVolcanoMember(t, 100, "account-20")
	if err := f.reduceBarrierToOneSmallTransmission(t); err != nil {
		t.Fatal(err)
	}
	return f
}

// addVolcanoMember registers another real online session for an existing raid
// slot. The session is discoverable through onlineGameSession(gameSessions) and
// therefore receives the production broadcast without direct sends. It has no
// 219 dungeon runtime: only the outbound projection is in scope.
func (f *raidVolcanoCrossSquadFixture) addVolcanoMember(t *testing.T, charID uint16, account string) *gameSession {
	t.Helper()
	s := f.service
	repos, ok := s.repositoryGroup()
	if !ok {
		t.Fatal("repositories unavailable")
	}
	if err := repos.Character.Save(context.Background(), dnfrepo.CharacterRecord{CharacterID: fmt.Sprint(charID), AccountID: account}); err != nil {
		t.Fatal(err)
	}
	session := &gameSession{
		conn:                &raidVolcanoPeerConn{},
		connID:              "volcano-peer",
		channel:             f.energy.channel,
		residentChannel:     f.energy.channel,
		selectedCharacterID: charID,
		accountID:           account,
		loginPacketSeen:     true,
		accountFromToken:    true,
	}
	s.bindGameSessionCharacter(session, charID)
	f.state.Members = append(f.state.Members, runtimeRaidMemberState{CharID: charID, GroupIndex: 2})
	f.battle.MemberAccounts[charID] = account
	if got, found := s.onlineGameSession(charID); !found || got != session {
		t.Fatal("volcano peer is not an indexed online session")
	}
	return session
}

// reduceBarrierToOneSmallTransmission steps the barrier down to exactly one
// small transmission using the real PVF trace amounts, so the single production
// handler event that follows is the one that reaches zero. Stepping uses the
// same domain reducer production uses; it opens no packet path and is fixture
// setup only.
func (f *raidVolcanoCrossSquadFixture) reduceBarrierToOneSmallTransmission(t *testing.T) error {
	t.Helper()
	small, ok := f.battle.Anton.TransmissionBarrierOperand(raid.AntonSmallTransmission)
	if !ok || small == 0 {
		return fmt.Errorf("small transmission operand unavailable")
	}
	large, ok := f.battle.Anton.TransmissionBarrierOperand(raid.AntonLargeTransmission)
	if !ok || large == 0 {
		return fmt.Errorf("large transmission operand unavailable")
	}
	cur := f.battle.Anton.NativeSymbols()[110]
	if cur <= small {
		return nil
	}
	diff := cur - small
	if diff%small != 0 {
		return fmt.Errorf("barrier %d cannot be stepped to small=%d diff=%d", cur, small, diff)
	}
	event := 0
	apply := func(source raid.AntonBarrierSource, n uint32) error {
		for i := uint32(0); i < n; i++ {
			event++
			next, replay, err := f.battle.Anton.ApplyBarrierDamage(fmt.Sprintf("fixture/volcano/step/%d", event), source, 218, f.battle.Anton.Epoch(), f.at)
			if err != nil || replay {
				return fmt.Errorf("fixture barrier step %d source=%d err=%v replay=%v", event, source, err, replay)
			}
			f.battle.Anton = next
		}
		return nil
	}
	if err := apply(raid.AntonLargeTransmission, diff/large); err != nil {
		return err
	}
	if err := apply(raid.AntonSmallTransmission, (diff%large)/small); err != nil {
		return err
	}
	if got := f.battle.Anton.NativeSymbols()[110]; got != small {
		return fmt.Errorf("fixture barrier=%d want one small transmission %d", got, small)
	}
	f.battle.Dungeons = f.battle.Anton.Dungeons()
	return nil
}

func (f *raidVolcanoCrossSquadFixture) liveState() runtimeRaidState {
	f.service.raidMu.Lock()
	defer f.service.raidMu.Unlock()
	return cloneRuntimeRaidState(*f.state)
}

func (f *raidVolcanoCrossSquadFixture) peerConn() *raidVolcanoPeerConn {
	return f.peer.conn.(*raidVolcanoPeerConn)
}

// waitPeerCursor waits until the peer's own projection cursor reached the
// expected authoritative sequence. The projection goroutine updates the cursor
// only after it finished writing, so an equal cursor proves the async dispatch
// completed; a not-yet-started goroutine simply leaves the old cursor and is
// retried. Polling the cursor (not just the mutex) is what makes this
// deterministic.
func (f *raidVolcanoCrossSquadFixture) waitPeerCursor(t *testing.T, want uint64) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		f.peer.raidProjection.mu.Lock()
		cursor := f.peer.raidProjection.symbolCursor
		f.peer.raidProjection.mu.Unlock()
		if cursor == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("peer cursor=%d want=%d frames=%v", cursor, want, volcanoFrameMessageIDs(splitVolcanoUpperFrames(t, f.peerConn().snapshot())))
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func volcanoFrameMessageIDs(frames []upperFrame) []uint16 {
	ids := make([]uint16, 0, len(frames))
	for _, frame := range frames {
		ids = append(ids, frame.msgID)
	}
	return ids
}

func volcanoOrderedFrames(t *testing.T, frames []upperFrame) [][]raid.SymbolValue {
	t.Helper()
	var out [][]raid.SymbolValue
	for _, frame := range frames {
		if frame.msgID == 583 {
			out = append(out, antonProjectionWrites(t, frame.body))
		}
	}
	return out
}

func volcanoFlatten(frames [][]raid.SymbolValue) []raid.SymbolValue {
	var out []raid.SymbolValue
	for _, writes := range frames {
		out = append(out, writes...)
	}
	return out
}

func volcanoHasSymbol(writes []raid.SymbolValue, id, value uint32) bool {
	for _, write := range writes {
		if write.ID == id && write.Value == value {
			return true
		}
	}
	return false
}

func volcanoSymbolValues(writes []raid.SymbolValue, id uint32) []uint32 {
	var values []uint32
	for _, write := range writes {
		if write.ID == id {
			values = append(values, write.Value)
		}
	}
	return values
}

func exchangeVolcanoBarrier(t *testing.T, f *raidVolcanoCrossSquadFixture) {
	t.Helper()
	small, ok := f.battle.Anton.TransmissionBarrierOperand(raid.AntonSmallTransmission)
	if !ok {
		t.Fatal("small transmission operand unavailable")
	}
	body := make([]byte, 9)
	binary.LittleEndian.PutUint32(body, 110)
	binary.LittleEndian.PutUint32(body[4:], small)
	body[8] = 2
	if err := f.service.handleCurrentAntonEnergySymbol(f.energy, uint16(dnfenum.CmdPacketRaidSetSymbol), body); err != nil {
		t.Fatalf("energy transmission handler: %v", err)
	}
}

// Test 1: the energy squad drives symbol 110 to 0 through the production handler
// and the volcano peer's session receives the live ordered NOTI583 delta
// 110->0, then the derived native animation state 111->1, then the formal PVF
// MOVIE BARRIER BREAK 126->1, after its fresh initialization already completed.
// A fresh snapshot frame carries the whole symbol table, so requiring exactly
// three writes in the first post-initialization frame excludes a snapshot-based
// false pass.
func TestRealPVFVolcanoBarrierEnergyBreakProjectsOrderedSymbolsToPeer(t *testing.T) {
	f := newRaidVolcanoCrossSquadFixture(t)
	baseline := f.battle.Anton.SymbolSequence()

	f.service.queueCurrentRaidDungeonProgress(f.liveState())
	f.waitPeerCursor(t, baseline)
	f.peerConn().reset()

	exchangeVolcanoBarrier(t, f)
	f.waitPeerCursor(t, f.battle.Anton.SymbolSequence())

	frames := splitVolcanoUpperFrames(t, f.peerConn().snapshot())
	if !volcanoHasFrame(frames, 585) {
		t.Fatalf("break delta carried no map frame ids=%v", volcanoFrameMessageIDs(frames))
	}
	delta := volcanoOrderedFrames(t, frames)
	if len(delta) != 1 || len(delta[0]) != 3 {
		t.Fatalf("break 583 frames=%v want exactly one [110->0,111->1,126->1] delta", delta)
	}
	if !volcanoEqualSequence(delta[0], volcanoPairSequence(110, 0, 111, 1, 126, 1)) {
		t.Fatalf("break delta=%v want [110->0, 111->1, 126->1]", delta[0])
	}
	if got := f.battle.Anton.NativeSymbols()[110]; got != 0 {
		t.Fatalf("energy break left barrier=%d want 0", got)
	}
	if got := f.battle.Anton.NativeSymbols()[raid.AntonVolcanoOpenSymbolID]; got != 1 {
		t.Fatalf("energy break left native volcano animation state=%d want 1", got)
	}
	dumpVolcano583Frames(t, "break-delta", f.peerConn().snapshot())
}

func volcanoHasFrame(frames []upperFrame, msgID uint16) bool {
	for _, frame := range frames {
		if frame.msgID == msgID {
			return true
		}
	}
	return false
}

// volcanoPairSequence flattens variadic (id,value) pairs into symbol writes.
func volcanoPairSequence(idValue ...uint32) []raid.SymbolValue {
	out := make([]raid.SymbolValue, 0, len(idValue)/2)
	for i := 0; i+1 < len(idValue); i += 2 {
		out = append(out, raid.SymbolValue{ID: idValue[i], Value: idValue[i+1]})
	}
	return out
}

func volcanoEqualSequence(got, want []raid.SymbolValue) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func volcanoEqualValues(got, want []uint32) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// Test 2: an immediately resumed clock replayed through single delayed callbacks
// coalesces many writes; the delivered stream must still begin with the live
// 110->0 / 111->1 / 126->1 break and then stay strictly ordered from zero. Both
// the 17/s and the later 34/s charge steps are required, and the derived native
// animation state must stay 1 for every intermediate charge value.
func TestRealPVFVolcanoBarrierClockResumeCoalesceKeepsBreakHistory(t *testing.T) {
	f := newRaidVolcanoCrossSquadFixture(t)
	baseline := f.battle.Anton.SymbolSequence()

	f.service.queueCurrentRaidDungeonProgress(f.liveState())
	f.waitPeerCursor(t, baseline)
	f.peerConn().reset()

	exchangeVolcanoBarrier(t, f)
	f.waitPeerCursor(t, f.battle.Anton.SymbolSequence())

	// First delayed callback: 180 replayed seconds at the healthy 17/s rate,
	// which also opens the hatchery and sets the infected 34/s rate.
	advanceVolcanoClock(t, f, 180*time.Second)
	// Second delayed callback: the infected 34/s rate.
	advanceVolcanoClock(t, f, 10*time.Second)

	writes := volcanoFlatten(volcanoOrderedFrames(t, splitVolcanoUpperFrames(t, f.peerConn().snapshot())))
	if len(writes) < 4 || !volcanoEqualSequence(writes[:3], volcanoPairSequence(110, 0, 111, 1, 126, 1)) {
		t.Fatalf("coalesced clock dropped the live break history: %v", writes[:min(len(writes), 5)])
	}
	previous := uint32(0)
	regen, saw17, saw34 := 0, false, false
	for i := 3; i < len(writes); i++ {
		if writes[i].ID != 110 {
			continue
		}
		value := writes[i].Value
		if value <= previous {
			t.Fatalf("regenerated barrier not ordered at index %d: %d then %d", i, previous, value)
		}
		switch value - previous {
		case 17:
			saw17 = true
		case 34:
			saw34 = true
		}
		previous = value
		regen++
	}
	if regen == 0 || !saw17 || !saw34 {
		t.Fatalf("expected ordered 17/s and 34/s regen steps: regen=%d saw17=%v saw34=%v values=%v", regen, saw17, saw34, volcanoSymbolValues(writes, 110))
	}
	if previous != f.battle.Anton.NativeSymbols()[110] {
		t.Fatalf("peer last regen=%d want current barrier %d", previous, f.battle.Anton.NativeSymbols()[110])
	}
	// The native animation state must not be rewritten while the barrier merely
	// recharges: exactly one derived write (111->1) and no return to zero yet.
	if got := volcanoSymbolValues(writes, raid.AntonVolcanoOpenSymbolID); !volcanoEqualValues(got, []uint32{1}) {
		t.Fatalf("native animation state changed during recharge: 111=%v want [1]", got)
	}
	t.Logf("observed: coalesced callbacks kept [110->0,111->1,126->1] then %d ordered regen writes to %d (17/s and 34/s), 111 stayed 1", regen, previous)
	dumpVolcano583Frames(t, "coalesce-regen", f.peerConn().snapshot())
}

func advanceVolcanoClock(t *testing.T, f *raidVolcanoCrossSquadFixture, delta time.Duration) {
	t.Helper()
	states, errs := f.service.advanceCurrentRaidClocks(f.battle.Anton.LastTime().Add(delta))
	for _, err := range errs {
		t.Fatalf("raid clock error: %v", err)
	}
	if len(states) == 0 {
		t.Fatalf("coalesced clock callback produced no changed state after %s", delta)
	}
	for _, state := range states {
		f.service.queueCurrentRaidDungeonProgress(state)
	}
	f.waitPeerCursor(t, f.battle.Anton.SymbolSequence())
}

// Test 3: after the release, compare how a newly entering volcano session is
// served. It reports the snapshot's MOVIE BARRIER RECOVER (127) value; no
// "clear 127 back to zero" fix is assumed or asserted.
func TestRealPVFVolcanoBarrierEnteringSessionSnapshotVersusCursor(t *testing.T) {
	f := newRaidVolcanoCrossSquadFixture(t)
	exchangeVolcanoBarrier(t, f)
	if got := f.battle.Anton.NativeSymbols()[110]; got != 0 {
		t.Fatalf("break barrier=%d want 0", got)
	}
	if f.battle.Anton.NativeSymbols()[126] != 1 {
		t.Fatalf("break did not set MOVIE BARRIER BREAK: %v", f.battle.Anton.NativeSymbols()[126])
	}
	sequence := f.battle.Anton.SymbolSequence()

	// Both observers must exist before the frozen state is captured, because the
	// projection wrapper validates membership against the state it is handed.
	entering := f.addVolcanoMember(t, 101, "account-21")
	initial := f.addVolcanoMember(t, 102, "account-22")
	state := f.liveState()

	// Entering/reconnect projection (production initial=false path).
	enteringConn := entering.conn.(*raidVolcanoPeerConn)
	enteringConn.reset()
	if err := f.service.sendCurrentRaidProgressProjection(entering, state, false, false); err != nil {
		t.Fatal(err)
	}
	enteringFrames := splitVolcanoSymbolFrames(t, splitVolcanoUpperFrames(t, enteringConn.snapshot()))
	if len(enteringFrames.sentinel) != 1 || len(enteringFrames.snapshot) == 0 {
		t.Fatalf("entering session 583 frames sentinel=%v snapshot=%v", enteringFrames.sentinel, enteringFrames.snapshot)
	}
	// The discriminator is frame kind, not value adjacency: a fresh snapshot
	// carries 110 and 126 as adjacent ids, which must not be read as the live
	// transition. A swallowed cursor delivers no journal frame at all.
	if len(enteringFrames.journal) != 0 {
		t.Fatalf("entering session replayed journal writes despite a swallowed cursor: %v", enteringFrames.journal)
	}
	snapshot := enteringFrames.snapshot
	if !volcanoHasSymbol(snapshot, 126, 1) {
		t.Fatalf("entering snapshot missing MOVIE BARRIER BREAK=1: %v", snapshot)
	}
	if !volcanoHasSymbol(snapshot, raid.AntonVolcanoOpenSymbolID, 1) {
		t.Fatalf("entering snapshot missing native animation state 111=1: %v", snapshot)
	}
	recoverValues := volcanoSymbolValues(snapshot, 127)
	if len(recoverValues) != 1 || recoverValues[0] != 1 {
		t.Fatalf("entering snapshot MOVIE BARRIER RECOVER(127)=%v (observed; no reverse-clear fix assumed)", recoverValues)
	}
	entering.raidProjection.mu.Lock()
	enteringCursor := entering.raidProjection.symbolCursor
	entering.raidProjection.mu.Unlock()
	if enteringCursor != sequence {
		t.Fatalf("entering cursor=%d want swallowed to current sequence %d", enteringCursor, sequence)
	}
	t.Logf("observed: entering snapshot keeps MOVIE BARRIER RECOVER(127)=1 alongside BREAK(126)=1; cursor swallowed to %d, live break transition not replayed", enteringCursor)

	// Phase-start style projection (production initial=true path) replays the
	// ordered journal, so the live break transition is retained for a fresh
	// subscriber that asks for initialization.
	initialConn := initial.conn.(*raidVolcanoPeerConn)
	initialConn.reset()
	if err := f.service.sendCurrentRaidProgressProjection(initial, state, true, false); err != nil {
		t.Fatal(err)
	}
	initialFrames := splitVolcanoSymbolFrames(t, splitVolcanoUpperFrames(t, initialConn.snapshot()))
	if len(initialFrames.journal) == 0 {
		t.Fatalf("initialized session received no ordered journal sentinel=%v snapshot=%d", initialFrames.sentinel, len(initialFrames.snapshot))
	}
	barrierHistory := volcanoSymbolValues(initialFrames.journal, 110)
	if len(barrierHistory) == 0 || barrierHistory[0] != 10000 {
		t.Fatalf("initialized session barrier history does not start at 10000: %v", barrierHistory)
	}
	if animationHistory := volcanoSymbolValues(initialFrames.journal, raid.AntonVolcanoOpenSymbolID); len(animationHistory) == 0 || animationHistory[0] != 0 {
		t.Fatalf("initialized journal animation history does not start at 0: %v", animationHistory)
	}
	if !volcanoContainsSequence(initialFrames.journal, volcanoPairSequence(110, 0, 111, 1, 126, 1)...) {
		t.Fatalf("initialized session lost the ordered break history: %v", initialFrames.journal)
	}
	dumpVolcano583Frames(t, "entering-snapshot", enteringConn.snapshot())
	dumpVolcano583Frames(t, "initial-journal", initialConn.snapshot())
}

// Test 4: when the barrier fully recovers to 10000 the derived native animation
// state returns to 0 and the formal PVF MOVIE BARRIER RECOVER (127)=1 follows,
// delivered as an ordered triple after the break triple.
func TestRealPVFVolcanoBarrierFullRecoveryReturnsNativeStateToZero(t *testing.T) {
	f := newRaidVolcanoCrossSquadFixture(t)
	baseline := f.battle.Anton.SymbolSequence()

	f.service.queueCurrentRaidDungeonProgress(f.liveState())
	f.waitPeerCursor(t, baseline)
	f.peerConn().reset()

	exchangeVolcanoBarrier(t, f)
	f.waitPeerCursor(t, f.battle.Anton.SymbolSequence())

	// One long delayed callback replays the recharge past full recovery.
	advanceVolcanoClock(t, f, 900*time.Second)

	writes := volcanoFlatten(volcanoOrderedFrames(t, splitVolcanoUpperFrames(t, f.peerConn().snapshot())))
	if len(writes) < 6 || !volcanoEqualSequence(writes[:3], volcanoPairSequence(110, 0, 111, 1, 126, 1)) {
		t.Fatalf("recovery stream did not start at the break triple: %v", writes[:min(len(writes), 5)])
	}
	if !volcanoContainsSequence(writes, volcanoPairSequence(110, 10000, 111, 0, 127, 1)...) {
		t.Fatalf("recovery stream lost the [110->10000,111->0,127->1] triple; tail=%v", writes[len(writes)-min(len(writes), 6):])
	}
	if got := volcanoSymbolValues(writes, raid.AntonVolcanoOpenSymbolID); !volcanoEqualValues(got, []uint32{1, 0}) {
		t.Fatalf("native animation history=%v want exactly [1,0]", got)
	}
	if got := f.battle.Anton.NativeSymbols()[110]; got != 10000 {
		t.Fatalf("recovered barrier=%d want 10000", got)
	}
	if got := f.battle.Anton.NativeSymbols()[raid.AntonVolcanoOpenSymbolID]; got != 0 {
		t.Fatalf("recovered native animation state=%d want 0", got)
	}
	t.Logf("observed: full recovery delivered [110->10000,111->0,127->1] with native animation history [1,0]")
	dumpVolcano583Frames(t, "full-recovery", f.peerConn().snapshot())
}

// volcanoSymbolFrameKinds separates the three NOTI583 shapes production can
// send: the native reset sentinel (one zero pair), a current-state snapshot
// (unique ascending ids in BuildSymbolBody order), and the ordered journal.
type volcanoSymbolFrameKinds struct {
	sentinel []raid.SymbolValue
	snapshot []raid.SymbolValue
	journal  []raid.SymbolValue
}

func splitVolcanoSymbolFrames(t *testing.T, frames []upperFrame) volcanoSymbolFrameKinds {
	t.Helper()
	var kinds volcanoSymbolFrameKinds
	for _, frame := range frames {
		if frame.msgID != 583 {
			continue
		}
		writes := antonProjectionWrites(t, frame.body)
		if len(writes) == 1 && writes[0].ID == 0 {
			kinds.sentinel = append(kinds.sentinel, writes...)
			continue
		}
		ascending := true
		for i := 1; i < len(writes); i++ {
			if writes[i].ID <= writes[i-1].ID {
				ascending = false
				break
			}
		}
		if ascending {
			kinds.snapshot = append(kinds.snapshot, writes...)
		} else {
			kinds.journal = append(kinds.journal, writes...)
		}
	}
	return kinds
}

func volcanoContainsPair(writes []raid.SymbolValue, id0, value0, id1, value1 uint32) bool {
	return volcanoContainsSequence(writes, volcanoPairSequence(id0, value0, id1, value1)...)
}

// volcanoContainsSequence reports whether want appears as a contiguous ordered
// run inside writes.
func volcanoContainsSequence(writes []raid.SymbolValue, want ...raid.SymbolValue) bool {
	if len(want) == 0 {
		return true
	}
	for i := 0; i+len(want) <= len(writes); i++ {
		if volcanoEqualSequence(writes[i:i+len(want)], want) {
			return true
		}
	}
	return false
}

// dumpVolcano583Frames writes optional standalone native-transport evidence for
// every classification-0 NOTI583 frame present in a sink buffer.
//
// Set DNF90_ANTON_NATIVE_DIR to a directory to enable it; otherwise it is a
// no-op and the tests are unchanged. Only symbol event frames are written: the
// 16-byte upper header carries classification/msgID/total/checksum/sequence plus
// three zero pad bytes, and the body is a u8 count plus u32 symbol id/value
// pairs, so no account,
// character, item or credential data is emitted. Op583 is sent with
// classification 0, where this build applies no body codec, so these bytes are
// the real wire form for the body; the manifest records that fact and the
// header size used.
func dumpVolcano583Frames(t *testing.T, scenario string, raw []byte) {
	t.Helper()
	dir := strings.TrimSpace(os.Getenv(volcanoNativeEvidenceEnv))
	if dir == "" {
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("anton native evidence dir %q: %v", dir, err)
	}
	headerSize := volcanoUpperHeaderSize
	var manifest strings.Builder
	fmt.Fprintf(&manifest, "{\n  \"scenario\": %q,\n  \"msg_id\": 583,\n  \"header_size\": %d,\n  \"body_codec\": %q,\n  \"frames\": [\n",
		scenario, headerSize, "plaintext (classification 0 op583 body is not body-encoded in this build)")
	wrote := 0
	for index, frame := range splitVolcanoRawFrames(t, raw) {
		if binary.LittleEndian.Uint16(frame[1:3]) != 583 {
			continue
		}
		name := fmt.Sprintf("%s-583-%02d.bin", scenario, index)
		if err := os.WriteFile(filepath.Join(dir, name), frame, 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		sum := sha256.Sum256(frame)
		body := frame[headerSize:]
		writes := antonProjectionWrites(t, body)
		pairs := make([]string, 0, len(writes))
		for _, write := range writes {
			pairs = append(pairs, fmt.Sprintf("{\"id\": %d, \"value\": %d}", write.ID, write.Value))
		}
		fmt.Fprintf(&manifest, "    {\"file\": %q, \"sha256\": %q, \"classification\": %d, \"sequence\": %d, \"frame_len\": %d, \"body_len\": %d, \"symbols\": [%s]},\n",
			name, fmt.Sprintf("%x", sum), frame[0], binary.LittleEndian.Uint16(frame[11:13]), len(frame), len(body), strings.Join(pairs, ", "))
		wrote++
	}
	out := manifest.String()
	if wrote == 0 {
		out += "  ]\n}\n"
	} else {
		out = strings.TrimSuffix(out, ",\n") + "\n  ]\n}\n"
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest-"+scenario+".json"), []byte(out), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	t.Logf("anton native evidence: wrote %d NOTI583 frame(s) for %s into %s", wrote, scenario, dir)
}
