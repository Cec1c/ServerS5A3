package dnfbridge

import (
	"context"
	"encoding/binary"
	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	r "longheng.io/server/internal/modules/dnf/repository"
	pvf "longheng.io/server/internal/platform/pvf"
	"os"
	"strconv"
	"testing"
	"time"
)

func TestAF10RaidPlacementDoesNotRebuildActorsOrWaitForDungeonLocks(t *testing.T) {
	s, leader, member, lc, mc, _ := prepareApprovedCurrentRaid(t)
	leader.connectionTownActorOwnerChannel = byte(leader.channel.ID)
	member.connectionTownActorOwnerChannel = byte(member.channel.ID)
	lc.write.Reset()
	mc.write.Reset()
	leader.dungeon.mu.Lock()
	member.dungeon.mu.Lock()
	done := make(chan error, 1)
	go func() {
		done <- s.handleOnlineRaidManagerWork(leader, uint16(dnfenum.CmdPacketRaidManagerWork), buildRaidManagerWorkRequestForBridgeTest(0, uint32(member.selectedCharacterID), 2))
	}()
	blocked := false
	select {
	case err := <-done:
		if err != nil {
			t.Error(err)
		}
	case <-time.After(350 * time.Millisecond):
		blocked = true
	}
	member.dungeon.mu.Unlock()
	leader.dungeon.mu.Unlock()
	if blocked {
		if err := <-done; err != nil {
			t.Error(err)
		}
		t.Fatal("placement-only roster waited for cosmetic actor preparation")
	}
	for _, c := range []*bufferConn{lc, mc} {
		rosters := 0
		for _, f := range splitUpperFrames(t, c.write.Bytes()) {
			if f.msgID == 2 || f.msgID == 752 || f.msgID == 598 {
				t.Fatalf("placement replayed unrelated profile/material packet %d", f.msgID)
			}
			if f.msgID == 591 {
				rosters++
			}
		}
		if rosters != 1 {
			t.Fatalf("rosters=%d", rosters)
		}
	}
	state, _ := s.runtimeRaidForMember(member.selectedCharacterID)
	if state.Members[runtimeRaidMemberIndex(state, member.selectedCharacterID)].GroupIndex != 2 {
		t.Fatal("placement lost")
	}
}

func TestAF10RaidJoinPrimesOnlyNewActorForExistingMembers(t *testing.T) {
	f := newCurrentCrossSceneProfileBatchFixture(t)
	s := f.service
	repos, _ := s.repositoryGroup()
	target, found, err := repos.Character.Load(context.Background(), "29")
	if err != nil || !found {
		t.Fatalf("load existing viewer: found=%t err=%v", found, err)
	}
	// The base viewer fixture intentionally has no display name. A newly
	// joining member needs an actual, displayable existing actor here.
	target.Name = "existing-leader"
	if err := repos.Character.Save(context.Background(), target); err != nil {
		t.Fatal(err)
	}
	peers := []*gameSession{f.target, f.actor, f.viewer2}
	state := runtimeRaidState{RaidKey: uint32(s.channelAdvertiseID())<<24 | 23<<16 | uint32(f.target.selectedCharacterID), LeaderID: f.target.selectedCharacterID, CurrentLayout: true}
	for _, p := range peers {
		p.channel = channelcatalog.Channel{ID: 23, Type: dnfenum.RaidChannelType}
		p.loginPacketSeen = true
		p.accountFromToken = true
		p.connectionTownActorOwnerChannel = 23
		p.townActorOwnerChannel = 23
		state.Members = append(state.Members, s.newRuntimeRaidMemberLocked(p.selectedCharacterID, 1, byte(len(state.Members))))
	}
	s.raids = map[uint32]*runtimeRaidState{state.RaidKey: &state}
	f.reset()
	if err := s.broadcastRuntimeRaidJoinRefresh(state, f.viewer2.selectedCharacterID); err != nil {
		t.Fatal(err)
	}
	for i, c := range []*bufferConn{f.targetConn, f.actorConn, f.viewer2Conn} {
		ids := map[uint16]bool{}
		roster := 0
		profiles := 0
		for _, p := range splitUpperFrames(t, c.write.Bytes()) {
			if p.msgID == 2 && len(p.body) > 78 && p.body[0] == 8 {
				ids[binary.LittleEndian.Uint16(p.body[76:78])] = true
			}
			if p.msgID == 591 {
				roster++
			}
			if p.msgID == 752 {
				profiles++
			}
		}
		if roster != 1 || profiles != 1 {
			t.Fatalf("peer%d missing compact cache/roster", i)
		}
		if i < 2 {
			if len(ids) != 1 || !ids[f.viewer2.selectedCharacterID] {
				t.Fatalf("existing peer%d rebuilt unchanged actors: %v", i, ids)
			}
		} else {
			if len(ids) != 2 || !ids[f.target.selectedCharacterID] || !ids[f.actor.selectedCharacterID] {
				t.Fatalf("new member missing existing appearances: %v", ids)
			}
		}
	}
}

