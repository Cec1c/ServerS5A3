package dungeon

import (
	"context"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

type luckyStarConcurrentRead struct {
	repo.AccountRepository
	loaded atomic.Int32
	ready  chan struct{}
}

func (r *luckyStarConcurrentRead) Load(ctx context.Context, id string) (repo.AccountRecord, bool, error) {
	a, found, err := r.AccountRepository.Load(ctx, id)
	if err != nil {
		return a, found, err
	}
	if r.loaded.Add(1) == 2 {
		close(r.ready)
	}
	select {
	case <-r.ready:
		return a, found, nil
	case <-ctx.Done():
		return a, found, ctx.Err()
	}
}

func luckyStarAtomicFixture(t *testing.T) repo.Group {
	t.Helper()
	ctx := context.Background()
	g := memory.NewMemoryGroup()
	if err := g.Account.Save(ctx, repo.AccountRecord{AccountID: "stars-account", Metadata: map[string]string{LuckyStarMetadataKey: "10", "unchanged": "keep"}}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"1", "2"} {
		if err := g.Character.Save(ctx, repo.CharacterRecord{CharacterID: id, AccountID: "stars-account", Level: 50}); err != nil {
			t.Fatal(err)
		}
	}
	return g
}

func TestLuckyStarAwardsSerializeAccountUpdates(t *testing.T) {
	g := luckyStarAtomicFixture(t)
	guarded := g
	guarded.Account = &luckyStarConcurrentRead{AccountRepository: g.Account, ready: make(chan struct{})}
	owner, err := NewOwner(guarded)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan error, 2)
	for _, id := range []string{"1", "2"} {
		go func(id string) {
			_, err := owner.AwardLuckyStar(ctx, LuckyStarCommand{AccountID: "stars-account", CharacterID: id, CharacterLevel: 50, RecommendedMin: 45, RecommendedMax: 55})
			done <- err
		}(id)
	}
	for i := 0; i < 2; i++ {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	a, _, err := g.Account.Load(ctx, "stars-account")
	if err != nil {
		t.Fatal(err)
	}
	if a.Metadata[LuckyStarMetadataKey] != "12" || a.Metadata["unchanged"] != "keep" {
		t.Fatalf("two successful awards left metadata=%v, want stars=12", a.Metadata)
	}
}

type luckyStarLateFailure struct{ repo.RentalAssetUnitOfWork }

var errLuckyStarLateFailure = errors.New("injected award transaction failure")

func (u luckyStarLateFailure) WithinRentalAssets(ctx context.Context, account, character string, apply func(repo.AccountRepository, repo.CharacterRepository, repo.InventoryRepository, repo.EquipmentRepository) error) error {
	return u.RentalAssetUnitOfWork.WithinRentalAssets(ctx, account, character, func(a repo.AccountRepository, c repo.CharacterRepository, i repo.InventoryRepository, e repo.EquipmentRepository) error {
		if err := apply(a, c, i, e); err != nil {
			return err
		}
		return errLuckyStarLateFailure
	})
}

func TestLuckyStarAtomicFailureAndOwnership(t *testing.T) {
	for _, kind := range []string{"late_failure", "foreign_owner", "missing_character", "no_transaction"} {
		t.Run(kind, func(t *testing.T) {
			g := luckyStarAtomicFixture(t)
			configured := g
			cmd := LuckyStarCommand{AccountID: "stars-account", CharacterID: "1", CharacterLevel: 50, RecommendedMin: 45, RecommendedMax: 55}
			switch kind {
			case "late_failure":
				configured.RentalAssets = luckyStarLateFailure{g.RentalAssets}
			case "foreign_owner":
				if err := g.Character.Save(context.Background(), repo.CharacterRecord{CharacterID: "1", AccountID: "other", Level: 50}); err != nil {
					t.Fatal(err)
				}
			case "missing_character":
				cmd.CharacterID = "missing"
			case "no_transaction":
				configured.RentalAssets = nil
			}
			before, _, _ := g.Account.Load(context.Background(), cmd.AccountID)
			owner, err := NewOwner(configured)
			if err != nil {
				t.Fatal(err)
			}
			result, err := owner.AwardLuckyStar(context.Background(), cmd)
			if err == nil || result.Awarded {
				t.Fatalf("invalid award succeeded: result=%+v err=%v", result, err)
			}
			if kind == "late_failure" && !errors.Is(err, errLuckyStarLateFailure) {
				t.Fatalf("late fault not reached: %v", err)
			}
			after, _, _ := g.Account.Load(context.Background(), cmd.AccountID)
			if !reflect.DeepEqual(before, after) {
				t.Fatal("failed award changed account")
			}
		})
	}
}
