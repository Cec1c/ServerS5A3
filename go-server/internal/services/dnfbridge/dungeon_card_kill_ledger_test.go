package dnfbridge

import (
	"errors"
	"strings"
	"testing"
)

func cardKillLedgerRuntimeForTest(t *testing.T) *runtimeDungeonState {
	t.Helper()
	const (
		mapPath   = "map/dungeon/test/start.map"
		oneActor  = "[monster]\n3001 10 0 100 200 0 0 0 `[fixed]` `[normal]`\n"
		twoActors = "[monster]\n" +
			"3001 10 0 100 200 0 0 0 `[fixed]` `[normal]`\n" +
			"3001 10 0 300 400 0 0 0 `[fixed]` `[boss]`\n"
	)
	source := bridgeDungeonPVF(false)
	before := source[mapPath]
	after := strings.Replace(before, oneActor, twoActors, 1)
	if after == before {
		t.Fatal("fixture start map no longer contains the expected monster row")
	}
	source[mapPath] = after
	runtime := prepareSyntheticDungeonRuntimeForEntryTest(t, source, nil)
	if count, err := runtime.Room.AnnounceAllActors(runtime.Session); err != nil {
		t.Fatalf("announce real fixture actors: %v", err)
	} else if count != 2 {
		t.Fatalf("announced actors=%d want 2", count)
	}
	return runtime
}

func cardKillLedgerMonsterByActorType(t *testing.T, runtime *runtimeDungeonState, actorType byte) runtimeDungeonMonster {
	t.Helper()
	if runtime == nil || runtime.Room == nil {
		t.Fatal("fixture room is missing")
	}
	for _, monster := range runtime.Room.Snapshot().Monsters {
		got, err := currentDungeonMonsterActorType(monster.Spawn)
		if err != nil {
			t.Fatalf("classify fixture monster %d: %v", monster.ObjectKey, err)
		}
		if got == actorType {
			return monster
		}
	}
	t.Fatalf("fixture has no monster with actor type %d", actorType)
	return runtimeDungeonMonster{}
}

func assertCardKillLedgerCountsForTest(t *testing.T, got, want currentDungeonCardKillCounts) {
	t.Helper()
	if got != want {
		t.Fatalf("card gold kill counts=%+v want=%+v", got, want)
	}
}

func TestCurrentDungeonCardKillCountsActorTypeBuckets(t *testing.T) {
	tests := []struct {
		name      string
		actorType byte
		want      currentDungeonCardKillCounts
	}{
		{name: "normal zero", actorType: 0, want: currentDungeonCardKillCounts{Normal: 1}},
		{name: "normal super champion", actorType: 2, want: currentDungeonCardKillCounts{Normal: 1}},
		{name: "normal ai character", actorType: 5, want: currentDungeonCardKillCounts{Normal: 1}},
		{name: "normal ai super champion", actorType: 7, want: currentDungeonCardKillCounts{Normal: 1}},
		{name: "champion monster", actorType: 1, want: currentDungeonCardKillCounts{Champion: 1}},
		{name: "champion ai character", actorType: 6, want: currentDungeonCardKillCounts{Champion: 1}},
		{name: "boss monster", actorType: 3, want: currentDungeonCardKillCounts{Boss: 1}},
		{name: "boss ai character", actorType: 8, want: currentDungeonCardKillCounts{Boss: 1}},
		{name: "special object is ignored", actorType: 9},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			var got currentDungeonCardKillCounts
			got.addActorType(test.actorType)
			assertCardKillLedgerCountsForTest(t, got, test.want)
		})
	}
}

