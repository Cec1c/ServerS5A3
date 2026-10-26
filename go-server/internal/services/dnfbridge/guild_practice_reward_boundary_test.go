package dnfbridge

import (
	"testing"

	"longheng.io/server/internal/modules/dnf/pvf"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

func TestFriendlyPracticeIsNotRecommendedOrGuildMileageClear(t *testing.T) {
	for _, tc := range []struct {
		name, tags string
		ordinary   bool
	}{
		{"ordinary", "", true},
		{"friendly", "[agit war dungeon]\n1\n[friendly agit dungeon]", false},
		{"friendly-nonzero-integer", "[agit war dungeon]\n2\n[friendly agit dungeon]", false},
		{"inactive-agit", "[agit war dungeon]\n0\n[friendly agit dungeon]", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc, err := pvf.Parse("practice.dgn", "[recommended level]\n50 86\n"+tc.tags)
			if err != nil {
				t.Fatal(err)
			}
			runtime := &runtimeDungeonState{Dungeon: worldmap.ParseDungeon(8510, "practice.dgn", doc), entryCharacterLevel: 86, ordinaryFinalRoomClearAccepted: true}
			runtime.Character.Level = 86
			if !currentDungeonMatchesRecommendedLevel(runtime) {
				t.Fatal("fixture is outside suitable level")
			}
			if got := currentDungeonIsRecommendedClear(runtime); got != tc.ordinary {
				t.Fatalf("recommended clear = %v", got)
			}
			if got := currentDungeonEarnsGuildMileage(runtime); got != tc.ordinary {
				t.Fatalf("guild mileage = %v", got)
			}
		})
	}
}
