package dnfbridge

import (
	"context"
	"encoding/binary"
	"reflect"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/dnfenum"
)

func TestAF10StartRefreshKeepsCompactCacheWithoutActorRebuild(t *testing.T) {
	f := newCurrentCrossSceneProfileBatchFixture(t)
	s := f.service
	repos, _ := s.repositoryGroup()
	c, found, err := repos.Character.Load(context.Background(), "29")
	if err != nil || !found {
		t.Fatalf("load existing leader: found=%t err=%v", found, err)
	}
	c.Name, c.Job = "existing-leader", "0"
	if err := repos.Character.Save(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	peers := []*gameSession{f.target, f.actor, f.viewer2}
	state := runtimeRaidState{RaidKey: uint32(s.channelAdvertiseID())<<24 | 23<<16 | 29, LeaderID: 29, CurrentLayout: true}
	for _, peer := range peers {
		peer.channel = channelcatalog.Channel{ID: 23, Type: dnfenum.RaidChannelType}
		peer.loginPacketSeen, peer.accountFromToken = true, true
		peer.connectionTownActorOwnerChannel, peer.townActorOwnerChannel = 23, 23
		state.Members = append(state.Members, s.newRuntimeRaidMemberLocked(peer.selectedCharacterID, 1, byte(len(state.Members))))
	}
	s.raids = map[uint32]*runtimeRaidState{state.RaidKey: &state}
	f.reset()
	if err := s.broadcastRuntimeRaidRefresh(state); err != nil {
		t.Fatal(err)
	}
	warmActors := 0
	for _, frame := range splitUpperFrames(t, f.targetConn.write.Bytes()) {
		if frame.msgID == 2 && len(frame.body) > 0 && frame.body[0] == 8 {
			warmActors++
		}
	}
	if warmActors != 2 {
		t.Fatalf("fixture did not install both existing raid actors: %d", warmActors)
	}
	f.reset()
	for _, peer := range peers {
		peer.dungeon.mu.Lock()
	}
	done := make(chan error, 1)
	go func() { done <- s.broadcastRuntimeRaidStartRefresh(state) }()
	blocked := false
	select {
	case err := <-done:
		if err != nil {
			t.Error(err)
		}
	case <-time.After(350 * time.Millisecond):
		blocked = true
	}
	for _, peer := range peers {
		peer.dungeon.mu.Unlock()
	}
	if blocked {
		<-done
		t.Fatal("start refresh waited for unrelated cosmetic actor preparation")
	}
	for i, conn := range []*bufferConn{f.targetConn, f.actorConn, f.viewer2Conn} {
		cache, roster := -1, -1
		for n, frame := range splitUpperFrames(t, conn.write.Bytes()) {
			if frame.msgID == 2 {
				t.Fatalf("recipient%d reinstalled actor during start-only refresh", i)
			}
			if frame.msgID == 752 {
				cache = n
				if len(frame.body) != 4+57*len(peers) {
					t.Fatalf("recipient%d compact cache lost an existing actor: bytes=%d count=%d", i, len(frame.body), binary.LittleEndian.Uint32(frame.body))
				}
			}
			if frame.msgID == 591 {
				roster = n
			}
		}
		if cache < 0 || roster <= cache {
			t.Fatalf("recipient%d compact-cache/roster order=%d/%d", i, cache, roster)
		}
	}
}

func TestRealPVFAF10AcceptedClearPushesTableWithoutInfoRequest(t *testing.T) {
	f, session, _ := realAntonProjectionFixture(t, 0)
	f.state.CurrentLayout = true
	seedClearWireCharacterProfiles(t, f)
	if err := f.service.sendCurrentRaidBattleState(session, cloneRuntimeRaidState(*f.state)); err != nil {
		t.Fatal(err)
	}
	collect := func(want byte) {
		t.Helper()
		f.conn.write.Reset()
		if err := f.service.sendCurrentRaidProgressProjection(session, cloneRuntimeRaidState(*f.state), false, false); err != nil {
			t.Fatal(err)
		}
		var rosters []map[uint16]byte
		for _, frame := range splitUpperFrames(t, f.conn.write.Bytes()) {
			if frame.msgID == 591 {
				if len(frame.body) < 9 || binary.LittleEndian.Uint32(frame.body) != f.state.RaidKey || binary.LittleEndian.Uint32(frame.body[4:]) != 3 {
					t.Fatal("table owner/mode mismatch")
				}
				cursor := clearWireCursor{t: t, b: frame.body[8:]}
				rosters = append(rosters, cursor.roster())
			}
			if frame.msgID == 752 || frame.msgID == 2 || frame.msgID == 587 {
				t.Fatalf("live clear replayed actor/start packet%d", frame.msgID)
			}
		}
		if len(rosters) != 1 || !reflect.DeepEqual(rosters[0], map[uint16]byte{19: want}) {
			t.Fatalf("accepted clear did not push native table: got=%v want=%d", rosters, want)
		}
	}
	for n := byte(1); n <= 2; n++ {
		runtime := raidCompletedRuntimeFixture(t, session, 210)
		if err := f.service.freezeCurrentRaidDungeonGroupEntry(session, runtime, nil); err != nil {
			t.Fatal(err)
		}
		if _, changed, err := f.service.reduceCurrentRaidDungeonClearLocked(session, runtime, time.Now()); err != nil || !changed {
			t.Fatal("owned clear was not accepted", changed, err)
		}
		collect(n)
		if _, changed, err := f.service.reduceCurrentRaidDungeonClearLocked(session, runtime, time.Now()); err != nil || changed {
			t.Fatal("replayed clear changed the journal", changed, err)
		}
		f.state.Battle.ProgressVersion++ // unrelated same-phase update
		f.conn.write.Reset()
		if err := f.service.sendCurrentRaidProgressProjection(session, cloneRuntimeRaidState(*f.state), false, false); err != nil {
			t.Fatal(err)
		}
		for _, frame := range splitUpperFrames(t, f.conn.write.Bytes()) {
			if frame.msgID == 591 {
				t.Fatal("unchanged clear counters replayed the table")
			}
		}
	}
}