func TestDungeonCardKillLedgerRecordsOnlyAcceptedCombatDeaths(t *testing.T) {
	t.Run("real death counts exactly once and replay does not", func(t *testing.T) {
		runtime := cardKillLedgerRuntimeForTest(t)
		normal := cardKillLedgerMonsterByActorType(t, runtime, 0)
		assertCardKillLedgerCountsForTest(t, runtime.currentCardKillCounts(), currentDungeonCardKillCounts{})

		if _, _, err := runtime.Room.CommitActorDeathReport(normal.ObjectKey, runtime.Session); err != nil {
			t.Fatalf("commit real normal death: %v", err)
		}
		assertCardKillLedgerCountsForTest(t, runtime.currentCardKillCounts(), currentDungeonCardKillCounts{Normal: 1})

		if _, _, err := runtime.Room.CommitActorDeathReport(normal.ObjectKey, runtime.Session); !errors.Is(err, errDungeonMonsterAlreadyDefeated) {
			t.Fatalf("same-object replay error=%v want %v", err, errDungeonMonsterAlreadyDefeated)
		}
		assertCardKillLedgerCountsForTest(t, runtime.currentCardKillCounts(), currentDungeonCardKillCounts{Normal: 1})
	})

	t.Run("boss forced cleanup does not mint a normal kill", func(t *testing.T) {
		runtime := cardKillLedgerRuntimeForTest(t)
		boss := cardKillLedgerMonsterByActorType(t, runtime, 3)
		normal := cardKillLedgerMonsterByActorType(t, runtime, 0)

		if _, _, err := runtime.Room.CommitActorDeathReport(boss.ObjectKey, runtime.Session); err != nil {
			t.Fatalf("commit real boss death: %v", err)
		}
		forced, _, err := runtime.Room.CommitRemainingAnnouncedHostilesAfterBoss(boss.ObjectKey, runtime.Session)
		if err != nil {
			t.Fatalf("force-clear remaining hostiles: %v", err)
		}
		if len(forced) != 1 || forced[0].ObjectKey != normal.ObjectKey {
			t.Fatalf("forced cleanup targets=%+v want only normal object %d", forced, normal.ObjectKey)
		}
		assertCardKillLedgerCountsForTest(t, runtime.currentCardKillCounts(), currentDungeonCardKillCounts{Boss: 1})
	})

	t.Run("scripted retirement does not count", func(t *testing.T) {
		runtime := cardKillLedgerRuntimeForTest(t)
		normal := cardKillLedgerMonsterByActorType(t, runtime, 0)

		if _, _, err := runtime.Room.commitActorDeathReport(normal.ObjectKey, runtime.Session, false); err != nil {
			t.Fatalf("retire scripted normal actor: %v", err)
		}
		stateFound := false
		for _, monster := range runtime.Room.Snapshot().Monsters {
			if monster.ObjectKey != normal.ObjectKey {
				continue
			}
			stateFound = true
			if monster.State != runtimeDungeonMonsterDefeated {
				t.Fatalf("scripted actor state=%s want defeated", monster.State)
			}
		}
		if !stateFound {
			t.Fatalf("scripted actor %d disappeared from the fixture room", normal.ObjectKey)
		}
		assertCardKillLedgerCountsForTest(t, runtime.currentCardKillCounts(), currentDungeonCardKillCounts{})
	})
}

