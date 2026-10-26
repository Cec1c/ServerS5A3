package dnfbridge

import (
	"context"
	"fmt"
	"longheng.io/server/internal/modules/dnf/raid"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
)

func TestCompletedRaidMapOtherSelectorDoesNotWaitForOrdinaryCards(t *testing.T) {
	for _, op := range []uint16{72} {
		for _, fault := range []string{"ready", "phase_completed", "phase_advanced", "missing_clear", "wrong_phase", "wrong_attempt", "wrong_instance", "wrong_channel", "wrong_account", "foreign_participant", "ordinary"} {
			t.Run(fmt.Sprintf("op%d/%s", op, fault), func(t *testing.T) {
				s, r, session, conn := prepareOrdinaryCompletedSettlementRuntimeForExitTest(t)
				completeSettlementExitBarriersForTest(t, s, r, session)
				s.options.currentClientVersion = "official-24.9"
				repos, ok := s.repositoryGroup()
				if !ok {
					t.Fatal("repos missing")
				}
				character, found, err := repos.Character.Load(context.Background(), "99")
				if err != nil || !found {
					t.Fatal(err, found)
				}
				session.accountID = character.AccountID
				session.loginPacketSeen, session.accountFromToken = true, true
				s.bindGameSessionCharacter(session, session.selectedCharacterID)
				session.channel = channelcatalog.Channel{ID: 23, Type: 23}
				s.bindGameSessionCharacter(session, 99)
				r.AccountID = character.AccountID
				entry := &currentRaidDungeonEntry{RaidKey: 23<<16 | 99, InstanceID: "cleared-raid-map", RunID: "owned-run", Phase: 0, Attempt: 1, ChannelID: 23, ChannelType: 23, Participants: []uint16{99}}
				r.raidEntry = entry
				b := &runtimeRaidBattle{InstanceID: entry.InstanceID, Mode: 1, ChannelID: 23, ChannelType: 23, MemberAccounts: map[uint16]string{99: character.AccountID}, ClearedRuns: map[string]currentRaidClearedRun{entry.RunID: {DungeonID: uint32(r.Dungeon.ID), Phase: entry.Phase, Attempt: entry.Attempt, Participants: []uint16{99}}}}
				s.raids = map[uint32]*runtimeRaidState{entry.RaidKey: {RaidKey: entry.RaidKey, LeaderID: 99, Started: true, Battle: b, Members: []runtimeRaidMemberState{{CharID: 99}}}}
				switch fault {
				case "phase_completed":
					b.PhaseCompleted = true
				case "phase_advanced":
					b.Phase = 1
				case "wrong_phase":
					entry.Phase = 1
				case "wrong_attempt":
					entry.Attempt = 2
				case "missing_clear":
					delete(b.ClearedRuns, entry.RunID)
				case "wrong_instance":
					b.InstanceID = "other"
				case "wrong_channel":
					session.channel.ID = 24
				case "wrong_account":
					b.MemberAccounts[99] = "other"
				case "foreign_participant":
					entry.Participants = []uint16{100}
				case "ordinary":
					r.raidEntry = nil
					s.raids = nil
					session.channel.Type = 0
				}
				conn.write.Reset()
				if op == 72 {
					err = s.handleDungeonEplpCommand(session, []byte{1, 1})
				} else {
					err = s.handleDungeonGiveupGame(session, nil)
				}
				if err != nil {
					t.Fatal(err)
				}
				if fault == "ready" || fault == "phase_completed" || fault == "phase_advanced" {
					if session.dungeon.runtime != nil {
						t.Fatalf("raid other selector still waiting for ordinary cards: op24=%v runtime=%p", r.townReturnOp24Sent, session.dungeon.runtime)
					}
				} else if session.dungeon.runtime != r {
					t.Fatal("foreign/uncommitted/ordinary exit bypassed card gate")
				}
				if r.settlementCardLayoutSent || r.settlementCardSelectionSent || r.settlementCardRewardCommitted {
					t.Fatal("raid return fabricated ordinary card state")
				}
			})
		}
	}
}

