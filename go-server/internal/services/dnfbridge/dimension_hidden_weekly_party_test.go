package dnfbridge

import (
	"context"
	"os"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	dnfdungeon "longheng.io/server/internal/modules/dnf/dungeon"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func TestRealPVFDimensionHiddenWeeklyCapIncludesPartyMembers(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("requires current PVF")
	}
	s, repos, clock := newDungeonDailyCountTestService(t, path)
	leader := seedDungeonDailyCountCharacter(t, repos, "19", 19, "hidden-party-leader")
	peer := seedDungeonDailyCountCharacter(t, repos, "20", 20, "hidden-party-peer")
	s.gameSessions = map[uint16]*gameSession{19: leader, 20: peer}
	state := alignedcmd.PartyState{PartyID: 1, UserID: 19, MaxMembers: 4, Members: []alignedcmd.PartyMemberState{{UserID: 19}, {UserID: 20, Slot: 1}}}
	for _, session := range []*gameSession{leader, peer} {
		storeRuntimePartyState(session, state)
		s.bindGameSessionCharacter(session, session.selectedCharacterID)
	}
	ctx := context.Background()
	character, _, _ := repos.Character.Load(ctx, "19")
	member, _, _ := repos.Character.Load(ctx, "20")
	member.Stats["dimension_hidden_reward_week"] = dnfdungeon.DimensionHiddenWeekID(clock.now)
	member.Stats["dimension_hidden_weekly_appearances"] = 2
	if err := repos.Character.Save(ctx, member); err != nil {
		t.Fatal(err)
	}
	d, _ := s.worldMapTable.FindDungeon(7165)
	ordinary := func() (dungeonMazeSelection, error) {
		return selectDungeonMaze(d.Mazes, 0, dnfrepo.QuestRecord{}, s.chooseDungeonIndex)
	}
	s.dungeonChoice = func(int) (int, error) { return 0, nil }
	selected, plan, err := s.selectCurrentDimensionHiddenMaze(leader, d, character, ordinary, false)
	if err != nil || plan == nil || plan.Encountered || d.Mazes[selected.Index].HiddenBoss {
		t.Fatalf("capped peer did not force ordinary maze: selected=%+v plan=%+v err=%v", selected, plan, err)
	}
	member.Stats["dimension_hidden_weekly_appearances"] = 1
	if err := repos.Character.Save(ctx, member); err != nil {
		t.Fatal(err)
	}
	selected, plan, err = s.selectCurrentDimensionHiddenMaze(leader, d, character, ordinary, false)
	if err != nil || plan == nil || !plan.Encountered || !d.Mazes[selected.Index].HiddenBoss {
		t.Fatalf("eligible party denied hidden maze: selected=%+v plan=%+v err=%v", selected, plan, err)
	}
	frozen := func() (dungeonMazeSelection, error) { return selected, nil }
	member.Stats["dimension_hidden_weekly_appearances"] = 2
	if _, _, err := s.selectCurrentDimensionHiddenMaze(peer, d, member, frozen, true); err == nil {
		t.Fatal("capped follower accepted a frozen hidden maze")
	}
	// A normal frozen maze remains admissible after either member's cap.
	normal, _ := ordinary()
	frozen = func() (dungeonMazeSelection, error) { return normal, nil }
	if _, plan, err := s.selectCurrentDimensionHiddenMaze(peer, d, member, frozen, true); err != nil || plan == nil || plan.Encountered {
		t.Fatalf("ordinary frozen maze denied: plan=%+v err=%v", plan, err)
	}
}
