package dnfbridge

import (
	"context"
	"encoding/binary"
	"os"
	"testing"
	"time"

	dnfdungeon "longheng.io/server/internal/modules/dnf/dungeon"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func TestRealPVFDimensionHiddenSelectionAndCountProjection(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("requires current PVF")
	}
	s, repos, queue := newDungeonDailyCountTestService(t, path)
	s.options.gameUpperHeader = gameUpperHeaderServer16
	session := seedDungeonDailyCountCharacter(t, repos, "19", 19, "dimension-hidden-account")
	s.gameSessions = map[uint16]*gameSession{19: session}
	character, _, _ := repos.Character.Load(context.Background(), "19")
	doc, err := s.currentDimensionInfoDocument()
	if err != nil {
		t.Fatal(err)
	}
	policy, err := parseCurrentDimensionHiddenPolicy(doc)
	if err != nil || policy.WeeklyLimit != 2 || len(policy.AppearanceRates) != 30 || policy.AppearanceRates[0] != 100 || policy.AppearanceRates[12] != 10000 {
		t.Fatalf("policy=%+v err=%v", policy, err)
	}
	for id := int64(7165); id <= 7179; id++ {
		d, ok := s.worldMapTable.FindDungeon(id)
		if !ok {
			t.Fatalf("dungeon %d missing", id)
		}
		ordinary := func() (dungeonMazeSelection, error) {
			return selectDungeonMaze(d.Mazes, 0, dnfrepo.QuestRecord{}, s.chooseDungeonIndex)
		}
		for _, tc := range []struct {
			roll   int
			hidden bool
		}{{0, true}, {99, true}, {100, false}, {9999, false}} {
			s.dungeonChoice = func(limit int) (int, error) {
				if limit == 10000 {
					return tc.roll, nil
				}
				return 0, nil
			}
			choice, plan, err := s.selectCurrentDimensionHiddenMaze(session, d, character, ordinary, false)
			if err != nil || plan == nil || plan.Encountered != tc.hidden || d.Mazes[choice.Index].HiddenBoss != tc.hidden {
				t.Fatalf("id=%d roll=%d choice=%+v plan=%+v err=%v", id, tc.roll, choice, plan, err)
			}
		}
	}
	d, _ := s.worldMapTable.FindDungeon(7165)
	ordinary := func() (dungeonMazeSelection, error) {
		return selectDungeonMaze(d.Mazes, 0, dnfrepo.QuestRecord{}, s.chooseDungeonIndex)
	}
	s.dungeonChoice = func(int) (int, error) { return 0, nil }
	for round := 0; round < 3; round++ {
		choice, plan, err := s.selectCurrentDimensionHiddenMaze(session, d, character, ordinary, false)
		if err != nil || plan == nil || plan.Encountered != (round < 2) || d.Mazes[choice.Index].HiddenBoss != (round < 2) {
			t.Fatalf("hidden plan=%+v err=%v", plan, err)
		}
		runtime := &runtimeDungeonState{Dungeon: d, MazeIndex: choice.Index, dimensionHiddenAdmission: plan}
		character, _, err = s.consumeCurrentDimensionGateDailyEntry(session, runtime, currentDailyCountEntryAssets{})
		if err != nil || runtime.dimensionHiddenHighReward != (round < 2) {
			t.Fatalf("entry %d high=%t err=%v", round, runtime.dimensionHiddenHighReward, err)
		}
		if err := s.sendCurrentDimensionGateEntranceInfo(session, "test_relogin_projection"); err != nil {
			t.Fatal(err)
		}
		conn := session.conn.(*bufferConn)
		packet, rest := splitLongHengGameServerUpperPacket(t, conn.write.Bytes())
		conn.write.Reset()
		weekly := round + 1
		if weekly > 2 {
			weekly = 2
		}
		if packet.Header.MsgID != 812 || packet.Header.Classification != 0 || len(rest) != 0 || len(packet.Body) != 9 || binary.LittleEndian.Uint32(packet.Body) != uint32(round+1) || binary.LittleEndian.Uint32(packet.Body[4:]) != 0 || packet.Body[8] != byte(weekly) {
			t.Fatalf("entry %d projection=%+v", round, packet)
		}
	}
	// Third entry uses the ordinary maze. Thursday restores hidden encounters.
	zone := time.FixedZone("UTC+8", 8*3600)
	queue.now = time.Date(2026, 10, 15, 6, 0, 0, 0, zone)
	count, err := dnfdungeon.DimensionGateEntranceProjection(character, queue.now, 5, 5, 2)
	if err != nil || count != (dnfdungeon.DimensionEntranceCount{}) {
		t.Fatalf("next week count=%+v err=%v", count, err)
	}
	choice, plan, err := s.selectCurrentDimensionHiddenMaze(session, d, character, ordinary, false)
	if err != nil || plan == nil || !plan.Encountered || !d.Mazes[choice.Index].HiddenBoss {
		t.Fatalf("next week selection=%+v plan=%+v err=%v", choice, plan, err)
	}
}