func TestCompletedRaidMapRetryClosedDungeonRetainsReturnOwner(t *testing.T) {
	s, r, session, conn := prepareOrdinaryCompletedSettlementRuntimeForExitTest(t)
	completeSettlementExitBarriersForTest(t, s, r, session)
	s.options.currentClientVersion = "official-24.9"
	repos, _ := s.repositoryGroup()
	character, _, err := repos.Character.Load(context.Background(), "99")
	if err != nil {
		t.Fatal(err)
	}
	session.accountID = character.AccountID
	session.loginPacketSeen, session.accountFromToken = true, true
	s.bindGameSessionCharacter(session, session.selectedCharacterID)
	session.channel = channelcatalog.Channel{ID: 23, Type: 23}
	s.bindGameSessionCharacter(session, 99)
	r.AccountID = character.AccountID
	e := &currentRaidDungeonEntry{RaidKey: 23<<16 | 99, InstanceID: "closed-raid-map", RunID: "owned-run", Attempt: 1, ChannelID: 23, ChannelType: 23, Participants: []uint16{99}}
	r.raidEntry = e
	b := &runtimeRaidBattle{InstanceID: e.InstanceID, Mode: 1, ChannelID: 23, ChannelType: 23, MemberAccounts: map[uint16]string{99: character.AccountID}, ClearedRuns: map[string]currentRaidClearedRun{e.RunID: {DungeonID: uint32(r.Dungeon.ID), Attempt: 1, Participants: []uint16{99}}}}
	s.raids = map[uint32]*runtimeRaidState{e.RaidKey: {RaidKey: e.RaidKey, LeaderID: 99, Started: true, Battle: b, Members: []runtimeRaidMemberState{{CharID: 99}}}}
	conn.write.Reset()
	if err := s.handleDungeonEplpCommand(session, []byte{1, 0}); err != nil {
		t.Fatal(err)
	}
	if session.dungeon.runtime != r || r.settlementCardExitAckSent || len(conn.write.Bytes()) != 0 {
		t.Fatal("closed raid dungeon retired/ACKed re-entry")
	}
	if err := s.handleDungeonEplpCommand(session, []byte{1, 2}); err != nil {
		t.Fatal(err)
	}
	if session.dungeon.runtime != nil || !r.townReturnOp24Sent {
		t.Fatal("blocked retry consumed completed return owner")
	}
}