func TestAF10RaidProgressSurvivesBusyOwnerBeyondWriteTimeout(t *testing.T) {
	for _, source := range []string{"roster", "dungeon"} {
		t.Run(source, func(t *testing.T) {
			s, session, _, battle := raidLifeRuntimeFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			session.ctx = ctx
			if err := s.startGameSessionEvents(ctx, session); err != nil {
				t.Fatal(err)
			}
			defer stopTestGameSessionEvents(t, s, session)
			state, _ := s.runtimeRaidForMember(session.selectedCharacterID)
			if err := s.callGameSession(ctx, session, "prime-raid-progress", func() error {
				return s.sendCurrentRaidProgressProjection(session, state, true, false)
			}); err != nil {
				t.Fatal(err)
			}
			conn := session.conn.(*bufferConn)
			conn.write.Reset()
			started, release := make(chan struct{}), make(chan struct{})
			done := make(chan error, 1)
			go func() {
				done <- s.callGameSession(ctx, session, "busy-scene-load", func() error {
					close(started)
					<-release
					return nil
				})
			}()
			waitTestSignal(t, started, "owner did not enter loading work")
			s.raidMu.Lock()
			battle.ProgressVersion++
			battle.Participation = map[uint16]currentRaidParticipation{99: {RunID: "accepted-entry", DungeonID: 210}}
			state = cloneRuntimeRaidState(*s.runtimeRaidByMemberLocked(99))
			s.raidMu.Unlock()
			if source == "roster" {
				s.queueCurrentRaidRosterProgress(state)
			} else {
				s.queueCurrentRaidDungeonProgress(state)
			}
			if !af10WaitRaidProgressQueued(session) {
				close(release)
				<-done
				t.Fatal("raid progress was not submitted to the blocked owner")
			}
			// This is intentionally longer than the old queue context. A socket
			// write deadline must not expire work waiting for its session owner.
			time.Sleep(createWriteTimeout + 250*time.Millisecond)
			close(release)
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			if err := s.callGameSession(ctx, session, "drain-raid-progress", func() error { return nil }); err != nil {
				t.Fatal(err)
			}
			seen := false
			for _, f := range splitUpperFrames(t, conn.write.Bytes()) {
				if f.msgID == 584 && len(f.body) >= 9 && f.body[5] == 0 && binary.LittleEndian.Uint16(f.body[7:]) == 99 {
					seen = true
				}
			}
			if !seen {
				t.Fatal("committed participation refresh expired while the owner was busy")
			}
		})
	}
}

