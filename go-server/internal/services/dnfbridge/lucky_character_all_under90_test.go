package dnfbridge

import (
	"context"
	"testing"
)

func TestLuckyAllUnder90PaidRoomBurnStopsAtCapWithoutMarker(t *testing.T) {
	s, runtime, repos := prepareCurrentDungeonMonsterExperienceTest(t)
	ch, _, err := repos.Character.Load(context.Background(), "99")
	if err != nil {
		t.Fatal(err)
	}
	if ch.Stats[currentLuckyNewCharacterStat] != 0 {
		t.Fatal("expected an existing role without a creation marker")
	}
	s.luckyCharacterCatalog = &currentLuckyCatalog{Level1To89Only: true}
	scene, ok := runtime.Session.Scene()
	if !ok {
		t.Fatal("missing room")
	}
	recordCurrentLuckyFatigueRoom(runtime, scene.Coordinate, 100)
	runtime.Character = ch
	for _, marker := range []int64{0, 1} {
		runtime.Character.Stats[currentLuckyNewCharacterStat] = marker
		runtime.Character.Level = 89
		if got := s.currentLuckyRoomBurnPercent(runtime); got != 100 {
			t.Fatalf("Lv89 marker=%d burn=%d", marker, got)
		}
		runtime.Character.Level = 90
		if got := s.currentLuckyRoomBurnPercent(runtime); got != 0 {
			t.Fatalf("Lv90 marker=%d retained burn=%d", marker, got)
		}
		if runtime.luckyFatigueRooms[scene.Coordinate] != 100 {
			t.Fatal("level cap rewrote the committed debit receipt")
		}
	}
	// Resources without the private level policy retain their original behavior.
	s.luckyCharacterCatalog.Level1To89Only = false
	if got := s.currentLuckyRoomBurnPercent(runtime); got != 100 {
		t.Fatalf("legacy unrestricted room changed: %d", got)
	}
}