func TestCompletedRaidMapPartyRetryRetiresOnlyOwnedCompletedGroupWithoutOrdinaryCards(t *testing.T) {
	for _, fault := range []string{"ready", "peer_incomplete", "peer_foreign_run", "map_closed"} {
		t.Run(fault, func(t *testing.T) {
			s, leader, peer, _, _, _ := partyDungeonSyncFixture(t)
			runPartyDungeonSyncEntry(t, s, leader)
			s.options.currentClientVersion = "official-24.9"
			leader.channel = channelcatalog.Channel{ID: 23, Type: 23}
			peer.channel = leader.channel
			participants := []uint16{leader.selectedCharacterID, peer.selectedCharacterID}
			b := &runtimeRaidBattle{InstanceID: "party-raid-map", Mode: 1, ChannelID: leader.channel.ID, ChannelType: leader.channel.Type, MemberAccounts: map[uint16]string{}, ClearedRuns: map[string]currentRaidClearedRun{}}
			state := &runtimeRaidState{RaidKey: 777, LeaderID: leader.selectedCharacterID, Started: true, Battle: b}
			for _, session := range []*gameSession{leader, peer} {
				completePartyRetryFixture(t, s, session)
				session.accountID = session.dungeon.runtime.AccountID
				session.loginPacketSeen, session.accountFromToken = true, true
				s.bindGameSessionCharacter(session, session.selectedCharacterID)
				r := session.dungeon.runtime
				r.settlementCardLayoutSent, r.settlementCardSelectionSent, r.settlementCardRewardCommitted = false, false, false
				r.settlementPhase = currentDungeonSettlementPhaseResultShown
				r.raidEntry = &currentRaidDungeonEntry{RaidKey: 777, InstanceID: b.InstanceID, RunID: "party-owned-run", Attempt: 1, ChannelID: session.channel.ID, ChannelType: session.channel.Type, Participants: participants}
				b.MemberAccounts[session.selectedCharacterID] = s.accountIDForSession(session)
				r.AccountID = b.MemberAccounts[session.selectedCharacterID]
				state.Members = append(state.Members, runtimeRaidMemberState{CharID: session.selectedCharacterID})
			}
			oldLeader, oldPeer := leader.dungeon.runtime, peer.dungeon.runtime
			b.Deadline = time.Now().Add(time.Hour)
			b.Dungeons = []raid.DungeonState{{ID: uint32(oldLeader.Dungeon.ID), State: 0}}
			b.ClearedRuns["party-owned-run"] = currentRaidClearedRun{DungeonID: uint32(oldLeader.Dungeon.ID), Attempt: 1, Participants: participants}
			s.raids = map[uint32]*runtimeRaidState{777: state}
			if fault == "map_closed" {
				b.Dungeons[0].State = 1
			}
			if fault == "peer_incomplete" {
				oldPeer.settlementStatisticReceived = false
			}
			if fault == "peer_foreign_run" {
				oldPeer.raidEntry.RunID = "foreign"
			}
			retired, err := s.retireCompletedPartyForRetry(leader, oldLeader)
			if fault == "ready" {
				if err != nil || !retired || leader.dungeon.runtime != nil || peer.dungeon.runtime != nil {
					t.Fatal("owned raid group retirement failed", retired, err)
				}
			} else if err == nil || retired || leader.dungeon.runtime != oldLeader || peer.dungeon.runtime != oldPeer {
				t.Fatal("invalid peer detached group", retired, err)
			}
			if oldLeader.settlementCardRewardCommitted || oldPeer.settlementCardRewardCommitted {
				t.Fatal("ordinary reward state manufactured")
			}
		})
	}
}

