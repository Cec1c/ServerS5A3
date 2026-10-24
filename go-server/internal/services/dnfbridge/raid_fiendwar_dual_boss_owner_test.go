package dnfbridge

import (
	"context"
	"strconv"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/raid/fiendwar"
	repo "longheng.io/server/internal/modules/dnf/repository"
	town "longheng.io/server/internal/modules/dnf/town"
)

func prepareTwoCurrentFiendWarBossRuns(t *testing.T, f *raidStartGuardFixture, peers []*gameSession) (*runtimeRaidBattle, []*runtimeDungeonState) {
	t.Helper()
	b := f.state.Battle
	now := time.Now()
	for _, name := range []string{"top command center state", "mid command center state", "bot command center state"} {
		b.FiendWar.Symbols[b.FiendWar.Config.Symbols[name]] = 0
	}
	if err := b.FiendWar.Apply(fiendwar.FiendWarEvent{}, now); err != nil {
		t.Fatal(err)
	}
	syncCurrentFiendWarDungeons(b)
	if !b.FiendWar.AreaOpen(30) || !b.FiendWar.AreaOpen(31) {
		t.Fatal("two boss areas did not open")
	}
	table, err := town.Load(context.Background(), f.service.initialEquipmentArchive, town.Options{})
	if err != nil {
		t.Fatal(err)
	}
	f.service.townCatalog = table
	runs := make([]*runtimeDungeonState, 4)
	for pair, area := range []int{30, 31} {
		members := f.state.Members[pair*2 : pair*2+2]
		managed, ok := f.service.replaceManagedRuntimeParty(runtimeRaidGroupPartyState(*f.state, members))
		if !ok {
			t.Fatal("cannot create boss party")
		}
		instance := &dungeonPartyInstance{key: dungeonPartyRunKey{PartyID: uint16(managed.PartyID)}}
		for i := pair * 2; i < pair*2+2; i++ {
			p := peers[i]
			_, r := prepareTutorialScopeRuntime(t, tutorialScopeFixtureOptions{disableTutorial: true, singleMonster: true})
			r.Dungeon.ID = int64(b.FiendWar.Config.Areas[area].Dungeon)
			r.AccountID = p.accountID
			r.Character = repo.CharacterRecord{CharacterID: strconv.Itoa(int(p.selectedCharacterID)), AccountID: p.accountID}
			p.dungeon.runtime = r
			runs[i] = r
			b.FiendWar.MemberAreas[p.selectedCharacterID] = area
			setCurrentFiendWarTestOrigin(p, area)
			p.townSceneReadyCharacterID = p.selectedCharacterID
			r.partyInstance = instance
		}
		if err := f.service.freezeCurrentRaidDungeonGroupEntry(peers[pair*2], runs[pair*2], []*runtimePartyPreparedDungeonEntry{{session: peers[pair*2+1], runtime: runs[pair*2+1]}}); err != nil {
			t.Fatal(err)
		}
	}
	b.Participation = map[uint16]currentRaidParticipation{}
	return b, runs
}

func TestCurrentFiendWarUncommittedBossReservationDoesNotRetreatOtherBoss(t *testing.T) {
	f, peers := actualCurrentFiendWar(t, 4)
	f.state.Started = true
	f.state.Battle.EnterAfter = time.Now().Add(-time.Minute)
	f.state.Battle.Deadline = time.Now().Add(time.Hour)
	b, runs := prepareTwoCurrentFiendWarBossRuns(t, f, peers)
	whiteArea, whiteEncounter := b.FiendWar.Areas[31], b.FiendWarEncounters[31]
	whiteDeadline := b.FiendWar.Areas[31].Deadline

	// runs[0] is the dark 4978 reservation; runs[2] is the live pure-white 4953 run.
	peers[0].dungeon.runtime = nil // pre-commit: the runtime never became the session owner
	peers[1].dungeon.runtime = nil // all prepared followers have also been rolled back
	f.service.commitCurrentFiendWarReservation(runs[2])
	f.service.cancelUncommittedCurrentFiendWarEntry(runs[0])

	if b.FiendWar.Areas[31] != whiteArea || !b.FiendWar.Areas[31].Deadline.Equal(whiteDeadline) || b.FiendWarEncounters[31].RunID != whiteEncounter.RunID {
		t.Fatal("uncommitted dark reservation abandoned the live white boss owner")
	}
	if b.FiendWar.Areas[30].Group != 0 || b.FiendWarEncounters[30].RunID != "" {
		t.Fatal("canceled dark reservation was not released")
	}
	if got := b.FiendWar.Value("top boss state"); got != 1 {
		t.Fatalf("canceled dark reservation did not restore its entry state: %d", got)
	}
	if len(b.FiendWarRetreatRuns) != 0 {
		t.Fatal("reservation cancellation emitted live retreat intent")
	}
}

func TestCurrentFiendWarReleasedBossMarksForceRetreatedSide(t *testing.T) {
	f, peers := actualCurrentFiendWar(t, 4)
	f.state.Started = true
	f.state.Battle.EnterAfter = time.Now().Add(-time.Minute)
	f.state.Battle.Deadline = time.Now().Add(time.Hour)
	b, runs := prepareTwoCurrentFiendWarBossRuns(t, f, peers)
	dark, white := runs[0].raidEntry.RunID, runs[2].raidEntry.RunID

	f.service.commitCurrentFiendWarReservation(runs[0])
	f.service.commitCurrentFiendWarReservation(runs[2])
	f.service.releaseCurrentFiendWarEncounterLocked(b, runs[0], peers[0].selectedCharacterID)

	if !b.FiendWarRetreatRuns[white] || b.FiendWarRetreatRuns[dark] {
		t.Fatal("released boss encounter attributed retreat to the wrong run")
	}
	if b.FiendWar.Areas[31].Group != 0 || b.FiendWar.Areas[30].Group != 0 {
		t.Fatal("boss release did not clear both PVF-owned rooms")
	}
	if err := f.service.maintainCurrentFiendWarSession(peers[2]); err != nil {
		t.Fatal(err)
	}
	if !runs[2].fiendWarRetreatKeepsParty || !runs[2].townReturnPending {
		t.Fatal("force-retreated white side took the ordinary leader-exit path")
	}
}
