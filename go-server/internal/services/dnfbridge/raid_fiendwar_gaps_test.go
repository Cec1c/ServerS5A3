package dnfbridge

import (
	"context"
	repo "longheng.io/server/internal/modules/dnf/repository"
	town "longheng.io/server/internal/modules/dnf/town"
	"strconv"
	"testing"
	"time"
)

func TestRealPVFCurrentFiendWarPartyRetreatKeepsOtherEncounter(t *testing.T) {
	f, peers := actualCurrentFiendWar(t, 4)
	s, b := f.service, f.state.Battle
	f.state.Started = true
	b.EnterAfter = time.Now().Add(-time.Minute)
	b.Deadline = time.Now().Add(time.Hour)
	table, err := town.Load(context.Background(), s.initialEquipmentArchive, town.Options{})
	if err != nil {
		t.Fatal(err)
	}
	s.townCatalog = table
	var areas []int
	for id, a := range b.FiendWar.Config.Areas {
		if a.Dungeon > 0 && b.FiendWar.AreaOpen(id) {
			areas = append(areas, id)
		}
	}
	if len(areas) < 2 {
		t.Fatal("真实PVF缺少两处遭遇战")
	}
	runs := make([]*runtimeDungeonState, 4)
	for pair := 0; pair < 2; pair++ {
		area := areas[pair]
		members := f.state.Members[pair*2 : pair*2+2]
		managed, ok := s.replaceManagedRuntimeParty(runtimeRaidGroupPartyState(*f.state, members))
		if !ok {
			t.Fatal("不能建立实际临时队伍")
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
		if err := s.freezeCurrentRaidDungeonGroupEntry(peers[pair*2], runs[pair*2], []*runtimePartyPreparedDungeonEntry{{session: peers[pair*2+1], runtime: runs[pair*2+1]}}); err != nil {
			t.Fatal(err)
		}
	}
	b.Participation = map[uint16]currentRaidParticipation{}
	for i, p := range peers {
		b.Participation[p.selectedCharacterID] = currentRaidParticipation{RunID: runs[i].raidEntry.RunID, Runtime: runs[i], Session: p}
	}
	runs[1].townReturnOp24Sent = true
	if err := s.beginCurrentFiendWarPartyRetreatLocked(peers[1], runs[1], time.Now()); err != nil || len(b.FiendWarRetreatRuns) != 0 {
		t.Fatal("队员退出带退全队", err)
	}
	runs[1].townReturnOp24Sent = false
	runs[0].townReturnOp24Sent = true
	if err := s.beginCurrentFiendWarPartyRetreatLocked(peers[0], runs[0], time.Now()); err != nil {
		t.Fatal(err)
	}
	first, second := runs[0].raidEntry.RunID, runs[2].raidEntry.RunID
	if !b.FiendWarRetreatRuns[first] || b.FiendWarRetreatRuns[second] || !runs[0].fiendWarRetreatKeepsParty {
		t.Fatal("撤退串入其他遭遇战")
	}
	if err := s.maintainCurrentFiendWarSession(peers[1]); err != nil {
		t.Fatal(err)
	}
	if !runs[1].townReturnPending || !runs[1].townReturnOp24Sent || !runs[1].fiendWarRetreatKeepsParty || int(runs[1].townReturnTransition.AreaID) != b.FiendWar.Config.WaitingArea {
		t.Fatal("队员没有被送回本场等待室")
	}
	if runs[2].townReturnPending || runs[3].townReturnPending || b.FiendWarEncounters[areas[1]].RunID != second {
		t.Fatal("误退另一场队伍")
	}
	if err := s.commitPendingDungeonReturnForSceneRequest(peers[1], "测试撤退确认"); err != nil {
		t.Fatal(err)
	}
	party := s.runtimePartyStateForSession(peers[1])
	if party.PartyID != 0 {
		t.Fatal("超时空临时队伍在确认回城后未解除")
	}
	if other := s.runtimePartyStateForSession(peers[2]); len(runtimePartyMembers(other)) != 2 {
		t.Fatal("临时队伍清理误伤另一区域")
	}
}
