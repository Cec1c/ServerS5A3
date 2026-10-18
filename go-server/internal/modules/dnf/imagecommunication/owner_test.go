package imagecommunication

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/modules/dnf/town"
)

func ownerFixture(t *testing.T, gold int64) (dnfrepo.Group, *Catalog, Scene, Command) {
	t.Helper()
	r := memory.NewMemoryGroup()
	if err := r.Character.Save(context.Background(), dnfrepo.CharacterRecord{CharacterID: "17", AccountID: "owner", Level: 82, Stats: map[string]int64{"gold": gold, "town_id": 14, "area_id": 1}}); err != nil {
		t.Fatal(err)
	}
	if err := r.Quest.Save(context.Background(), dnfrepo.QuestRecord{CharacterID: "17", States: map[int64]dnfrepo.QuestState{3734: {Status: "active", ProgressValue: 1}, 3733: {Status: "completed"}}}); err != nil {
		t.Fatal(err)
	}
	return r, loadFixture(t), Scene{TownID: 14, Area: town.Area{ID: 1}, SourceNPCs: []int64{78}}, Command{AccountID: "owner", CharacterID: "17", Now: time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC), Binding: Binding{Session: [32]byte{1}, CharacterGeneration: 1, SceneGeneration: 1}}
}

func TestUseConcurrentReplayExpiryAndWallet(t *testing.T) {
	r, c, scene, cmd := ownerFixture(t, 40000)
	before, _, _ := r.Quest.Load(context.Background(), "17")
	var wg sync.WaitGroup
	results := make(chan Result, 20)
	for i := 0; i < 20; i++ {
		wg.Go(func() {
			result, err := Use(context.Background(), r, c, scene, cmd)
			if err != nil {
				t.Error(err)
			}
			results <- result
		})
	}
	wg.Wait()
	close(results)
	charges := 0
	for result := range results {
		if !result.Replayed {
			charges++
		}
		if result.Charge != 10000 || result.Gold != 30000 {
			t.Errorf("result=%+v", result)
		}
	}
	if charges != 1 {
		t.Fatalf("new charges=%d", charges)
	}
	stored, _, _ := r.Character.Load(context.Background(), "17")
	stored.Stats["gold"] += 700
	if err := r.Character.Save(context.Background(), stored); err != nil {
		t.Fatal(err)
	}
	cmd.Now = cmd.Now.Add(PermitLifetime - time.Millisecond)
	result, err := Use(context.Background(), r, c, scene, cmd)
	if err != nil || !result.Replayed || result.Gold != 30000 || result.CurrentGold != 30700 {
		t.Fatalf("before expiry=%+v %v", result, err)
	}
	cmd.Now = cmd.Now.Add(time.Millisecond)
	result, err = Use(context.Background(), r, c, scene, cmd)
	if err != nil || result.Replayed || result.Gold != 20700 {
		t.Fatalf("at expiry=%+v %v", result, err)
	}
	after, _, _ := r.Quest.Load(context.Background(), "17")
	if !reflect.DeepEqual(before, after) {
		t.Fatal("communication changed quest")
	}
}

type faultAssetQuestUOW struct {
	dnfrepo.CharacterAssetUnitOfWork
	failAfterApply  bool
	failAfterCommit bool
}

var errUseInjected = errors.New("injected asset commit failure")

func (f faultAssetQuestUOW) WithinCharacterAssetsAndQuest(ctx context.Context, id string, apply func(dnfrepo.CharacterRepository, dnfrepo.QuestReader) error) error {
	err := f.CharacterAssetUnitOfWork.(dnfrepo.CharacterAssetQuestUnitOfWork).WithinCharacterAssetsAndQuest(ctx, id, func(c dnfrepo.CharacterRepository, q dnfrepo.QuestReader) error {
		if err := apply(c, q); err != nil {
			return err
		}
		if f.failAfterApply {
			return errUseInjected
		}
		return nil
	})
	if err == nil && f.failAfterCommit {
		return errUseInjected
	}
	return err
}

func TestUseRollbackAndUnknownCommitRetry(t *testing.T) {
	for _, committed := range []bool{false, true} {
		t.Run(map[bool]string{false: "rollback", true: "unknown commit"}[committed], func(t *testing.T) {
			r, c, scene, cmd := ownerFixture(t, 20000)
			base := r.CharacterAssets
			r.CharacterAssets = faultAssetQuestUOW{CharacterAssetUnitOfWork: base, failAfterApply: !committed, failAfterCommit: committed}
			if _, err := Use(context.Background(), r, c, scene, cmd); !errors.Is(err, errUseInjected) {
				t.Fatal(err)
			}
			stored, _, _ := r.Character.Load(context.Background(), "17")
			want := int64(20000)
			if committed {
				want = 10000
			}
			if stored.Stats["gold"] != want {
				t.Fatalf("gold=%d want%d", stored.Stats["gold"], want)
			}
			r.CharacterAssets = base
			result, err := Use(context.Background(), r, c, scene, cmd)
			if err != nil || result.Gold != 10000 || result.Replayed != committed {
				t.Fatalf("retry=%+v %v", result, err)
			}
		})
	}
}

