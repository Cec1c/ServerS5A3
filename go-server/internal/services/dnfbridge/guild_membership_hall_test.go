package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	"strconv"
	"testing"
)

// Both native success-action1 and NOTI151 can call 0x3705A90 -> 0x36FF950.
// This exercises the corresponding 16-byte movement shape; it does not run
// the native callbacks or establish their notification ordering.
func TestGuildLifecycleNativeHallReturnAfterExit(t *testing.T) {
	s, peers, repos := guildHallFixture(t)
	ctx := context.Background()
	// The older town fixture replaces Character with a read/write fake while
	// its Guild UoW still owns the original store. Use one real memory aggregate
	// for both the transactional exit and the subsequent movement requests.
	consistent := memory.NewMemoryGroup()
	for id := range peers {
		key := strconv.Itoa(int(id))
		c, _, err := repos.Character.Load(ctx, key)
		if err != nil {
			t.Fatal(err)
		}
		if err := consistent.Character.Save(ctx, c); err != nil {
			t.Fatal(err)
		}
		skill, found, err := repos.Skill.Load(ctx, key)
		if err != nil {
			t.Fatal(err)
		}
		if found {
			if err := consistent.Skill.Save(ctx, skill); err != nil {
				t.Fatal(err)
			}
		}
	}
	state, _, err := repos.Settings.Load(ctx, dnfrepo.GuildRegistryScope)
	if err != nil {
		t.Fatal(err)
	}
	if err := consistent.Settings.Save(ctx, state); err != nil {
		t.Fatal(err)
	}
	repos = consistent
	s.repositoryProvider = func() (dnfrepo.Group, bool) { return consistent, true }
	for _, id := range []uint16{29, 30, 31, 33} {
		s.publishTownPresenceInArea(peers[id], 8, 0, 500, 220, 5, 3, "secede-research")
	}
	guildDispatch(t, s, peers[30], 153, []byte{0, 0, 0, 0, 0})
	if !guildLifecycleHasPacket(t, peers[30], 0, 151, nil) {
		t.Fatalf("native reset missing after exit; ACK=%x", guildReply(t, peers[30], 153))
	}
	if _, found := s.onlinePlayers.PlayerForCharacter(30); found {
		t.Fatal("former member still published in the hall")
	}
	for _, peer := range peers {
		peer.conn.(*bufferConn).write.Reset()
	}
	request := buildTownMoveRequest(38, 0, 450, 234, 5)
	binary.LittleEndian.PutUint16(request[7:9], 8)
	for attempt := 0; attempt < 2; attempt++ {
		peers[30].conn.(*bufferConn).write.Reset()
		if err := s.handleTownSetUserArea(peers[30], request); err != nil {
			t.Fatal(err)
		}
		c, found, err := repos.Character.Load(ctx, "30")
		if err != nil || !found || c.Stats["town_id"] != 38 || c.Stats["area_id"] != 0 || c.Stats["pos_x"] != 450 || c.Stats["pos_y"] != 234 {
			t.Fatalf("return attempt=%d found=%v error=%v location=%v", attempt, found, err, c.Stats)
		}
		actor, found := s.onlinePlayers.PlayerForCharacter(30)
		if !found || actor.TownID != 38 || actor.AreaID != 0 {
			t.Fatalf("return attempt=%d public presence missing", attempt)
		}
		if s.onlinePlayers.PeerInSameArea(29, 30) || s.onlinePlayers.PeerInSameArea(31, 30) {
			t.Fatal("return retained hall peers")
		}
		want, err := buildCurrentSceneTransitionBody(38, 0, []currentSceneTransitionRow{{
			ObjectOrResourceKey: 30, Value1: 450, Value2: 234, Value3: 5, Value4: byte(c.Stats["area_state"]),
		}})
		if err != nil {
			t.Fatal(err)
		}
		transitions := 0
		for wire := peers[30].conn.(*bufferConn).write.Bytes(); len(wire) > 0; {
			packet, rest := splitCurrentGameServerUpperPacketAuto(t, wire)
			wire = rest
			if packet.Header.Classification == 0 && packet.Header.MsgID == currentSceneTransitionMsgID {
				transitions++
				if !bytes.Equal(packet.Body, want) {
					t.Fatalf("return attempt=%d scene=%x want=%x", attempt, packet.Body, want)
				}
			}
		}
		if transitions != 1 {
			t.Fatalf("return attempt=%d native scene transitions=%d", attempt, transitions)
		}
	}
	if peers[31].conn.(*bufferConn).write.Len() != 0 || peers[33].conn.(*bufferConn).write.Len() != 0 {
		t.Fatal("return broadcast crossed guild or channel")
	}
}