func TestCurrentCardKillCountsDeduplicatesRoomsAcrossPartyState(t *testing.T) {
	service, leader, follower, leaderRuntime, followerRuntime, scene := partyDungeonInstanceFixture(t, 73, 0x7711)
	instance := service.adoptPartyDungeonInstance(leader, leaderRuntime, scene)
	if instance == nil {
		t.Fatal("leader did not create a party dungeon instance")
	}
	if joined := service.adoptPartyDungeonInstance(follower, followerRuntime, scene); joined != instance {
		t.Fatalf("follower instance=%p want=%p", joined, instance)
	}

	shared := leaderRuntime.Room
	shared.mu.Lock()
	shared.cardGoldKills = currentDungeonCardKillCounts{Normal: 1, Champion: 2, Boss: 3}
	shared.mu.Unlock()

	leaderAliasScene := partyDungeonInstanceScene(8, 8, 58569)
	leaderAliasKey := runtimeDungeonRoomKeyFromScene(leaderAliasScene)
	leaderRuntime.Rooms[leaderAliasKey] = &runtimeDungeonRoomVisit{Scene: leaderAliasScene, Room: shared}

	peerOnlyScene := partyDungeonInstanceScene(1, 2, 58570)
	peerOnlyKey := runtimeDungeonRoomKeyFromScene(peerOnlyScene)
	peerOnly := partyDungeonInstanceRoom(peerOnlyScene, 901)
	peerOnly.mu.Lock()
	peerOnly.cardGoldKills = currentDungeonCardKillCounts{Normal: 4, Champion: 5, Boss: 6}
	peerOnly.mu.Unlock()
	followerRuntime.Rooms[peerOnlyKey] = &runtimeDungeonRoomVisit{Scene: peerOnlyScene, Room: peerOnly}

	instance.mu.Lock()
	instance.room = shared
	instance.rooms[leaderAliasKey] = partyDungeonSharedRoom{Room: shared, Scene: leaderAliasScene}
	instance.rooms[peerOnlyKey] = partyDungeonSharedRoom{Room: peerOnly, Scene: peerOnlyScene}
	instance.mu.Unlock()

	want := currentDungeonCardKillCounts{Normal: 5, Champion: 7, Boss: 9}
	assertCardKillLedgerCountsForTest(t, leaderRuntime.currentCardKillCounts(), want)
	if _, leaderSawPeerOnly := leaderRuntime.Rooms[peerOnlyKey]; leaderSawPeerOnly {
		t.Fatal("fixture setup error: leader must not have visited the peer-only room")
	}
}

func TestCardKillLedgerPreservesRanklessTournamentDeath(t *testing.T) {
	t.Setenv(dungeonTournamentEnvKey, dungeonTournamentModeVendor)
	runtime, _ := tournamentStartMapBlockingFixture(t)
	if _, err := runtime.Room.AnnounceAllActors(runtime.Session); err != nil {
		t.Fatal(err)
	}
	monster := runtime.Room.Snapshot().Monsters[0]
	if _, err := currentDungeonMonsterActorType(monster.Spawn); err == nil {
		t.Fatal("projected tournament actor unexpectedly has an ordinary rank")
	}
	if _, _, err := runtime.Room.CommitActorDeathReport(monster.ObjectKey, runtime.Session); err != nil {
		t.Fatalf("valid projected actor death rejected by gold bookkeeping: %v", err)
	}
	if runtime.Room.Snapshot().Monsters[0].State != runtimeDungeonMonsterDefeated {
		t.Fatal("accepted opponent remains alive")
	}
	assertCardKillLedgerCountsForTest(t, runtime.currentCardKillCounts(), currentDungeonCardKillCounts{})
	if _, _, err := runtime.Room.CommitActorDeathReport(monster.ObjectKey, runtime.Session); !errors.Is(err, errDungeonMonsterAlreadyDefeated) {
		t.Fatalf("replayed projected death: %v", err)
	}
}

func TestAdoptPartyDungeonInstanceCarriesEntryPartyCount(t *testing.T) {
	service, leader, follower, leaderRuntime, followerRuntime, scene := partyDungeonInstanceFixture(t, 74, 0x7712)
	leaderRuntime.entryPartyCount = 3
	followerRuntime.entryPartyCount = 4

	instance := service.adoptPartyDungeonInstance(leader, leaderRuntime, scene)
	if instance == nil {
		t.Fatal("leader did not create a party dungeon instance")
	}
	if instance.entryPartyCount != 3 {
		t.Fatalf("instance entry party count=%d want 3", instance.entryPartyCount)
	}
	if joined := service.adoptPartyDungeonInstance(follower, followerRuntime, scene); joined != instance {
		t.Fatalf("follower instance=%p want=%p", joined, instance)
	}
	if got := followerRuntime.entryPartyCount; got != 3 {
		t.Fatalf("follower inherited entry party count=%d want 3", got)
	}
	if got := instance.memberCount(); got != 2 {
		t.Fatalf("current instance member count=%d want 2", got)
	}
}
