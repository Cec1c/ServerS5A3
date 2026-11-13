package dnfbridge

import (
	"context"
	"longheng.io/server/internal/modules/dnf/alignedcmd"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"strconv"
	"testing"
	"time"
)

func TestPartyDungeonSharedLoadingBarrierFourMembers(t *testing.T) {
	for _, leave := range []bool{false, true} {
		t.Run(strconv.FormatBool(leave), func(t *testing.T) {
			saved := partyDungeonLoadingBarrierDispatch
			partyDungeonLoadingBarrierDispatch = func(run func()) { run() }
			defer func() { partyDungeonLoadingBarrierDispatch = saved }()
			s, a, b, ac, bc, trace := partyDungeonSyncFixture(t)
			g, _ := s.repositoryGroup()
			ctx := context.Background()
			members := []*gameSession{a, b}
			conns := []*partyDungeonSyncConn{ac, bc}
			state := runtimePartyStateForSyncFixture()
			for _, id := range []uint16{101, 102} {
				ch, _, _ := g.Character.Load(ctx, "99")
				ch.CharacterID = strconv.Itoa(int(id))
				ch.Stats = repo.CloneCharacter(ch).Stats
				if err := g.Character.Save(ctx, ch); err != nil {
					t.Fatal(err)
				}
				if err := g.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: ch.CharacterID, Slots: map[string]repo.ItemStack{}}); err != nil {
					t.Fatal(err)
				}
				conn := &partyDungeonSyncConn{charID: id, trace: trace}
				p := &gameSession{conn: conn, channel: a.channel, residentChannel: a.residentChannel, selectedCharacterID: id}
				s.bindGameSessionCharacter(p, id)
				s.onlinePlayers.EnterArea(&onlinePlayerInfo{CharacterID: id, TownID: 38, AreaID: 1, Session: p})
				bindDungeonSelectorOriginForTestAt(t, s, p, 38, 1, 450, 234)
				members = append(members, p)
				conns = append(conns, conn)
				state.Members = append(state.Members, alignedcmd.PartyMemberState{UserID: id, UserState: 1, HPPercent: 100, MPPercent: 100})
			}
			for _, p := range members {
				storeRuntimePartyState(p, state)
			}
			runPartyDungeonSyncEntry(t, s, a)
			cycle := s.currentPartyDungeonLoadingCycle(99)
			if cycle == nil {
				t.Fatal("barrier missing")
			}
			defer func() {
				if cycle.Timer != nil {
					cycle.Timer.Stop()
				}
			}()
			for _, p := range members[:3] {
				if err := s.sendFinishLoadingStatus(p, make([]byte, currentFinishLoadingLegacyRequestBodySize)); err != nil {
					t.Fatal(err)
				}
			}
			for n := 0; n < 4; n++ {
				s.forceReleasePartyDungeonLoadingCycle(99, cycle, "slow_fourth")
			}
			if cycle.Released {
				t.Fatal("three ready bypassed fourth")
			}
			for _, conn := range conns {
				if partyDungeonSyncConnHasPlacement(t, conn) {
					t.Fatal("early scene")
				}
			}
			if leave {
				cancelled, cancel := context.WithCancel(context.Background())
				cancel()
				members[3].ctx = cancelled
				s.forceReleasePartyDungeonLoadingCycle(99, cycle, "fourth_disconnected")
			} else {
				if err := s.sendFinishLoadingStatus(members[3], make([]byte, currentFinishLoadingLegacyRequestBodySize)); err != nil {
					t.Fatal(err)
				}
			}
			deadline := time.Now().Add(time.Second)
			for !cycle.Released && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			if !cycle.Released {
				t.Fatal("ready connected members not released")
			}
			for i, conn := range conns {
				if leave && i == 3 {
					continue
				}
				if !partyDungeonSyncConnHasPlacement(t, conn) {
					t.Fatalf("member %d not released", i)
				}
			}
		})
	}
}