func TestUseRefusalsDoNotMutate(t *testing.T) {
	for _, name := range []string{"gold9999", "owner", "missing-character", "town", "area", "level", "area-quest", "no-source", "no-active", "completed", "states-override", "unavailable", "zero-session", "corrupt-receipt"} {
		t.Run(name, func(t *testing.T) {
			r, c, scene, cmd := ownerFixture(t, 20000)
			char, _, _ := r.Character.Load(context.Background(), "17")
			q, _, _ := r.Quest.Load(context.Background(), "17")
			switch name {
			case "gold9999":
				char.Stats["gold"] = 9999
			case "owner":
				cmd.AccountID = "other"
			case "missing-character":
				cmd.CharacterID = "18"
			case "town":
				scene.TownID++
			case "area":
				scene.Area.ID++
			case "level":
				scene.Area.MinLevel = 99
			case "area-quest":
				scene.Area.NeedQuests = []int64{9999}
			case "no-source":
				scene.SourceNPCs = nil
			case "no-active":
				delete(q.States, 3734)
			case "completed":
				q.States[3734] = dnfrepo.QuestState{Status: "completed"}
			case "states-override":
				q.States[3734] = dnfrepo.QuestState{Status: "completed"}
				q.Progress = map[int64]dnfrepo.QuestState{3734: {Status: "active"}}
			case "unavailable":
				r.CharacterAssets = nil
			case "zero-session":
				cmd.Binding.Session = [32]byte{}
			case "corrupt-receipt":
				char.Stats[receiptPrefix+"version"] = 1
			}
			if err := r.Character.Save(context.Background(), char); err != nil {
				t.Fatal(err)
			}
			if err := r.Quest.Save(context.Background(), q); err != nil {
				t.Fatal(err)
			}
			if _, err := Use(context.Background(), r, c, scene, cmd); err == nil {
				t.Fatal("refusal passed")
			}
			after, _, _ := r.Character.Load(context.Background(), "17")
			if !reflect.DeepEqual(char, after) {
				t.Fatal("refusal changed assets")
			}
		})
	}
}

func TestUseBindingAndQuestVersionBoundaries(t *testing.T) {
	for _, name := range []string{"session", "character-generation", "scene-generation", "active-version", "eligibility", "legacy-progress"} {
		t.Run(name, func(t *testing.T) {
			r, c, scene, cmd := ownerFixture(t, 30000)
			if _, err := Use(context.Background(), r, c, scene, cmd); err != nil {
				t.Fatal(err)
			}
			q, _, _ := r.Quest.Load(context.Background(), "17")
			switch name {
			case "session":
				cmd.Binding.Session[0]++
			case "character-generation":
				cmd.Binding.CharacterGeneration++
			case "scene-generation":
				cmd.Binding.SceneGeneration++
			case "active-version":
				state := q.States[3734]
				state.UpdatedAt = cmd.Now
				q.States[3734] = state
			case "eligibility":
				q.States[3741] = dnfrepo.QuestState{Status: "active"}
			case "legacy-progress":
				delete(q.States, 3734)
				q.Progress = map[int64]dnfrepo.QuestState{3741: {Status: "active"}}
			}
			if err := r.Quest.Save(context.Background(), q); err != nil {
				t.Fatal(err)
			}
			result, err := Use(context.Background(), r, c, scene, cmd)
			if err != nil || result.Replayed || result.Gold != 10000 {
				t.Fatalf("new permission=%+v %v", result, err)
			}
		})
	}
}

func TestUseExactFundsAndTaskCompletionRevokesPermit(t *testing.T) {
	r, c, scene, cmd := ownerFixture(t, 10000)
	result, err := Use(context.Background(), r, c, scene, cmd)
	if err != nil || result.Gold != 0 {
		t.Fatalf("exact funds=%+v %v", result, err)
	}
	result, err = Use(context.Background(), r, c, scene, cmd)
	if err != nil || !result.Replayed || result.Gold != 0 {
		t.Fatalf("zero wallet replay=%+v %v", result, err)
	}
	q, _, _ := r.Quest.Load(context.Background(), "17")
	q.States[3734] = dnfrepo.QuestState{Status: "completed"}
	if err := r.Quest.Save(context.Background(), q); err != nil {
		t.Fatal(err)
	}
	if _, err = Use(context.Background(), r, c, scene, cmd); !errors.Is(err, ErrEligibility) {
		t.Fatalf("completed replay=%v", err)
	}
}

func TestUsePreservesQuestOwnerAndTownGateStatusAliases(t *testing.T) {
	for _, statuses := range [][2]string{{"accepted", "done"}, {"in_progress", "cleared"}, {"in-progress", "finished"}, {"progress", "complete"}, {" ACTIVE ", "COMPLETED"}} {
		t.Run(statuses[0]+"/"+statuses[1], func(t *testing.T) {
			r, c, scene, cmd := ownerFixture(t, 10000)
			scene.Area.NeedQuests = []int64{3733}
			q, _, _ := r.Quest.Load(context.Background(), "17")
			q.States[3734] = dnfrepo.QuestState{Status: statuses[0]}
			q.States[3733] = dnfrepo.QuestState{Status: statuses[1]}
			if err := r.Quest.Save(context.Background(), q); err != nil {
				t.Fatal(err)
			}
			result, err := Use(context.Background(), r, c, scene, cmd)
			if err != nil || result.Gold != 0 {
				t.Fatalf("legacy statuses: %+v %v", result, err)
			}
		})
	}
}
