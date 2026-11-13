package dnfbridge

import (
	"context"
	onlineevent "longheng.io/server/internal/modules/dnf/onlineevent"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	"testing"
)

// Catalog replacement runs before a character is bound as well as in town.
// It must not enter the account/season transaction to decide HUD visibility.
func TestWeeklyEntryR6SurvivesUnboundSelectorAndUnavailableSeason(t *testing.T) {
	s, session, _ := festivalSelectorFixture()
	s.options.currentClientVersion = "official-24.9"
	s.weeklyCatalog = &onlineevent.WeeklyCatalog{}
	controls, err := onlineevent.NewControls(context.Background(), memory.NewMemoryGroup().Settings, []onlineevent.ControlDefinition{{ID: 2208, Name: "weekly", DefaultEnabled: true}})
	if err != nil {
		t.Fatal(err)
	}
	s.activityControls = controls
	calls := 0
	s.repositoryProvider = func() (repo.Group, bool) { calls++; return repo.Group{}, false }
	for _, id := range []uint16{0, 3, 0, 1, 3} {
		session.selectedCharacterID = id
		body, _, err := s.buildCurrentCompleteActivityInfoForDay(session, s.spendTimeCatalogState, false, 0)
		if err != nil {
			t.Fatal(err)
		}
		if !parseCurrentSpendTimeEventInfoBody(t, body).hasEvent(2208) {
			t.Fatalf("weekly entry disappeared for character %d", id)
		}
	}
	if calls != 0 {
		t.Fatalf("HUD visibility called repositories %d times", calls)
	}
	if _, err := s.currentWeeklySeason(session); err == nil {
		t.Fatal("unbound character acquired a reward season")
	}
}
