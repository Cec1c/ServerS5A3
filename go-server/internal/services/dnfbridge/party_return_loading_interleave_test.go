package dnfbridge

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

// Real managed membership and normal op16 entry; only repositories, map data
// and connections are synthetic. The empty start room needs no fabricated kill
// or completion to make the subsequent move legal.
func fourMemberReturnLoadingFixture(t *testing.T) (*Service, []*gameSession) {
	t.Helper()
	s, a, b, _, _, trace := partyDungeonSyncFixture(t)
	source := bridgeDungeonPVF(false)
	source["dungeon/test.dgn"] = strings.Replace(source["dungeon/test.dgn"], "[limit party count]\n1", "[limit party count]\n4", 1)
	source["dungeon/test.dgn"] = strings.Replace(source["dungeon/test.dgn"], "[size]\n1 1", "[size]\n2 1", 1)
	source["dungeon/test.dgn"] = strings.Replace(source["dungeon/test.dgn"], "`map` 0 0 100", "`map` 0 0 100\n`map` 1 0 101", 1)
	source["dungeon/test.dgn"] = strings.Replace(source["dungeon/test.dgn"], "[boss map]\n0 0", "[boss map]\n1 0", 1)
	source[worldmap.DefaultMapList] += "101 `dungeon/test/next.map`\n"
	source["map/dungeon/test/start.map"] = "[map name]\n`start`\n[dungeon]\n700\n[type]\n`[start]`\n[monster]\n"
	source["map/dungeon/test/next.map"] = "[map name]\n`next`\n[dungeon]\n700\n[type]\n`[boss]`\n[monster]\n"
	s.worldMapTable, s.worldMapResolver, s.dungeonMonsterTable = loadBridgeDungeonStaticData(t, source)
	g, _ := s.repositoryGroup()
	ctx := context.Background()
	peers := []*gameSession{a, b}
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
		p := &gameSession{conn: &partyDungeonSyncConn{charID: id, trace: trace}, channel: a.channel, residentChannel: a.residentChannel, selectedCharacterID: id}
		s.bindGameSessionCharacter(p, id)
		s.onlinePlayers.EnterArea(&onlinePlayerInfo{CharacterID: id, TownID: 38, AreaID: 1, Session: p})
		bindDungeonSelectorOriginForTestAt(t, s, p, 38, 1, 450, 234)
		peers = append(peers, p)
		state.Members = append(state.Members, alignedcmd.PartyMemberState{UserID: id, UserState: 1, HPPercent: 100, MPPercent: 100})
	}
	for _, p := range peers {
		storeRuntimePartyState(p, state)
		enableTownMoveSkillProjection(t, s, g, strconv.Itoa(int(p.selectedCharacterID)))
	}
	managed, ok := s.bootstrapManagedRuntimePartyForSession(a)
	if !ok || len(managed.Members) != 4 {
		t.Fatal("four-member authoritative party not established")
	}
	runPartyDungeonSyncEntry(t, s, a)
	s.options.currentClientVersion = "official-24.9"
	for _, p := range peers {
		if p.dungeon.runtime == nil || p.dungeon.runtime.partyInstance != a.dungeon.runtime.partyInstance {
			t.Fatal("normal entry did not admit all four members to one instance")
		}
		p.connID = fmt.Sprintf("return-loading-%d", p.selectedCharacterID)
	}
	return s, peers
}