func TestAF10QueuedRaidProgressRejectsReplacedOwnersAndPhases(t *testing.T) {
	for _, source := range []string{"roster", "dungeon"} {
		for _, replacement := range []string{"generation", "session", "phase", "account", "channel"} {
			t.Run(source+"/"+replacement, func(t *testing.T) {
				s, session, _, battle := raidLifeRuntimeFixture(t)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				session.ctx = ctx
				if err := s.startGameSessionEvents(ctx, session); err != nil {
					t.Fatal(err)
				}
				defer stopTestGameSessionEvents(t, s, session)
				conn := session.conn.(*bufferConn)
				conn.write.Reset()
				started, release := make(chan struct{}), make(chan struct{})
				done := make(chan error, 1)
				go func() {
					done <- s.callGameSession(ctx, session, "replace-owner-during-load", func() error {
						close(started)
						<-release
						s.raidMu.Lock()
						defer s.raidMu.Unlock()
						switch replacement {
						case "generation":
							advanceGameSessionCharacterGeneration(session)
						case "session":
							s.mu.Lock()
							s.gameSessions[99] = &gameSession{accountID: "account-1", selectedCharacterID: 99, characterGeneration: session.characterGeneration + 1}
							s.mu.Unlock()
						case "phase":
							battle.Phase++
						case "account":
							session.accountID = "foreign-account"
						case "channel":
							session.channel.ID++
						}
						return nil
					})
				}()
				waitTestSignal(t, started, "replacement work did not start")
				state, _ := s.runtimeRaidForMember(99)
				if source == "roster" {
					s.queueCurrentRaidRosterProgress(state)
				} else {
					s.queueCurrentRaidDungeonProgress(state)
				}
				if !af10WaitRaidProgressQueued(session) {
					close(release)
					<-done
					t.Fatal("stale progress was not submitted before replacement")
				}
				close(release)
				if err := <-done; err != nil {
					t.Fatal(err)
				}
				if err := s.callGameSession(ctx, session, "drain-stale-raid-progress", func() error { return nil }); err != nil {
					t.Fatal(err)
				}
				if conn.write.Len() != 0 {
					t.Fatalf("queued progress crossed %s boundary", replacement)
				}
			})
		}
	}
}

func af10WaitRaidProgressQueued(session *gameSession) bool {
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if session.events.loop.QueueLengths().Medium > 0 {
			return true
		}
		time.Sleep(time.Millisecond)
	}
	return false
}

type af10InventoryReadCounter struct {
	r.InventoryRepository
	loads map[string]int
}

func (c *af10InventoryReadCounter) Load(ctx context.Context, id string) (r.InventoryRecord, bool, error) {
	c.loads[id]++
	return c.InventoryRepository.Load(ctx, id)
}

func TestAF10RaidBroadcastReadsAdmissionInventoryOncePerMember(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("real PVF required")
	}
	archive, err := pvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s, leader, member, lc, mc, state := prepareApprovedCurrentRaid(t)
	s.initialEquipmentArchive = archive
	repos, _ := s.repositoryGroup()
	for _, m := range state.Members {
		if err := repos.Inventory.Save(context.Background(), r.InventoryRecord{CharacterID: strconv.Itoa(int(m.CharID)), Slots: map[string]r.ItemStack{"0:121": {ItemID: 10096296, Count: 3}}}); err != nil {
			t.Fatal(err)
		}
	}
	counter := &af10InventoryReadCounter{InventoryRepository: repos.Inventory, loads: map[string]int{}}
	repos.Inventory = counter
	s.repositoryProvider = func() (r.Group, bool) { return repos, true }
	lc.write.Reset()
	mc.write.Reset()
	if err := s.broadcastRuntimeRaidRefresh(state); err != nil {
		t.Fatal(err)
	}
	for _, m := range state.Members {
		if n := counter.loads[strconv.Itoa(int(m.CharID))]; n != 1 {
			t.Fatalf("member %d inventory reads=%d, want one per broadcast", m.CharID, n)
		}
	}
	for _, c := range []*bufferConn{lc, mc} {
		seen := false
		for _, f := range splitUpperFrames(t, c.write.Bytes()) {
			if f.msgID == 598 {
				seen = true
			}
		}
		if !seen {
			t.Fatal("admission status missing")
		}
	}
	if leader.selectedCharacterID == member.selectedCharacterID {
		t.Fatal("invalid fixture")
	}
}
