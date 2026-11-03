package dnfbridge

import (
	"context"
	"encoding/binary"
	"strconv"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	"longheng.io/server/internal/modules/dnf/guild"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func TestDungeonDisconnectKeepsSurvivorPartyAndInstance(t *testing.T) {
	for _, leaderLeaves := range []bool{true, false} {
		name := "member"
		if leaderLeaves {
			name = "leader"
		}
		t.Run(name, func(t *testing.T) {
			f := newLeaderExitFixture(t)
			f.service.options.currentClientVersion = "official-24.9"
			leaving, staying := f.member, f.leader
			if leaderLeaves {
				leaving, staying = f.leader, f.member
			}
			before, _ := f.service.runtimePartyManagerForService().SnapshotByUser(staying.selectedCharacterID, 0)
			runtime := staying.dungeon.runtime
			f.service.unbindGameSession(leaving)
			after, found := f.service.runtimePartyManagerForService().SnapshotByUser(staying.selectedCharacterID, 0)
			if !found || after.ID != before.ID || after.ID != f.instance.key.PartyID || after.Leader != staying.selectedCharacterID {
				t.Fatalf("disconnect replaced the active dungeon party: before=%+v after=%+v", before, after)
			}
			if staying.dungeon.runtime != runtime || runtime.partyInstance != f.instance || runtime.townReturnPending {
				t.Fatal("disconnect disturbed the survivor's dungeon owner")
			}
			for _, peer := range dungeonPartyInstanceMemberSessions(f.instance, 0) {
				if peer.CharacterID == leaving.selectedCharacterID {
					t.Fatal("disconnected member still blocks the shared dungeon instance")
				}
			}
		})
	}
}

func TestThreeMemberDungeonLeaderDisconnectRetainsSharedRun(t *testing.T) {
	var third *gameSession
	s, leader, member, _, memberRuntime := partyDungeonDeathFixture(t, func(s *Service, leader, member *gameSession) {
		repos, _ := s.repositoryGroup()
		c, _, err := repos.Character.Load(context.Background(), "100")
		if err != nil {
			t.Fatal(err)
		}
		c = dnfrepo.CloneCharacter(c)
		c.CharacterID = "101"
		if err := repos.Character.Save(context.Background(), c); err != nil {
			t.Fatal(err)
		}
		third = &gameSession{conn: &bufferConn{}, channel: leader.channel, residentChannel: leader.channel}
		s.bindGameSessionCharacter(third, 101)
		state := runtimePartyStateSnapshot(leader)
		state.Members = append(state.Members, alignedcmd.PartyMemberState{UserID: 101, UserState: 1, HPPercent: 100, MPPercent: 100})
		for _, p := range []*gameSession{leader, member, third} {
			storeRuntimePartyState(p, state)
		}
		if _, ok := s.bootstrapManagedRuntimePartyForSession(leader); !ok {
			t.Fatal("three-member lobby was not registered")
		}
	})
	s.options.currentClientVersion = "official-24.9"
	runtime, _, err := s.prepareDungeonRuntime(context.Background(), third, dungeoncmd.SelectDungeonRequest{DungeonID: 700, Difficulty: 1})
	if err != nil {
		t.Fatal(err)
	}
	third.dungeon.runtime = runtime
	scene, ok := runtime.Session.Scene()
	if !ok {
		t.Fatal("third member has no scene")
	}
	instance := s.adoptPartyDungeonInstance(third, runtime, scene)
	if instance == nil || instance != memberRuntime.partyInstance {
		t.Fatal("fixture did not share one three-member run")
	}
	s.recordDungeonPartyRunKey(third, runtime, scene)
	before, _ := s.runtimePartyManagerForService().SnapshotByUser(99, 0)
	s.unbindGameSession(leader)
	for _, survivor := range []*gameSession{member, third} {
		after, found := s.runtimePartyManagerForService().SnapshotByUser(survivor.selectedCharacterID, 0)
		if !found || after.ID != before.ID || len(after.Members) != 2 || after.Leader != 100 {
			t.Fatalf("three-member disconnect replaced surviving lobby: %+v", after)
		}
		key, valid := s.currentDungeonPartyRunKey(survivor)
		if !valid || key.PartyID != after.ID || survivor.dungeon.runtime.partyInstance != instance {
			t.Fatal("surviving run key no longer matches the central party")
		}
	}
	peers := dungeonPartyInstanceMemberSessions(instance, 0)
	if len(peers) != 2 {
		t.Fatalf("instance retained %d occupants after disconnect; want 2", len(peers))
	}
}

func TestCompletedMemberPersonalExitDoesNotReturnLeader(t *testing.T) {
	for _, opcode := range []uint16{13, 42, 72, 132} {
		t.Run(strconv.Itoa(int(opcode)), func(t *testing.T) {
			f := newCompletionReturnPartyFixture(t)
			f.service.options.currentClientVersion = "official-24.9"
			for _, runtime := range []*runtimeDungeonState{f.leaderRuntime, f.memberRuntime} {
				if err := runtime.Session.CompleteCurrentRoom(); err != nil {
					t.Fatal(err)
				}
				runtime.settlementClearRewardSent = true
			}
			f.leaderConn.write.Reset()
			transition, err := f.service.prepareCurrentDungeonTownTransitionForSession(context.Background(), f.member, f.member.selectedCharacterID)
			if err != nil {
				t.Fatal(err)
			}
			if err := f.service.sendCurrentCompletedDungeonReturnToTown(f.member, f.memberRuntime, transition, opcode, "personal_completed_member_exit"); err != nil {
				t.Fatal(err)
			}
			if f.member.dungeon.runtime != nil {
				t.Fatal("requesting member did not return")
			}
			if f.leader.dungeon.runtime != f.leaderRuntime || f.leaderRuntime.townReturnPending || f.leaderRuntime.partyInstance != f.instance {
				t.Fatal("member's personal completed exit returned the leader")
			}
			for _, packet := range splitUpperFrames(t, f.leaderConn.write.Bytes()) {
				if packet.msgID == currentSceneTransitionMsgID {
					t.Fatal("member's personal exit sent a town transition to the leader")
				}
			}
		})
	}
}

func TestGuildPeerEssenceRefreshWaitsUntilDungeonExit(t *testing.T) {
	s, peers, repos := guildHallFixture(t)
	s.options.guildEssenceEnabled = true
	p := peers[30]
	p.dungeon.runtime = &runtimeDungeonState{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := s.startGameSessionEvents(ctx, p); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		stop, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := s.stopGameSessionEvents(stop, p); err != nil {
			t.Error(err)
		}
	})
	s.queueCurrentGuildEssenceRefresh(1, 29)
	if err := s.callGameSession(ctx, p, "drain-combat-refresh", func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if p.conn.(*bufferConn).write.Len() != 0 {
		t.Fatal("unsolicited guild placement/essence snapshot interrupted the dungeon")
	}
	reg, err := guild.Load(ctx, repos.Settings)
	if err != nil {
		t.Fatal(err)
	}
	reg.Guilds[1].Essence.AP = 57
	if err := guild.Save(ctx, repos.Settings, reg, s.gameplayNow()); err != nil {
		t.Fatal(err)
	}
	if err := s.callGameSession(ctx, p, "town-refresh", func() error {
		p.dungeon.mu.Lock()
		p.dungeon.runtime = nil
		p.dungeon.mu.Unlock()
		// The native town/member-window refresh reads the latest committed
		// balance; deferral must not replay an income transaction.
		s.projectOptionalGuildEssenceSnapshot(p)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	body := guildReply(t, p, 1297)
	if len(body) != 0x895 || binary.LittleEndian.Uint32(body[0x17:]) != 57 {
		t.Fatal("town refresh did not read the latest committed guild balance")
	}
}