func TestPersonalReturnBeforeQueuedLoadingPreparationReleasesSurvivors(t *testing.T) {
	for _, leavingIndex := range []int{0, 3} {
		t.Run(map[int]string{0: "leader", 3: "member"}[leavingIndex], func(t *testing.T) {
			s, peers := fourMemberReturnLoadingFixture(t)
			leaving := peers[leavingIndex]
			oldRuntime := leaving.dungeon.runtime
			instance := oldRuntime.partyInstance
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			for _, p := range peers {
				p.ctx = ctx
				if err := s.startGameSessionEvents(ctx, p); err != nil {
					t.Fatal(err)
				}
			}
			var gateOnce sync.Once
			gate, started, gateDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
			openGate := func() { gateOnce.Do(func() { close(gate) }) }
			t.Cleanup(func() {
				openGate()
				for _, p := range peers {
					stopTestGameSessionEvents(t, s, p)
				}
			})
			// Capture only the dispatcher, leaving actual op37 processing and the
			// subsequent preparation/activation callbacks intact.
			previous := partyDungeonLoadingBarrierDispatch
			var dispatch func()
			partyDungeonLoadingBarrierDispatch = func(run func()) { dispatch = run }
			defer func() { partyDungeonLoadingBarrierDispatch = previous }()
			for _, p := range peers {
				if err := s.callGameSession(ctx, p, "return-loading-op37", func() error {
					return s.sendFinishLoadingStatus(p, make([]byte, currentFinishLoadingLegacyRequestBodySize))
				}); err != nil {
					t.Fatal(err)
				}
			}
			partyID := instance.key.PartyID
			cycle := s.currentPartyDungeonLoadingCycle(partyID)
			if cycle == nil || !cycle.Released || dispatch == nil {
				t.Fatal("four actual op37 requests did not reach the shared release")
			}
			go func() {
				_ = s.callGameSession(ctx, leaving, "return-loading-gate", func() error {
					close(started)
					<-gate
					return nil
				})
				close(gateDone)
			}()
			waitTestSignal(t, started, "returning owner did not block")
			returned := make(chan error, 1)
			if err := s.postGameSessionCharacterEvent(leaving, "personal-return-before-loading-prepare", leaving.selectedCharacterID, leaving.characterGeneration, func() error {
				if err := s.handleDungeonGiveupGame(leaving, nil); err != nil {
					returned <- err
					return err
				}
				err := s.commitPendingDungeonReturnForSceneRequest(leaving, "personal_return_loading_interleave")
				returned <- err
				return err
			}); err != nil {
				t.Fatal(err)
			}
			dispatchDone := make(chan struct{})
			go func() { dispatch(); close(dispatchDone) }()
			deadline := time.Now().Add(3 * time.Second)
			for {
				s.partyDungeonLoadingMu.Lock()
				prepared := 0
				for id, p := range cycle.preparing {
					if id != leaving.selectedCharacterID && p.ready {
						prepared++
					}
				}
				activated := cycle.activationScheduled
				s.partyDungeonLoadingMu.Unlock()
				if prepared == 3 {
					if activated {
						t.Fatal("survivors activated before fourth member prepared or left")
					}
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("three owners did not prepare independently")
				}
				time.Sleep(time.Millisecond)
			}
			openGate()
			select {
			case err := <-returned:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("personal return did not finish ahead of old preparation")
			}
			<-gateDone
			<-dispatchDone
			for _, p := range peers {
				deadline := time.Now().Add(3 * time.Second)
				for {
					ready := false
					if err := s.callGameSession(ctx, p, "observe-return-loading-release", func() error {
						if p == leaving {
							ready = p.dungeon.runtime == nil
						} else {
							g := p.finishLoadingGateSnapshot()
							ready = g.State && g.Completion
						}
						return nil
					}); err != nil {
						t.Fatal(err)
					}
					if ready {
						break
					}
					if time.Now().After(deadline) {
						s.partyDungeonLoadingMu.Lock()
						t.Logf("cycle activation=%t plan_count=%d", cycle.activationScheduled, len(cycle.preparing))
						for id, plan := range cycle.preparing {
							t.Logf("plan%d ready=%t tail=%t", id, plan.ready, plan.wantTail)
						}
						s.partyDungeonLoadingMu.Unlock()
						for _, target := range peers {
							state, found := s.runtimePartyManagerForService().SnapshotByUser(target.selectedCharacterID, 0)
							t.Logf("membership%d found=%t snapshot=%+v gates=%+v", target.selectedCharacterID, found, state, target.finishLoadingGateSnapshot())
						}
						t.Log(testPacketLogText(t, s))
						t.Fatalf("member%d still waits for departed preparation", p.selectedCharacterID)
					}
					time.Sleep(time.Millisecond)
				}
			}
			manager := s.runtimePartyManagerForService()
			if _, found := manager.SnapshotByUser(leaving.selectedCharacterID, 0); found {
				t.Fatal("returned member retained authoritative membership")
			}
			s.partyDungeonLoadingMu.Lock()
			retired := cycle.preparing[leaving.selectedCharacterID] == nil
			s.partyDungeonLoadingMu.Unlock()
			if !retired {
				t.Fatal("departed member still owns a preparation plan")
			}
			var leader *gameSession
			for _, p := range peers {
				if p == leaving {
					continue
				}
				state, found := manager.SnapshotByUser(p.selectedCharacterID, 0)
				if !found || len(state.Members) != 3 || p.dungeon.runtime.partyInstance != instance {
					t.Fatal("survivor lost retained three-member party or dungeon instance")
				}
				if state.Leader == p.selectedCharacterID {
					leader = p
				}
			}
			if leader == nil {
				t.Fatal("remaining party has no leader")
			}
			move := make([]byte, dungeoncmd.MoveMapRequestSize)
			move[0] = 1
			if err := s.callGameSession(ctx, leader, "survivor-next-room", func() error { return s.handleDungeonMoveMap(leader, move) }); err != nil {
				t.Fatal(err)
			}
			for _, p := range peers {
				if p == leaving {
					continue
				}
				// Fanout now admits each follow independently. Observe through
				// the recipient queue after its accepted follow, rather than
				// assuming the leader waited for every recipient to execute.
				if err := s.callGameSession(ctx, p, "next-room-owner-commit-observe", func() error {
					scene, ok := p.dungeon.runtime.Session.Scene()
					if !ok || scene.Coordinate.X != 1 {
						return fmt.Errorf("survivor%d did not enter next room", p.selectedCharacterID)
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			}
			dispatch = nil
			for _, p := range peers {
				if p != leaving {
					if err := s.callGameSession(ctx, p, "next-room-op37", func() error {
						return s.sendFinishLoadingStatus(p, make([]byte, currentFinishLoadingLegacyRequestBodySize))
					}); err != nil {
						t.Fatal(err)
					}
				}
			}
			if dispatch == nil || s.currentPartyDungeonLoadingCycle(partyID) == cycle {
				t.Fatal("next room did not establish its own three-member loading cycle")
			}
			dispatch()
			for _, p := range peers {
				if p == leaving {
					continue
				}
				deadline := time.Now().Add(3 * time.Second)
				for {
					ready := false
					if err := s.callGameSession(ctx, p, "next-room-release-observe", func() error {
						g := p.finishLoadingGateSnapshot()
						ready = g.State && g.Completion
						return nil
					}); err != nil {
						t.Fatal(err)
					}
					if ready {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("survivors did not finish loading their next room")
					}
					time.Sleep(time.Millisecond)
				}
			}
			// The previous room's callbacks arrive after the new room activated.
			// They may not write even to a still-bound surviving session.
			for _, p := range peers {
				conn := p.conn.(*partyDungeonSyncConn)
				before := len(conn.frames())
				old := cycle.owners[p.selectedCharacterID]
				s.releasePartyDungeonLoadingMemberPhase(partyID, cycle, p.selectedCharacterID, old, true, "late-old-room-callback", 1)
				if len(conn.frames()) != before {
					t.Fatal("old room callback wrote after next room activation")
				}
			}
		})
	}
}

func TestPreparedDungeonPlacementContractsOnlyToOriginalMembers(t *testing.T) {
	for _, mode := range []string{"unchanged", "departed", "joined", "member_generation", "member_room", "slot", "leader", "departed_then_joined"} {
		t.Run(mode, func(t *testing.T) {
			f := newLeaderExitFixture(t)
			s := f.service
			s.options.currentClientVersion = "official-24.9"
			plan := &partyLoadingPreparation{owner: snapshotPartyDungeonLoadingOwner(f.leader)}
			f.leader.partyLoadingPrepared = plan
			scene, _ := f.leaderRuntime.Session.Scene()
			err := s.sendInitialDungeonActorPlacements(f.leader, f.leaderRuntime, scene, "prepared-placement-guard")
			f.leader.partyLoadingPrepared = nil
			if err != nil || len(plan.activation) != 1 {
				t.Fatalf("positive preparation did not establish activation: callbacks=%d err=%v", len(plan.activation), err)
			}
			f.leaderConn.write.Reset()
			manager := s.runtimePartyManagerForService()
			before, _ := manager.SnapshotByUser(99, 0)
			if strings.HasPrefix(mode, "departed") {
				r := manager.LeaveDungeonExpected(100, f.member.characterGeneration, before.ID)
				if !r.OK {
					t.Fatal(r.Reason)
				}
			}
			switch mode {
			case "joined", "departed_then_joined":
				addRetirementPeer(t, f, 101)
			case "member_generation":
				s.mu.Lock()
				advanceGameSessionCharacterGeneration(f.member)
				s.mu.Unlock()
			case "member_room":
				f.member.dungeonPlacementEpoch.Add(1)
			case "slot":
				if r := manager.Reposition(99, f.leader.characterGeneration, 1, 2); !r.OK {
					t.Fatal(r.Reason)
				}
			case "leader":
				if r := manager.TransferLeader(99, f.leader.characterGeneration, 100, f.member.characterGeneration); !r.OK {
					t.Fatal(r.Reason)
				}
			}
			err = plan.activation[0]()
			allowed := mode == "unchanged" || mode == "departed"
			if (err == nil) != allowed {
				t.Fatalf("activation accepted=%t want%t err=%v", err == nil, allowed, err)
			}
			if !allowed && f.leaderConn.write.Len() != 0 {
				t.Fatal("stale prepared placement wrote before rejecting changed owners")
			}
			if allowed {
				var slots []byte
				for _, frame := range splitUpperFrames(t, f.leaderConn.write.Bytes()) {
					if frame.msgID != 120 || len(frame.body) != 2 {
						t.Fatal("activation emitted a packet other than a slot placement")
					}
					slots = append(slots, frame.body[0])
				}
				want := []byte{0, 1}
				if mode == "departed" {
					want = []byte{0}
				}
				if fmt.Sprint(slots) != fmt.Sprint(want) {
					t.Fatalf("placed slots=%v want%v", slots, want)
				}
			}
		})
	}
}
