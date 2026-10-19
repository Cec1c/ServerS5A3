package dnfbridge

import (
	"context"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/fatigue"
	"longheng.io/server/internal/modules/dnf/premium"
)

func TestOrdinaryPartyRetryRefusalRestoresBothCompletedReturnOwners(t *testing.T) {
	s, leader, peer, _, _, _ := partyDungeonSyncFixture(t)
	runPartyDungeonSyncEntry(t, s, leader)
	completePartyRetryFixture(t, s, leader)
	completePartyRetryFixture(t, s, peer)
	for _, session := range []*gameSession{leader, peer} {
		authenticateRound100RetrySession(s, session)
	}
	oldLeader, oldPeer := leader.dungeon.runtime, peer.dungeon.runtime
	repos, _ := s.repositoryProvider()
	character, found, err := repos.Character.Load(context.Background(), "99")
	if err != nil || !found {
		t.Fatal("character missing", err)
	}
	character.Stats["fatigue"] = 0
	character.Stats["fatigue_limit"] = 156
	character.Stats["fatigue_used"] = 156
	character.Stats["fatigue_display_update"] = 156
	character.Stats[fatigue.DayKey] = premium.ServiceDayID(s.gameplayNow())
	if err := repos.Character.Save(context.Background(), character); err != nil {
		t.Fatal(err)
	}
	if err := s.retryCurrentCompletedDungeon(leader, oldLeader, "ordinary_party_refused"); err != nil {
		t.Fatal(err)
	}
	if leader.dungeon.runtime != oldLeader || peer.dungeon.runtime != oldPeer || oldLeader.partyInstance == nil || oldLeader.partyInstance != oldPeer.partyInstance {
		t.Fatal("failed entry did not restore both owners into the same instance")
	}
	for _, session := range []*gameSession{leader, peer} {
		if !s.currentCompletedDungeonActionReady(session, session.dungeon.runtime) {
			t.Fatal("restored owner cannot return", session.selectedCharacterID)
		}
	}
	after, _, _ := repos.Character.Load(context.Background(), "99")
	if !reflect.DeepEqual(character.Stats, after.Stats) {
		t.Fatal("refused retry changed assets")
	}
}

func TestOrdinaryRetryRestoreRejectsChangedCharacterGenerationOrNewRuntime(t *testing.T) {
	for _, change := range []string{"generation", "new_runtime", "unauthenticated", "channel", "account"} {
		t.Run(change, func(t *testing.T) {
			s, old, session, _ := prepareOrdinaryCompletedSettlementRuntime(t)
			old.settlementPlayResultReceived, old.settlementStatisticReceived = true, true
			markCurrentDungeonCardRewardCommittedForTest(old)
			authenticateRound100RetrySession(s, session)
			owners, err := s.captureCurrentRaidRetryReturnOwners(session, old)
			if err != nil || len(owners) != 1 {
				t.Fatal("capture failed", err)
			}
			owners[0].retired = true
			session.dungeon.runtime = nil
			switch change {
			case "generation":
				session.characterGeneration++
			case "new_runtime":
				session.dungeon.runtime = &runtimeDungeonState{}
			case "unauthenticated":
				session.loginPacketSeen = false
			case "channel":
				session.channel.ID++
			case "account":
				session.accountID = "foreign-account"
			}
			before := session.dungeon.runtime
			s.restoreCurrentRaidRetryReturnOwners(session, owners)
			if session.dungeon.runtime != before {
				t.Fatal("stale return owner replaced current binding")
			}
		})
	}
}
