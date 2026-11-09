package dnfbridge

import (
	"context"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/raid"
	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func syntheticRaidTablePartyFixture(t *testing.T) completionReturnFixture {
	f := newCompletionReturnPartyFixture(t)
	s := f.service
	s.options.currentClientVersion = "official-24.9"
	b := &runtimeRaidBattle{Mode: 1, InstanceID: "synthetic-battle", ChannelID: 23, ChannelType: 23, Phase: 0,
		Deadline: time.Now().Add(time.Hour), Dungeons: []raid.DungeonState{{ID: uint32(f.leaderRuntime.Dungeon.ID), State: 0}},
		MemberAccounts: map[uint16]string{}, Participation: map[uint16]currentRaidParticipation{}}
	state := &runtimeRaidState{RaidKey: 23<<24 | 1<<16 | 99, LeaderID: 99, Started: true, CurrentLayout: true,
		Members: []runtimeRaidMemberState{{CharID: 99, GroupIndex: 1}, {CharID: 100, GroupIndex: 1}}, Battle: b}
	s.raids = map[uint32]*runtimeRaidState{state.RaidKey: state}
	for i, p := range []*gameSession{f.leader, f.member} {
		r := []*runtimeDungeonState{f.leaderRuntime, f.memberRuntime}[i]
		p.channel.ID, p.channel.Type = 23, 23
		p.accountID, p.loginPacketSeen, p.accountFromToken = "account-1", true, true
		r.AccountID = "account-1"
		r.raidEntry = &currentRaidDungeonEntry{RaidKey: state.RaidKey, InstanceID: b.InstanceID, Phase: 0, Attempt: 1,
			ChannelID: 23, ChannelType: 23, RunID: "synthetic-run", Participants: []uint16{99, 100}}
		identity, _ := s.boundGameSessionCharacterSnapshot(p)
		b.MemberAccounts[p.selectedCharacterID] = r.AccountID
		b.Participation[p.selectedCharacterID] = currentRaidParticipation{Session: p, Runtime: r, Generation: identity.generation,
			RunID: r.raidEntry.RunID, DungeonID: uint32(r.Dungeon.ID), Phase: 0, Attempt: 1}
	}
	for i, p := range []*gameSession{f.leader, f.member} {
		if !s.currentRaidTableActiveReturnReady(p, p.dungeon.runtime) {
			t.Fatalf("synthetic active raid owner invalid %d: %v", i, s.validateCurrentRaidDungeonEntry(p, p.dungeon.runtime))
		}
	}
	return f
}

func TestRaidTablePartySyntheticLeaderTransition(t *testing.T) {
	for _, first := range []int{0, 1} {
		t.Run([]string{"leader_first", "member_first"}[first], func(t *testing.T) {
			f := syntheticRaidTablePartyFixture(t)
			if err := raidTableDispatch(f.service, f.leader, []byte{1, 2}); err != nil {
				t.Fatal(err)
			}
			if !f.leaderRuntime.townReturnOp24Sent || !f.memberRuntime.townReturnOp24Sent {
				t.Fatal("whole party transition missing")
			}
			for _, i := range []int{first, 1 - first} {
				p := []*gameSession{f.leader, f.member}[i]
				if err := f.service.commitPendingDungeonReturnForSceneRequest(p, "synthetic-town-scene"); err != nil {
					t.Fatal(err)
				}
				party, found := f.service.runtimePartyManagerForService().SnapshotByUser(p.selectedCharacterID, 0)
				if !found || party.Leader != f.leader.selectedCharacterID || p.dungeon.runtime != nil {
					t.Fatal("party or own runtime not retained/retired correctly")
				}
			}
		})
	}
}

// This synthetic ordinary-party fixture tests only queued ownership and lock
// isolation. The actual raid admission/return path is covered separately below.
func TestRaidTablePartyReturnQueueDoesNotWaitForMember(t *testing.T) {
	f := newCompletionReturnPartyFixture(t)
	s := f.service
	leader, _ := s.boundGameSessionCharacterSnapshot(f.leader)
	member, _ := s.boundGameSessionCharacterSnapshot(f.member)
	owner := &currentRaidTablePartyReturn{leader: leader, instance: f.instance}
	s.raids = map[uint32]*runtimeRaidState{1: {RaidKey: 1, Members: []runtimeRaidMemberState{{CharID: member.character}},
		Battle: &runtimeRaidBattle{Participation: map[uint16]currentRaidParticipation{member.character: {Session: f.member, Runtime: f.memberRuntime, Generation: member.generation}}}}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := s.startGameSessionEvents(ctx, f.member); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.stopGameSessionEvents(context.Background(), f.member) }()
	f.member.dungeon.mu.Lock()
	done := make(chan struct{})
	go func() { s.queueCurrentRaidTablePartyReturn(owner); close(done) }()
	select {
	case <-done:
		f.member.dungeon.mu.Unlock()
	case <-time.After(time.Second):
		f.member.dungeon.mu.Unlock()
		<-done
		t.Fatal("leader queue waited for another player's dungeon lock")
	}
	if err := s.callGameSession(ctx, f.member, "test-drain", func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if f.memberRuntime.townReturnPending {
		t.Fatal("ordinary fixture was treated as an admitted raid")
	}
}

func TestRaidTablePartyReturnRejectsReplacedRuntime(t *testing.T) {
	f := syntheticRaidTablePartyFixture(t)
	s := f.service
	leader, _ := s.boundGameSessionCharacterSnapshot(f.leader)
	member, _ := s.boundGameSessionCharacterSnapshot(f.member)
	entry := *f.memberRuntime.raidEntry
	owner := &currentRaidTablePartyReturn{leader: leader, instance: f.instance, entry: entry}
	f.memberRuntime.raidEntry = &entry
	if !s.currentRaidTablePartyReturnMatches(f.member, f.memberRuntime, owner) {
		t.Fatal("matching identity fixture rejected")
	}
	for _, kind := range []string{"run", "instance", "phase", "leader_generation", "runtime"} {
		t.Run(kind, func(t *testing.T) {
			bad := *owner
			old := f.member.dungeon.runtime
			switch kind {
			case "run":
				bad.entry.RunID = "later"
			case "instance":
				bad.instance = &dungeonPartyInstance{}
			case "phase":
				bad.entry.Phase++
			case "leader_generation":
				bad.leader.generation++
			case "runtime":
				copy := *old
				f.member.dungeon.runtime = &copy
			}
			before := f.memberConn.write.Len()
			if err := s.sendCurrentRaidTablePartyReturnOnOwner(member, old, &bad); err != nil {
				t.Fatal(err)
			}
			if f.memberConn.write.Len() != before || old.townReturnPending {
				t.Fatal("stale return emitted a transition")
			}
			f.member.dungeon.runtime = old
		})
	}
	if err := s.sendCurrentRaidTablePartyReturnOnOwner(member, f.memberRuntime, owner); err != nil {
		t.Fatal(err)
	}
	if !f.memberRuntime.townReturnOp24Sent {
		t.Fatal("valid control did not send the transition")
	}
}

// Actual op16/37 admission followed by the leader's battle-table action.
// Each client must acknowledge its own town scene before its run is retired.
func TestRealPVFRaidTableLeaderReturnsParty(t *testing.T) {
	catalog, _ := actualAntonRewardFixture(t)
	for _, first := range []int{0, 1} {
		t.Run([]string{"leader_confirms_first", "member_confirms_first"}[first], func(t *testing.T) {
			f, peers, conns, runs := raidTableLoaded(t, catalog.source.(*platformpvf.Archive))
			s := f.service
			partyBefore, found := s.runtimePartyManagerForService().SnapshotByUser(peers[0].selectedCharacterID, 0)
			if !found {
				t.Fatal("missing admitted party")
			}
			for n := 0; n < 2; n++ {
				deadlineAuditV10Event(t, s, peers[0], func() error { return raidTableDispatch(s, peers[0], []byte{1, 2}) })
				deadlineAuditV10Event(t, s, peers[1], func() error { return nil })
			}
			for i := range peers {
				peers[i].dungeon.mu.Lock()
				pending := runs[i].townReturnPending && runs[i].townReturnOp24Sent
				active := peers[i].dungeon.runtime == runs[i] && runs[i].Session.Snapshot().Run.Status == worldmap.DungeonRunActive
				peers[i].dungeon.mu.Unlock()
				if !pending || !active {
					t.Fatalf("member %d: pending=%v active=%v", i, pending, active)
				}
				count24 := 0
				for stream := conns[i].bytes(); len(stream) > 0; {
					p, rest := splitGameServerUpperPacket(t, stream)
					stream = rest
					if p.Header.Classification == 0 && p.Header.MsgID == 24 {
						count24++
					}
					if i == 1 && p.Header.Classification == 1 && p.Header.MsgID == 72 {
						t.Fatal("unsolicited member ACK")
					}
				}
				if count24 != 1 {
					t.Fatalf("member %d received %d transitions", i, count24)
				}
			}
			for _, i := range []int{first, 1 - first} {
				deadlineAuditV10Event(t, s, peers[i], func() error { return s.commitPendingDungeonReturnForSceneRequest(peers[i], "test_real_town_scene") })
				if peers[i].dungeon.runtime != nil || runs[i].Session.Snapshot().Run.Status != worldmap.DungeonRunAbandoned {
					t.Fatal("own scene did not retire own run", i)
				}
				partyAfter, found := s.runtimePartyManagerForService().SnapshotByUser(peers[i].selectedCharacterID, 0)
				if !found || partyAfter.ID != partyBefore.ID || partyAfter.Leader != partyBefore.Leader {
					t.Fatal("battle-table return changed party identity", i)
				}
			}
			if len(f.state.Battle.ClearedRuns) != 0 || len(f.state.Battle.MemberClears) != 0 || f.state.Battle.PhaseCompleted {
				t.Fatal("active retreat manufactured clear/reward")
			}
		})
	}
}
