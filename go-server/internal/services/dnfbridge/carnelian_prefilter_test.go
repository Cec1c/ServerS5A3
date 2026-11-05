package dnfbridge

import (
	"context"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// The full-repository fixtures do not implement the production activity
// projection. Exercise both routes through OP39, including the narrow query.
func TestCarnelianOrdinaryDeathWithActivityProjection(t *testing.T) {
	for _, tc := range []struct {
		name string
		area int64
		want int64
	}{
		{"matching region", 1, 1685},
		{"different region", 39, 1686},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, narrow := range []bool{false, true} {
				s, p, repos, key := qst017Fixture(t, 2)
				// The dungeon fixture seeds an unrelated active quest. Remove it
				// before replacing the catalog: an unknown ID forces the Full
				// fallback and would hide the production prefilter regression.
				if err := repos.Quest.Save(context.Background(), dnfrepo.QuestRecord{
					CharacterID: "19", States: map[int64]dnfrepo.QuestState{},
				}); err != nil {
					t.Fatal(err)
				}
				s.questCatalog = carnelianBridgeCatalog(t, tc.area)
				carnelianBridgeAccept(t, s, p)
				probe := &deathQuestStatusProbe{deathQuestFullProbe: &deathQuestFullProbe{QuestRepository: repos.Quest}}
				if narrow {
					repos.Quest = probe
					s.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }
				}
				if err := qst017Death(s, p, key); err != nil {
					t.Fatal(err)
				}
				if got := carnelianBridgeState(t, repos, p.selectedCharacterID).ProgressValue; got != tc.want {
					t.Fatalf("narrow=%t remaining=%d want=%d", narrow, got, tc.want)
				}
				if narrow && probe.queries != 1 {
					t.Fatalf("activity projection was not exercised: queries=%d", probe.queries)
				}
				if err := qst017Death(s, p, key); err != nil {
					t.Fatal(err)
				}
				if got := carnelianBridgeState(t, repos, p.selectedCharacterID).ProgressValue; got != tc.want {
					t.Fatalf("replayed death changed progress: narrow=%t remaining=%d", narrow, got)
				}
			}
		})
	}
}
