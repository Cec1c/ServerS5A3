package gmtool

import (
	"context"
	oe "longheng.io/server/internal/modules/dnf/onlineevent"
	"testing"
)

func TestActualEpic350GMReadonlyUsesSameEligibilityPolicy(t *testing.T) {
	source := loadRealPVFSource(t)
	character := realPVFCharacter()
	character.Level = 90
	h := newQuestWriteHarness(t, questWriteFixture{pvf: source, character: character})
	g, _ := h.server.deps.Repos()
	ctx := context.Background()
	for _, enabled := range []bool{true, false} {
		controls, e := oe.NewControls(ctx, g.Settings, []oe.ControlDefinition{{ID: 350, Name: "epic350", DefaultEnabled: enabled}})
		if e != nil {
			t.Fatal(e)
		}
		h.server.deps.Activities = controls
		record, catalog, eligibility, e := h.server.questReadContext(ctx, g, character)
		if e != nil {
			t.Fatal(e)
		}
		ids := acceptableQuestIDs(catalog, eligibility, record)
		_, first := ids[5578]
		_, second := ids[5579]
		if first != enabled || second {
			t.Fatalf("enabled%t first%t second%t", enabled, first, second)
		}
	}
}
