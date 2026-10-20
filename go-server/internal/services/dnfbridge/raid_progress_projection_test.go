package dnfbridge

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math"
	"net"
	"os"
	"strconv"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/raid"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestRealPVFAntonProgressBeforeInitialSendDoesNotLoseStartOrRollBackMap(t *testing.T) {
	f, session, now := realAntonProjectionFixture(t, 0)
	initial := cloneRuntimeRaidState(*f.state)
	b := f.state.Battle
	for n := 0; n < 4; n++ {
		next, _, err := b.Anton.ClearDungeon("before-start-"+strconv.Itoa(n), 210, b.Anton.Epoch(), now)
		if err != nil {
			t.Fatal(err)
		}
		b.Anton = next
	}
	b.Dungeons, b.ProgressVersion = b.Anton.Dungeons(), 1
	if err := f.service.sendCurrentRaidProgressProjection(session, initial, false, false); err != nil {
		t.Fatal(err)
	}
	if session.raidProjection.startTransitionSent {
		t.Fatal("map restoration consumed live start transition")
	}
	f.conn.write.Reset()
	if err := f.service.sendCurrentRaidBattleState(session, initial); err != nil {
		t.Fatal(err)
	}
	stream := append([]byte(nil), f.conn.write.Bytes()...)
	wantMap, _ := raid.BuildDungeonStateBody(b.Dungeons, b.Anton.InfectionDungeon())
	starts := 0
	for len(stream) > 0 {
		packet, rest := splitGameServerUpperPacket(t, stream)
		if packet.Header.MsgID == 587 {
			starts++
		}
		if packet.Header.MsgID == 585 && !bytes.Equal(packet.Body, wantMap) {
			t.Fatal("delayed initial packet rolled map back")
		}
		stream = rest
	}
	if starts != 1 || !session.raidProjection.startTransitionSent {
		t.Fatal("actual start transition omitted", starts)
	}
	f.conn.write.Reset()
	if err := f.service.sendCurrentRaidBattleState(session, initial); err != nil || f.conn.write.Len() != 0 {
		t.Fatal("start transition replayed", err)
	}
}

func TestRealPVFAntonOrderedProjectionBatchesBeyondNativeByteCount(t *testing.T) {
	f, _, now := realAntonProjectionFixture(t, 0)
	state := f.state.Battle.Anton
	for cycle := 0; cycle < 16; cycle++ {
		for n := 0; n < 4; n++ {
			next, _, err := state.ClearDungeon("batch-smoke-"+strconv.Itoa(cycle*4+n), 210, state.Epoch(), now)
			if err != nil {
				t.Fatal(err)
			}
			state = next
		}
		now = now.Add(301 * time.Second)
		next, err := state.Tick(now)
		if err != nil {
			t.Fatal(err)
		}
		state = next
	}
	packets, err := currentRaidOrderedSymbolProjection(state, 0)
	if err != nil || len(packets) < 2 {
		t.Fatal("journal did not split into native-sized frames", len(packets), err)
	}
	wanted, err := state.SymbolWritesAfter(0)
	if err != nil {
		t.Fatal(err)
	}
	var writes []raid.SymbolValue
	for _, packet := range packets {
		if packet.opcode != 583 || packet.symbolCursor != uint64(len(writes)+int(packet.body[0])) {
			t.Fatal("batch cursor is not cumulative")
		}
		writes = append(writes, antonProjectionWrites(t, packet.body)...)
	}
	if len(writes) != len(wanted) || packets[len(packets)-1].symbolCursor != state.SymbolSequence() {
		t.Fatal("multi-frame journal truncated")
	}
	for i, w := range writes {
		if w != wanted[i].SymbolValue {
			t.Fatal("multi-frame order changed", i)
		}
	}
}

