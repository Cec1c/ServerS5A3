package memory

import (
	"context"
	"reflect"
	"sync"
	"testing"

	r "longheng.io/server/internal/modules/dnf/repository"
)

type weddingStoreOnlyCharacter struct{ r.CharacterRepository }

func TestWeddingNamespaceSurvivesOldStatsAndFullSnapshots(t *testing.T) {
	ctx := context.Background()
	g := NewMemoryGroup()
	stale := r.CharacterRecord{CharacterID: "1", AccountID: "a", Stats: map[string]int64{"gold": 100}}
	if err := g.Character.Save(ctx, stale); err != nil {
		t.Fatal(err)
	}
	current := r.CloneCharacter(stale)
	current.Stats["wedding_v1_partner"], current.Stats["wedding_v1_phase"] = 2, 4
	current.Stats["wedding_v1_love_points"], current.Stats["wedding_v1_room_points"] = 55, 80
	if err := r.SaveCharacterFields(ctx, g.Character, current, r.CharacterFieldWedding); err != nil {
		t.Fatal(err)
	}
	protected, _, _ := g.Character.Load(ctx, "1")
	for _, full := range []bool{false, true} {
		for _, carriesOldState := range []bool{false, true} {
			old := r.CloneCharacter(stale)
			old.Stats["gold"] = 42
			if carriesOldState {
				old.Stats["wedding_v1_partner"], old.Stats["wedding_v1_love_points"] = 3, 999
			}
			var err error
			if full {
				err = g.Character.Save(ctx, old)
			} else {
				err = r.SaveCharacterFields(ctx, g.Character, old, r.CharacterFieldStats)
			}
			if err != nil {
				t.Fatal(err)
			}
			got, _, _ := g.Character.Load(ctx, "1")
			for key, value := range protected.Stats {
				if r.IsCharacterWeddingStat(key) && got.Stats[key] != value {
					t.Fatalf("full=%v old=%v lost %s: %+v", full, carriesOldState, key, got.Stats)
				}
			}
			if got.Stats["gold"] != 42 {
				t.Fatal("ordinary stats did not save")
			}
		}
	}
	// An explicit wedding-only save can remove state, and cannot change wallet/base.
	if err := r.SaveCharacterFields(ctx, weddingStoreOnlyCharacter{g.Character}, r.CharacterRecord{CharacterID: "1", Stats: map[string]int64{"gold": 999}}, r.CharacterFieldWedding); err != nil {
		t.Fatal(err)
	}
	got, _, _ := g.Character.Load(ctx, "1")
	if !reflect.DeepEqual(got.Stats, map[string]int64{"gold": 42}) || got.AccountID != "a" {
		t.Fatalf("wedding-only replacement changed ordinary state: %+v", got)
	}
}

func TestWeddingNamespaceConcurrentOrdinarySnapshotsCannotRegressState(t *testing.T) {
	ctx := context.Background()
	g := NewMemoryGroup()
	stale := r.CharacterRecord{CharacterID: "1", AccountID: "a", Stats: map[string]int64{"gold": 100, "wedding_v1_love_points": 999}}
	if err := g.Character.Save(ctx, stale); err != nil {
		t.Fatal(err)
	}
	first, _, _ := g.Character.Load(ctx, "1")
	if _, exists := first.Stats["wedding_v1_love_points"]; exists {
		t.Fatal("ordinary creation wrote wedding namespace")
	}
	var workers sync.WaitGroup
	start := make(chan struct{})
	errors := make(chan error, 21)
	for i := 0; i < 20; i++ {
		workers.Add(1)
		go func() { defer workers.Done(); <-start; errors <- g.Character.Save(ctx, stale) }()
	}
	workers.Add(1)
	go func() {
		defer workers.Done()
		<-start
		updated := r.CharacterRecord{CharacterID: "1", Stats: map[string]int64{"wedding_v1_partner": 2, "wedding_v1_love_points": 7, "wedding_v1_room_points": 99}}
		errors <- r.SaveCharacterFields(ctx, g.Character, updated, r.CharacterFieldWedding)
	}()
	close(start)
	workers.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	got, _, _ := g.Character.Load(ctx, "1")
	if got.Stats["wedding_v1_partner"] != 2 || got.Stats["wedding_v1_love_points"] != 7 || got.Stats["wedding_v1_room_points"] != 99 {
		t.Fatalf("stale snapshots regressed concurrent wedding state: %+v", got.Stats)
	}
}

func TestWeddingSingleOwnerTransactionCommitsExplicitState(t *testing.T) {
	ctx := context.Background()
	g := NewMemoryGroup()
	if err := g.Character.Save(ctx, r.CharacterRecord{CharacterID: "1", AccountID: "a", Stats: map[string]int64{"gold": 100}}); err != nil {
		t.Fatal(err)
	}
	if err := g.CharacterAssets.WithinCharacterAssets(ctx, "1", func(chars r.CharacterRepository, _ r.InventoryRepository, _ r.EquipmentRepository) error {
		c, _, err := chars.Load(ctx, "1")
		if err != nil {
			return err
		}
		c.Stats["wedding_v1_love_points"] = 9
		if err := r.SaveCharacterFields(ctx, chars, c, r.CharacterFieldWedding); err != nil {
			return err
		}
		// A subsequent unrelated save in the same UOW cannot replace that state.
		return chars.Save(ctx, r.CharacterRecord{CharacterID: "1", AccountID: "a", Stats: map[string]int64{"gold": 90, "wedding_v1_love_points": 999}})
	}); err != nil {
		t.Fatal(err)
	}
	got, _, _ := g.Character.Load(ctx, "1")
	if got.Stats["wedding_v1_love_points"] != 9 || got.Stats["gold"] != 90 {
		t.Fatalf("transaction lost explicit wedding changes: %+v", got.Stats)
	}
}
