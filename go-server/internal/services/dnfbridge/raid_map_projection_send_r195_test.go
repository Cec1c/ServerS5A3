package dnfbridge

import (
	"encoding/binary"
	"errors"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/raid"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

type r195MapFrame struct {
	id   uint16
	body []byte
}

type r195MapProjectionFixture struct {
	base    *raidStartGuardFixture
	service *Service
	session *gameSession
	state   *runtimeRaidState
	conn    *bufferConn
}

// Synthetic raid owner: no PVF, but the real authenticated sender path.
func newR195MapProjectionFixture(t *testing.T, mode byte) *r195MapProjectionFixture {
	t.Helper()
	f := newRaidStartGuardFixture(t, nil, map[string]dnfrepo.ItemStack{})
	t.Cleanup(func() { _ = f.logger.Close() })
	f.service.options.currentClientVersion = "official-24.9"
	session := f.session()
	session.loginPacketSeen, session.accountFromToken = true, true
	f.service.bindGameSessionCharacter(session, 19)
	now := time.Now()
	f.state.Started = true
	f.state.Battle = &runtimeRaidBattle{
		InstanceID:     "00112233445566778899aabbccddeeff",
		Mode:           mode,
		Phase:          0,
		ChannelID:      session.channel.ID,
		ChannelType:    session.channel.Type,
		StartedAt:      now,
		EnterAfter:     now,
		Deadline:       now.Add(time.Hour),
		Configuration:  raid.BattleConfiguration{PhaseLimitSeconds: []uint32{3600, 3600}},
		MemberAccounts: map[uint16]string{19: "account-1"},
		Dungeons: []raid.DungeonState{
			{ID: 5054, State: 3},
			{ID: 5055, State: 0},
		},
	}
	return &r195MapProjectionFixture{base: f, service: f.service, session: session, state: f.state, conn: f.conn}
}

func (f *r195MapProjectionFixture) drain(t *testing.T) []r195MapFrame {
	t.Helper()
	stream := append([]byte(nil), f.conn.write.Bytes()...)
	f.conn.write.Reset()
	var frames []r195MapFrame
	for len(stream) > 0 {
		packet, rest := splitGameServerUpperPacket(t, stream)
		stream = rest
		frames = append(frames, r195MapFrame{id: packet.Header.MsgID, body: append([]byte(nil), packet.Body...)})
	}
	return frames
}

func r195Map585(frames []r195MapFrame) [][]byte {
	var bodies [][]byte
	for _, frame := range frames {
		if frame.id == 585 {
			bodies = append(bodies, frame.body)
		}
	}
	return bodies
}

func (f *r195MapProjectionFixture) send(t *testing.T) {
	t.Helper()
	if err := f.service.sendCurrentRaidBattleState(f.session, cloneRuntimeRaidState(*f.state)); err != nil {
		t.Fatal(err)
	}
}

func (f *r195MapProjectionFixture) project(t *testing.T, initial, resubscribe bool) {
	t.Helper()
	if err := f.service.sendCurrentRaidProgressProjection(f.session, cloneRuntimeRaidState(*f.state), initial, resubscribe); err != nil {
		t.Fatal(err)
	}
}

// sendCurrentRaidBattleState publishes the full map and seeds the delta cache.
// The immediately following progress projection must not publish it again.
func TestR195LukeMapProjectionBattleStateSeedsDeltaCache(t *testing.T) {
	f := newR195MapProjectionFixture(t, 2)
	b := f.state.Battle
	f.send(t)
	maps := r195Map585(f.drain(t))
	if len(maps) != 1 || maps[0][1] != 2 {
		t.Fatalf("battle state maps=%v want one full 585", maps)
	}
	b.ProgressVersion++
	f.project(t, false, false)
	if maps := r195Map585(f.drain(t)); len(maps) != 0 {
		t.Fatalf("unchanged map republished %d times", len(maps))
	}
}

// The regression this fix exists for: 5054 already sits at state 3, another
// sanctum changes, and the wire must not carry 5054 again.
func TestR195LukeMapProjectionPublishesOnlyChangedRow(t *testing.T) {
	f := newR195MapProjectionFixture(t, 2)
	b := f.state.Battle
	f.send(t)
	f.drain(t)

	b.Dungeons[1].State = 1
	b.ProgressVersion++
	f.project(t, false, false)
	maps := r195Map585(f.drain(t))
	if len(maps) != 1 {
		t.Fatalf("map frames=%d want 1", len(maps))
	}
	body := maps[0]
	if body[0] != 0 || body[1] != 1 || body[6] != 1 {
		t.Fatalf("body=%x want one row", body)
	}
	if id := binary.LittleEndian.Uint32(body[2:6]); id != 5055 {
		t.Fatalf("row id=%d want 5055 (5054 must not be re-reported)", id)
	}
	if infection := binary.LittleEndian.Uint32(body[7:11]); infection != 0 {
		t.Fatalf("infection=%d want 0", infection)
	}
}

func TestR195LukeMapProjectionNewConnectionGetsFullSnapshot(t *testing.T) {
	f := newR195MapProjectionFixture(t, 2)
	b := f.state.Battle
	f.send(t)
	f.drain(t)

	b.ProgressVersion++
	next := f.base.session()
	next.loginPacketSeen, next.accountFromToken = true, true
	f.service.bindGameSessionCharacter(next, 19)
	if err := f.service.sendCurrentRaidProgressProjection(next, cloneRuntimeRaidState(*f.state), false, true); err != nil {
		t.Fatal(err)
	}
	maps := r195Map585(f.drain(t))
	if len(maps) != 1 || maps[0][1] != 2 {
		t.Fatalf("resubscribe maps=%v want one full 585", maps)
	}
	if id := binary.LittleEndian.Uint32(maps[0][2:6]); id != 5054 || maps[0][6] != 3 {
		t.Fatalf("full snapshot must carry 5054=3, body=%x", maps[0])
	}
}

type r195FailWriteConn struct{ bufferConn }

func (c *r195FailWriteConn) Write([]byte) (int, error) {
	return 0, errors.New("probe transport failure")
}

func TestR195LukeMapProjectionFailedSendDoesNotCommitCache(t *testing.T) {
	f := newR195MapProjectionFixture(t, 2)
	b := f.state.Battle
	f.project(t, true, false)
	if maps := r195Map585(f.drain(t)); len(maps) != 1 {
		t.Fatalf("initial maps=%d want 1", len(maps))
	}

	b.Dungeons[1].State = 1
	b.ProgressVersion++
	saved := f.session.conn
	f.session.conn = &r195FailWriteConn{}
	if err := f.service.sendCurrentRaidProgressProjection(f.session, cloneRuntimeRaidState(*f.state), false, false); err == nil {
		t.Fatal("expected transport failure")
	}
	f.session.conn = saved
	f.session.raidProjection.failed = false

	// If the failed send had committed 5055=1, this 1->0 transition would emit.
	b.Dungeons[1].State = 0
	b.ProgressVersion++
	f.project(t, false, false)
	if maps := r195Map585(f.drain(t)); len(maps) != 0 {
		t.Fatalf("failed send committed the row cache: %d map frames", len(maps))
	}
}

// The native state-3 prompt is proven on the Luke controller only; Anton keeps
// the historical full-map publication.
func TestR195AntonMapProjectionKeepsFullMap(t *testing.T) {
	f := newR195MapProjectionFixture(t, 1)
	b := f.state.Battle
	b.Dungeons = []raid.DungeonState{{ID: 210, State: 3}, {ID: 211, State: 0}}
	f.send(t)
	f.drain(t)

	b.ProgressVersion++
	f.project(t, false, false)
	maps := r195Map585(f.drain(t))
	if len(maps) != 1 || maps[0][1] != 2 {
		t.Fatalf("mode1 maps=%v want the full map", maps)
	}
}