func realAntonProjectionFixture(t *testing.T, phase int) (*raidStartGuardFixture, *gameSession, time.Time) {
	t.Helper()
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("requires current public PVF")
	}
	a, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	p, err := raid.LoadAntonProgram(a)
	if err != nil {
		t.Fatal(err)
	}
	config, err := raid.LoadBattleConfiguration(a, "etc/raid/anton.etc")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	progress, err := p.BeginPhase(phase, now, 19703)
	if err != nil {
		t.Fatal(err)
	}
	f := raidSafetyFixture(t, a, 1)
	f.service.options.currentClientVersion = "official-24.9"
	session := f.session()
	session.loginPacketSeen, session.accountFromToken = true, true
	f.service.bindGameSessionCharacter(session, 19)
	f.state.Started = true
	f.state.Battle = &runtimeRaidBattle{InstanceID: "00112233445566778899aabbccddeeff", Mode: 1, Phase: byte(phase), ChannelID: session.channel.ID, ChannelType: session.channel.Type,
		StartedAt: now, EnterAfter: now, Deadline: now.Add(time.Hour), Configuration: config, Anton: progress, Dungeons: progress.Dungeons(), MemberAccounts: map[uint16]string{19: session.accountID}}
	return f, session, now
}

func antonProjectionWrites(t *testing.T, body []byte) []raid.SymbolValue {
	t.Helper()
	if len(body) != 1+int(body[0])*8 {
		t.Fatal("invalid symbol frame length")
	}
	writes := make([]raid.SymbolValue, body[0])
	for i := range writes {
		writes[i] = raid.SymbolValue{ID: binary.LittleEndian.Uint32(body[1+8*i:]), Value: binary.LittleEndian.Uint32(body[5+8*i:])}
	}
	return writes
}

func TestRealPVFAntonProgressProjectionCoalescesMapsWithoutDroppingEvents(t *testing.T) {
	f, session, now := realAntonProjectionFixture(t, 0)
	if err := f.service.sendCurrentRaidBattleState(session, cloneRuntimeRaidState(*f.state)); err != nil {
		t.Fatal(err)
	}
	f.conn.write.Reset()
	cursor := session.raidProjection.symbolCursor
	b := f.state.Battle
	captured := cloneRuntimeRaidState(*f.state)
	for cycle := 0; cycle < 2; cycle++ {
		for n := 0; n < 4; n++ {
			next, _, err := b.Anton.ClearDungeon("projection-smoke-"+strconv.Itoa(cycle*4+n), 210, b.Anton.Epoch(), now)
			if err != nil {
				t.Fatal(err)
			}
			b.Anton = next
		}
		now = now.Add(301 * time.Second)
		next, err := b.Anton.Tick(now)
		if err != nil {
			t.Fatal(err)
		}
		b.Anton, b.Dungeons, b.ProgressVersion = next, next.Dungeons(), b.ProgressVersion+1
	}
	expected, err := b.Anton.SymbolWritesAfter(cursor)
	if err != nil {
		t.Fatal(err)
	}
	// This obsolete callback must select the current cumulative journal.
	if err := f.service.sendCurrentRaidProgressProjection(session, captured, false, false); err != nil {
		t.Fatal(err)
	}
	stream := append([]byte(nil), f.conn.write.Bytes()...)
	var actual []raid.SymbolValue
	mapCount, killCount, failOnCount, failOffCount := 0, 0, 0, 0
	for len(stream) > 0 {
		packet, rest := splitGameServerUpperPacket(t, stream)
		switch packet.Header.MsgID {
		case 583:
			actual = append(actual, antonProjectionWrites(t, packet.Body)...)
		case 585:
			mapCount++
		case 596:
		default:
			t.Fatal("progress replays transition/unknown packet", packet.Header.MsgID)
		}
		stream = rest
	}
	if len(actual) != len(expected) || mapCount != 1 {
		t.Fatal("coalesced projection lost writes", len(actual), len(expected), mapCount)
	}
	for i, w := range actual {
		if w != expected[i].SymbolValue {
			t.Fatal("write order changed", i, w, expected[i])
		}
		if w.ID == 102 && w.Value == 1 {
			killCount++
		}
		if w.ID == 104 {
			if w.Value == 1 {
				failOnCount++
			} else {
				failOffCount++
			}
		}
	}
	// The PVF may explicitly SET the same FAIL value for several due rules;
	// all those writes above must be retained, not collapsed to one per cycle.
	if killCount != 2 || failOnCount < 2 || failOffCount != 2 || session.raidProjection.symbolCursor != b.Anton.SymbolSequence() {
		t.Fatal("repeated failure effect lost", killCount, failOnCount, failOffCount)
	}
	f.conn.write.Reset()
	if err := f.service.sendCurrentRaidProgressProjection(session, captured, false, false); err != nil || f.conn.write.Len() != 0 {
		t.Fatal("duplicate callback replayed", err)
	}
}

