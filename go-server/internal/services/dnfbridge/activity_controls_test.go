package dnfbridge

import (
	"context"
	"testing"

	"longheng.io/server/internal/modules/dnf/onlineevent"
	"longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestActivityCatalogOmitsDisabledOnlineRewardsAndPreservesRaidFlags(t *testing.T) {
	for mask := 0; mask < 8; mask++ {
		body, err := buildCurrentActivityEventInfoBody(currentSpendTimeJoustCatalogGoldenRewards, 10800, mask&1 != 0, mask&2 != 0, true, true, mask&4 != 0)
		if err != nil {
			t.Fatal(err)
		}
		shape := parseCurrentSpendTimeEventInfoBody(t, body)
		for id, want := range map[uint16]bool{2347: mask&4 != 0, 2365: mask&1 != 0, 2395: mask&2 != 0, 181: true, 2426: true} {
			if shape.hasEvent(id) != want {
				t.Fatalf("mask%d event%d presence mismatch", mask, id)
			}
		}
	}
}

func TestActivityControlsWithoutPersistedPolicyPreserveIndependentLegacyJoustFlags(t *testing.T) {
	for _, mode := range []string{"on", "off", "auto"} {
		for _, disabled := range []bool{true, false} {
			service := &Service{options: options{joustEventDisabled: disabled, joustEventCatalogMode: mode}, repositoryProvider: func() (repository.Group, bool) { return memory.NewMemoryGroup(), true }}
			catalogBefore := service.currentSpendTimeCatalogIncludesJoustEvent()
			gameplayBefore := service.joustEventEnabled()
			if err := service.initializeActivityControls(context.Background()); err != nil {
				t.Fatal(err)
			}
			if service.currentSpendTimeCatalogIncludesJoustEvent() != catalogBefore || service.joustEventEnabled() != gameplayBefore {
				t.Fatalf("legacy policy changed mode=%s disabled=%v", mode, disabled)
			}
		}
	}
}

func TestActivityControlsDriveAllJoustPathsFromOnePolicyAfterRestart(t *testing.T) {
	ctx := context.Background()
	repos := memory.NewMemoryGroup()
	controls, err := onlineevent.NewControls(ctx, repos.Settings, []onlineevent.ControlDefinition{{ID: 2347, Name: "online", DefaultEnabled: true}, {ID: 2365, Name: "joust", DefaultEnabled: false}, {ID: 2395, Name: "npc"}})
	if err != nil {
		t.Fatal(err)
	}
	service := &Service{activityControls: controls, options: options{joustEventDisabled: true}}
	if service.joustEventEnabled() || service.currentSpendTimeCatalogIncludesJoustEvent() || !service.currentSpendTimeEventEnabled() {
		t.Fatal("initial policy not used")
	}
	if _, err = controls.Save(ctx, 0, []onlineevent.ControlChange{{ID: 2365, Enabled: true}, {ID: 2347, Enabled: false}}); err != nil {
		t.Fatal(err)
	}
	if service.joustEventEnabled() || !service.currentSpendTimeEventEnabled() {
		t.Fatal("save changed active process policy")
	}
	restarted, err := onlineevent.NewControls(ctx, repos.Settings, []onlineevent.ControlDefinition{{ID: 2347, Name: "online", DefaultEnabled: true}, {ID: 2365, Name: "joust"}, {ID: 2395, Name: "npc"}})
	if err != nil {
		t.Fatal(err)
	}
	service.activityControls = restarted
	if !service.joustEventEnabled() || !service.currentSpendTimeCatalogIncludesJoustEvent() || service.currentSpendTimeEventEnabled() {
		t.Fatal("restart did not apply policy consistently")
	}
}
