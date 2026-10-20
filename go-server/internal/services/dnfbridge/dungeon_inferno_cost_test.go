package dnfbridge

import (
	"longheng.io/server/internal/modules/dnf/worldmap"
	"testing"
)

func TestInfernoEntryUsesDeclaredCostAndRejectsMissingDefinition(t *testing.T) {
	area := worldmap.Area{HellDungeon: worldmap.OptionalInt{Set: true, Value: 1}, ItemConditions: []int64{3330}}
	dungeon := worldmap.Dungeon{Metadata: worldmap.DungeonMetadata{
		MinimumRequiredLevel: worldmap.OptionalInt{Set: true, Value: 90},
		Flags:                map[string]bool{"inferno dungeon": true},
		IntegerValues:        map[string]int64{"hell dungeon": 1, "hell party required item count": 30},
	}}
	if !currentHellEntryRequiresTickets(&runtimeDungeonState{Dungeon: dungeon}) {
		t.Fatal("inferno entry skipped because no ordinary hell room was selected")
	}
	for _, count := range []int64{30, 0, -1, 100001} {
		dungeon.Metadata.IntegerValues["hell party required item count"] = count
		alternatives, err := currentHellPartyTicketAlternatives(area, dungeon)
		if count == 30 {
			if err != nil || len(alternatives) != 1 || alternatives[0][0].Count != 30 {
				t.Fatalf("count=%d alternatives=%v err=%v", count, alternatives, err)
			}
		} else if err == nil {
			t.Fatalf("invalid inferno cost %d accepted", count)
		}
	}
	if currentHellEntryRequiresTickets(&runtimeDungeonState{}) || currentHellEntryRequiresTickets(nil) {
		t.Fatal("ordinary non-hell entry acquired a ticket cost")
	}
}