func TestRealPVFAntonProgressProjectionReconnectRestoresWithoutOldMovieOrDeath(t *testing.T) {
	f, session, now := realAntonProjectionFixture(t, 0)
	b := f.state.Battle
	for n := 0; n < 4; n++ {
		next, _, err := b.Anton.ClearDungeon("rejoin-smoke-"+strconv.Itoa(n), 210, b.Anton.Epoch(), now)
		if err != nil {
			t.Fatal(err)
		}
		b.Anton = next
	}
	next, err := b.Anton.Tick(now.Add(301 * time.Second))
	if err != nil {
		t.Fatal(err)
	}
	b.Anton, b.Dungeons, b.ProgressVersion = next, next.Dungeons(), 1
	if b.Anton.NativeSymbols()[102] != 1 {
		t.Fatal("fixture did not contain past death")
	}
	replacement := f.session()
	replacement.loginPacketSeen, replacement.accountFromToken = true, true
	f.service.bindGameSessionCharacter(replacement, 19)
	f.conn.write.Reset()
	state := cloneRuntimeRaidState(*f.state)
	if err := f.service.sendCurrentRaidProgressProjection(session, state, false, true); err == nil || f.conn.write.Len() != 0 {
		t.Fatal("replaced login sent", err)
	}
	if err := f.service.sendCurrentRaidProgressProjection(replacement, state, false, true); err != nil {
		t.Fatal(err)
	}
	stream := append([]byte(nil), f.conn.write.Bytes()...)
	symbolPackets, phaseTimers := 0, 0
	for len(stream) > 0 {
		packet, rest := splitGameServerUpperPacket(t, stream)
		if packet.Header.MsgID == 587 {
			t.Fatal("reconnect replayed start animation")
		}
		if packet.Header.MsgID == 596 && packet.Body[0] == 0 {
			phaseTimers++
		}
		if packet.Header.MsgID == 583 {
			symbolPackets++
			for _, w := range antonProjectionWrites(t, packet.Body) {
				if (w.ID == 102 || w.ID == 103 || w.ID >= 121 && w.ID <= 128 || w.ID >= 104 && w.ID <= 106) && w.Value != 0 {
					t.Fatal("reconnect replayed old effect", w)
				}
			}
		}
		stream = rest
	}
	if symbolPackets != 2 || phaseTimers != 1 || replacement.raidProjection.symbolCursor != b.Anton.SymbolSequence() {
		t.Fatal("reconnect did not subscribe current state", symbolPackets, phaseTimers)
	}
	f.conn.write.Reset()
	if err := f.service.sendCurrentRaidProgressProjection(replacement, state, false, false); err != nil || f.conn.write.Len() != 0 {
		t.Fatal("reconnect cursor did not suppress history", err)
	}
}

