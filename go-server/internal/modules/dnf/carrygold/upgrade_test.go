package carrygold

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

func fixture(t *testing.T, stage, gold int64, level int) dnfrepo.Group {
	t.Helper()
	r := dnfrepomemory.NewMemoryGroup()
	if err := r.Character.Save(context.Background(), dnfrepo.CharacterRecord{CharacterID: "17", AccountID: "a", Level: level, Name: "preserved", Stats: map[string]int64{StageStat: stage, "gold": gold, "fatigue": 92}}); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestUpgradeEachStageAndPreservesCharacter(t *testing.T) {
	r := fixture(t, 0, StageCost*4+7, 60)
	for stage := byte(1); stage <= 4; stage++ {
		result, err := Upgrade(context.Background(), r, "a", "17", stage)
		if err != nil || result.Stage != stage || !result.Known || result.Gold != StageCost*int64(4-stage)+7 {
			t.Fatalf("stage %d: result=%+v err=%v", stage, result, err)
		}
	}
	character, _, _ := r.Character.Load(context.Background(), "17")
	if character.Stats["fatigue"] != 92 || character.Name != "preserved" || character.Level != 60 || character.Stats["gold"] != 7 || character.Stats[StageStat] != 4 {
		t.Fatalf("character=%+v", character)
	}
}

func TestUpgradeRefusalsDoNotMutate(t *testing.T) {
	for _, tc := range []struct {
		name          string
		stage, gold   int64
		level         int
		account, char string
		requested     byte
		want          error
	}{
		{"funds", 0, StageCost - 1, 90, "a", "17", 1, ErrGold},
		{"level", 0, StageCost, 59, "a", "17", 1, ErrLevel},
		{"skip", 0, StageCost, 90, "a", "17", 2, ErrStage},
		{"replay", 1, StageCost, 90, "a", "17", 1, ErrStage},
		{"max", 4, StageCost, 90, "a", "17", 5, ErrStage},
		{"corrupt-high", 256, StageCost, 90, "a", "17", 1, ErrStage},
		{"corrupt-low", -1, StageCost, 90, "a", "17", 1, ErrStage},
		{"other-owner", 0, StageCost, 90, "b", "17", 1, ErrOwnership},
		{"missing-owner", 0, StageCost, 90, "", "17", 1, ErrOwnership},
		{"missing-character", 0, StageCost, 90, "a", "", 1, ErrOwnership},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := fixture(t, tc.stage, tc.gold, tc.level)
			before, _, _ := r.Character.Load(context.Background(), "17")
			_, err := Upgrade(context.Background(), r, tc.account, tc.char, tc.requested)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err=%v want=%v", err, tc.want)
			}
			after, _, _ := r.Character.Load(context.Background(), "17")
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("refusal changed character: %+v", after)
			}
		})
	}
}

type failCommit struct {
	base    dnfrepo.CharacterAssetUnitOfWork
	failure error
}

func (f failCommit) WithinCharacterAssets(ctx context.Context, id string, apply func(dnfrepo.CharacterRepository, dnfrepo.InventoryRepository, dnfrepo.EquipmentRepository) error) error {
	return f.base.WithinCharacterAssets(ctx, id, func(c dnfrepo.CharacterRepository, i dnfrepo.InventoryRepository, e dnfrepo.EquipmentRepository) error {
		if err := apply(c, i, e); err != nil {
			return err
		}
		return f.failure
	})
}

func TestUpgradeRollbackAndUnavailableTransaction(t *testing.T) {
	r := fixture(t, 0, StageCost+8, 90)
	before, _, _ := r.Character.Load(context.Background(), "17")
	want := errors.New("commit aborted")
	r.CharacterAssets = failCommit{r.CharacterAssets, want}
	result, err := Upgrade(context.Background(), r, "a", "17", 1)
	if !errors.Is(err, want) || result.Stage != 0 || result.Gold != StageCost+8 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	r.CharacterAssets = nil
	if _, err := Upgrade(context.Background(), r, "a", "17", 1); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	after, _, _ := r.Character.Load(context.Background(), "17")
	if !reflect.DeepEqual(before, after) {
		t.Fatal("failed transaction changed character")
	}
}

func TestConcurrentDuplicateUpgradeDebitsOnlyOnce(t *testing.T) {
	r := fixture(t, 0, StageCost*10, 90)
	var committed atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := Upgrade(context.Background(), r, "a", "17", 1)
			if err == nil {
				committed.Add(1)
			} else if !errors.Is(err, ErrStage) {
				t.Errorf("err=%v", err)
			}
		}()
	}
	wg.Wait()
	character, _, _ := r.Character.Load(context.Background(), "17")
	if committed.Load() != 1 || character.Stats[StageStat] != 1 || character.Stats["gold"] != StageCost*9 {
		t.Fatalf("commits=%d stats=%v", committed.Load(), character.Stats)
	}
}