func TestCompletedRaidMapPartyRejectedNewEntryRestoresAllCompletedReturnOwners(t *testing.T) {
	for _, fault := range []string{"new_entry_refused", "close_during_peer_retire"} {
		t.Run(fault, func(t *testing.T) {
			s, leader, peer, _, _, _ := partyDungeonSyncFixture(t)
			runPartyDungeonSyncEntry(t, s, leader)
			s.options.currentClientVersion = "official-24.9"
			leader.channel = channelcatalog.Channel{ID: 23, Type: 23}
			peer.channel = leader.channel
			participants := []uint16{leader.selectedCharacterID, peer.selectedCharacterID}
			b := &runtimeRaidBattle{InstanceID: "party-raid-map", Mode: 1, ChannelID: leader.channel.ID, ChannelType: leader.channel.Type, MemberAccounts: map[uint16]string{}, ClearedRuns: map[string]currentRaidClearedRun{}}
			state := &runtimeRaidState{RaidKey: 777, LeaderID: leader.selectedCharacterID, Started: true, Battle: b}
			for _, session := range []*gameSession{leader, peer} {
				completePartyRetryFixture(t, s, session)
				session.accountID = session.dungeon.runtime.AccountID
				session.loginPacketSeen, session.accountFromToken = true, true
				s.bindGameSessionCharacter(session, session.selectedCharacterID)
				r := session.dungeon.runtime
				r.settlementCardLayoutSent, r.settlementCardSelectionSent, r.settlementCardRewardCommitted = false, false, false
				r.settlementPhase = currentDungeonSettlementPhaseResultShown
				r.raidEntry = &currentRaidDungeonEntry{RaidKey: 777, InstanceID: b.InstanceID, RunID: "party-owned-run", Attempt: 1, ChannelID: session.channel.ID, ChannelType: session.channel.Type, Participants: participants}
				b.MemberAccounts[session.selectedCharacterID] = s.accountIDForSession(session)
				r.AccountID = b.MemberAccounts[session.selectedCharacterID]
				state.Members = append(state.Members, runtimeRaidMemberState{CharID: session.selectedCharacterID})
			}
			oldLeader, oldPeer := leader.dungeon.runtime, peer.dungeon.runtime
			if fault == "new_entry_refused" {
				oldLeader.Dungeon.ID, oldPeer.Dungeon.ID = 999999, 999999
			}
			b.Deadline = time.Now().Add(time.Hour)
			b.Dungeons = []raid.DungeonState{{ID: uint32(oldLeader.Dungeon.ID), State: 0}}
			b.ClearedRuns["party-owned-run"] = currentRaidClearedRun{DungeonID: uint32(oldLeader.Dungeon.ID), Attempt: 1, Participants: participants}
			s.raids = map[uint32]*runtimeRaidState{777: state}
			if fault == "map_closed" {
				b.Dungeons[0].State = 1
			}
			if fault == "peer_incomplete" {
				oldPeer.settlementStatisticReceived = false
			}
			if fault == "peer_foreign_run" {
				oldPeer.raidEntry.RunID = "foreign"
			}
			closedDuringRetire := false
			// The ordinary entry fixture publishes its shared instance before we
			// supply raid identity. Production freezes raid identity before commit;
			// republish both members under that final key before testing recovery.
			for _, session := range []*gameSession{leader, peer} {
				s.releasePartyDungeonInstance(session.dungeon.runtime, session.selectedCharacterID, "test_freeze_raid_identity")
			}
			for _, session := range []*gameSession{leader, peer} {
				runtime := session.dungeon.runtime
				scene, ok := runtime.Session.Scene()
				if !ok || s.adoptPartyDungeonInstance(session, runtime, scene) == nil {
					t.Fatal("completed raid fixture failed to publish shared instance")
				}
				key, valid := dungeonPartyRunKeyFrom(session, runtime, scene)
				if !valid || runtime.partyInstance.key != key {
					t.Fatal("completed raid fixture has stale shared instance identity")
				}
			}
			if oldLeader.partyInstance != oldPeer.partyInstance {
				t.Fatal("completed raid fixture members do not share an instance")
			}
			originalProvider := s.repositoryProvider
			if fault == "close_during_peer_retire" {
				s.repositoryProvider = func() (dnfrepo.Group, bool) {
					if !closedDuringRetire && peer.dungeon.runtime == nil && leader.dungeon.runtime == oldLeader {
						closedDuringRetire = true
						b.Dungeons[0].State = 1
						b.PhaseCompleted = true
					}
					return originalProvider()
				}
			}
			err := s.retryCurrentCompletedDungeon(leader, oldLeader, "test_raid_party_retry_new_entry_refusal")
			if fault == "close_during_peer_retire" && !closedDuringRetire {
				t.Fatal("mid-peer retirement closure not exercised")
			}
			retired := leader.dungeon.runtime == nil
			if fault == "new_entry_refused" || fault == "close_during_peer_retire" {
				if err != nil || retired || leader.dungeon.runtime != oldLeader || peer.dungeon.runtime != oldPeer || oldLeader.partyInstance == nil || oldLeader.partyInstance != oldPeer.partyInstance {
					t.Fatal("owned raid group retirement failed", retired, err)
				}
			} else if err == nil || retired || leader.dungeon.runtime != oldLeader || peer.dungeon.runtime != oldPeer {
				t.Fatal("invalid peer detached group", retired, err)
			}
			if oldLeader.settlementCardRewardCommitted || oldPeer.settlementCardRewardCommitted {
				t.Fatal("ordinary reward state manufactured")
			}
		})
	}
}