func TestRealPVFAntonProgressProjectionRejectsStaleAuthority(t *testing.T) {
	for _, name := range []string{"account", "channel", "phase", "instance", "removed", "login"} {
		t.Run(name, func(t *testing.T) {
			f, session, _ := realAntonProjectionFixture(t, 1)
			state := cloneRuntimeRaidState(*f.state)
			switch name {
			case "account":
				state.Battle.MemberAccounts[19] = "foreign"
			case "channel":
				session.channel.ID++
			case "phase":
				f.state.Battle.Phase = 0
			case "instance":
				f.state.Battle.InstanceID = "ffffffffffffffffffffffffffffffff"
			case "removed":
				f.state.Members = nil
			case "login":
				replacement := f.session()
				replacement.loginPacketSeen, replacement.accountFromToken = true, true
				f.service.bindGameSessionCharacter(replacement, 19)
			}
			if err := f.service.sendCurrentRaidProgressProjection(session, state, false, true); err == nil || f.conn.write.Len() != 0 {
				t.Fatal("stale authority produced bytes", err)
			}
		})
	}
}

type antonPartialWriteConn struct {
	net.Conn
	calls  int
	closed bool
}

func (c *antonPartialWriteConn) Write(body []byte) (int, error) {
	c.calls++
	if c.calls == 2 {
		return len(body) / 2, errors.New("injected partial event frame")
	}
	return c.Conn.Write(body)
}
func (c *antonPartialWriteConn) Close() error { c.closed = true; return c.Conn.Close() }

func TestRealPVFAntonProgressProjectionPartialWriteCannotReplayEvents(t *testing.T) {
	f, session, _ := realAntonProjectionFixture(t, 1)
	conn := &antonPartialWriteConn{Conn: session.conn}
	session.conn = conn
	state := cloneRuntimeRaidState(*f.state)
	if err := f.service.sendCurrentRaidProgressProjection(session, state, true, false); err == nil || !conn.closed || !session.raidProjection.failed {
		t.Fatal("partial frame remained reusable", err)
	}
	calls := conn.calls
	if err := f.service.sendCurrentRaidProgressProjection(session, state, true, false); err == nil || conn.calls != calls {
		t.Fatal("failed socket replayed effects", err)
	}
}

func TestRaidDungeonTimerProjectionDiffAndInvalidDeadlines(t *testing.T) {
	now := time.Unix(1791329738, 0)
	a := raid.AntonTimerKey{Kind: 1, DungeonID: 212}
	b := raid.AntonTimerKey{Kind: 2, DungeonID: 211}
	c := raid.AntonTimerKey{Kind: 3, DungeonID: 219}
	v := raid.AntonTimer{End: now.Add(300 * time.Second), Duration: 300}
	previous := map[raid.AntonTimerKey]raid.AntonTimer{a: v, b: v}
	next := map[raid.AntonTimerKey]raid.AntonTimer{a: v, c: {End: now.Add(180 * time.Second), Duration: 180}}
	packets, err := currentRaidTimerProjection(previous, next)
	if err != nil || len(packets) != 2 {
		t.Fatal("timer diff", packets, err)
	}
	if packets[0].body[0] != 2 || binary.LittleEndian.Uint32(packets[0].body[5:9]) != 0 || binary.LittleEndian.Uint32(packets[0].body[9:]) != 0 || packets[1].body[0] != 3 {
		t.Fatal("removed timer not zeroed in sorted order")
	}
	if packets, err := currentRaidTimerProjection(next, next); err != nil || len(packets) != 0 {
		t.Fatal("unchanged timer emitted", err)
	}
	for _, bad := range []map[raid.AntonTimerKey]raid.AntonTimer{
		{{Kind: 0, DungeonID: 212}: v}, {{Kind: 4, DungeonID: 212}: v}, {{Kind: 1, DungeonID: 0}: v},
		{a: {Duration: 300}}, {a: {End: now, Duration: 0}}, {a: {End: time.Unix(math.MaxUint32+1, 0), Duration: 300}}, {a: {End: time.Unix(10, 0), Duration: 300}},
	} {
		if _, err := currentRaidTimerProjection(nil, bad); err == nil {
			t.Fatal("invalid timer admitted", bad)
		}
	}
}
