package quest

import (
	"context"
	"errors"
	"reflect"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestGiveUpOrdinaryMaterialResolverKeepsRecoveryBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name, extra string
		material    bool
		resolveErr  error
		want        error
	}{
		{"material", "[type]\n`[seeking]`\n[int data]\n1001 2\n", true, nil, nil},
		{"quest_item", "[type]\n`[seeking]`\n[int data]\n1001 2\n", false, nil, ErrGiveUpNeedsAssets},
		{"missing_metadata", "[type]\n`[seeking]`\n[int data]\n1001 2\n", false, errors.New("unavailable"), ErrGiveUpNeedsAssets},
		{"cant_giveup", "[type]\n`[seeking]`\n[int data]\n1001 2\n[cant giveup]\n", true, nil, ErrQuestCannotGiveUp},
		{"activation_grant", "[type]\n`[seeking]`\n[int data]\n1001 2\n[depend give item]\n1002 1\n", true, nil, ErrGiveUpNeedsAssets},
		{"malformed_pair", "[type]\n`[seeking]`\n[int data]\n1001 2 1002\n", true, nil, ErrGiveUpNeedsAssets},
		{"invalid_pair", "[type]\n`[seeking]`\n[int data]\n1001 2 -1 3\n", true, nil, ErrGiveUpNeedsAssets},
		{"empty_requirements", "[type]\n`[seeking]`\n", true, nil, ErrGiveUpNeedsAssets},
		{"unknown_get_item_shape", "[type]\n`[get item]`\n[int data]\n1001 2\n", true, nil, ErrGiveUpNeedsAssets},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			r := dnfrepomemory.NewMemoryGroup()
			if err := r.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "18", AccountID: "acc-quest"}); err != nil {
				t.Fatal(err)
			}
			before := dnfrepo.QuestRecord{CharacterID: "18", States: map[int64]dnfrepo.QuestState{3145: {Status: "active"}, 3146: {Status: "completed"}}}
			if err := r.Quest.Save(ctx, before); err != nil {
				t.Fatal(err)
			}
			o, err := NewOwner(r)
			if err != nil {
				t.Fatal(err)
			}
			_, err = o.ApplyGiveUpWithMaterialResolver(ctx, testAcceptCatalog(t, tc.extra), NewQuestIDCommand(alignedRequest("acc-quest", 18), "giveup_quest", QuestIDRequest{QuestID: 3145}), func(int64) (bool, error) { return tc.material, tc.resolveErr })
			if !errors.Is(err, tc.want) {
				t.Fatalf("error=%v want=%v", err, tc.want)
			}
			after, _, _ := r.Quest.Load(ctx, "18")
			if tc.want != nil && !reflect.DeepEqual(before, after) {
				t.Fatal("rejected abandon changed quest state")
			}
			if tc.want == nil {
				if _, exists := after.States[3145]; exists {
					t.Fatal("quest not removed")
				}
				if after.States[3146].Status != "completed" {
					t.Fatal("unrelated quest changed")
				}
			}
		})
	}
}
