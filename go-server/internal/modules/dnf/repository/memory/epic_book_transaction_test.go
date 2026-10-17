package memory

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	repo "longheng.io/server/internal/modules/dnf/repository"
)

func seedEpicBookAssets(t *testing.T) repo.Group {
	t.Helper()
	g := NewMemoryGroup()
	ctx := context.Background()
	for _, err := range []error{
		g.Account.Save(ctx, repo.AccountRecord{AccountID: "a", Metadata: map[string]string{repo.AccountEpicPieceMetadataKey(10300000): "1000", "other": "keep"}}),
		g.Character.Save(ctx, repo.CharacterRecord{CharacterID: "19", AccountID: "a"}),
		g.Character.Save(ctx, repo.CharacterRecord{CharacterID: "20", AccountID: "a"}),
		g.AccountInventory.Save(ctx, repo.AccountInventoryRecord{AccountID: "a", Slots: map[string]repo.ItemStack{"0:364": {ItemID: 10099775, Count: 44}}}),
		g.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: "19", Slots: map[string]repo.ItemStack{}}),
		g.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: "20", Slots: map[string]repo.ItemStack{}}),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	return g
}

type epicBookFailSave struct {
	repo.InventoryRepository
	mode   string
	cancel context.CancelFunc
	once   sync.Once
}

func (s *epicBookFailSave) Save(ctx context.Context, r repo.InventoryRecord) (err error) {
	if err = s.InventoryRepository.Save(ctx, r); err != nil {
		return err
	}
	s.once.Do(func() {
		switch s.mode {
		case "error":
			err = errors.New("after-write failure")
		case "panic":
			panic("after-write panic")
		case "cancel":
			s.cancel()
		}
	})
	return err
}

func TestEpicBookMemoryCommitAndLateRollbackRound9(t *testing.T) {
	for _, mode := range []string{"success", "error", "panic", "cancel", "callback_abort", "foreign", "scope"} {
		t.Run(mode, func(t *testing.T) {
			g := seedEpicBookAssets(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			u := g.AccountAssets.(*memoryAccountCharacterAssetUnitOfWork)
			beforeA, _, _ := g.Account.Load(context.Background(), "a")
			beforeS, _, _ := g.AccountInventory.Load(context.Background(), "a")
			beforeI, _, _ := g.Inventory.Load(context.Background(), "19")
			if mode == "error" || mode == "panic" || mode == "cancel" {
				u.inventory = &epicBookFailSave{InventoryRepository: g.Inventory, mode: mode, cancel: cancel}
			}
			owner := "a"
			if mode == "foreign" {
				owner = "b"
			}
			var gotErr error
			panicked := false
			func() {
				defer func() { panicked = recover() != nil }()
				gotErr = u.WithinEpicBookAssets(ctx, owner, "19", func(d repo.EpicBookPieceDebitor, ch repo.CharacterRecord, s repo.AccountInventoryRepository, i repo.InventoryRepository) error {
					if mode == "scope" {
						_, _, e := i.Load(ctx, "20")
						return e
					}
					if _, e := d.DebitPiece(ctx, 10300000, 1000, time.Now()); e != nil {
						return e
					}
					if e := s.Save(ctx, repo.AccountInventoryRecord{AccountID: "a", Slots: map[string]repo.ItemStack{}}); e != nil {
						return e
					}
					if e := i.Save(ctx, repo.InventoryRecord{CharacterID: "19", Slots: map[string]repo.ItemStack{"0:9": {ItemID: 450004, Count: 1}}}); e != nil {
						return e
					}
					if mode == "callback_abort" {
						return errors.New("abort")
					}
					return nil
				})
			}()
			a, _, _ := g.Account.Load(context.Background(), "a")
			s, _, _ := g.AccountInventory.Load(context.Background(), "a")
			i, _, _ := g.Inventory.Load(context.Background(), "19")
			if mode == "success" {
				if gotErr != nil || a.Metadata[repo.AccountEpicPieceMetadataKey(10300000)] != "0" || a.Metadata["other"] != "keep" || len(s.Slots) != 0 || i.Slots["0:9"].ItemID != 450004 {
					t.Fatalf("commit failed: %v %+v %+v %+v", gotErr, a, s, i)
				}
			} else if (!panicked && gotErr == nil) || !reflect.DeepEqual(a, beforeA) || !reflect.DeepEqual(s, beforeS) || !reflect.DeepEqual(i, beforeI) {
				t.Fatalf("rollback failed: mode=%s err=%v panic=%v", mode, gotErr, panicked)
			}
		})
	}
}

func TestEpicBookMemoryConcurrentCraftAndCreditRound9(t *testing.T) {
	g := seedEpicBookAssets(t)
	ctx := context.Background()
	u := g.AccountAssets.(repo.EpicBookAssetUnitOfWork)
	entered, release, creditStarted, creditDone := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan error, 1)
	craftDone := make(chan error, 1)
	go func() {
		craftDone <- u.WithinEpicBookAssets(ctx, "a", "19", func(d repo.EpicBookPieceDebitor, _ repo.CharacterRecord, _ repo.AccountInventoryRepository, i repo.InventoryRepository) error {
			if _, e := d.DebitPiece(ctx, 10300000, 1000, time.Now()); e != nil {
				return e
			}
			close(entered)
			<-release
			return i.Save(ctx, repo.InventoryRecord{CharacterID: "19", Slots: map[string]repo.ItemStack{"0:9": {ItemID: 450004, Count: 1}}})
		})
	}()
	<-entered
	go func() {
		close(creditStarted)
		_, e := repo.AddAccountEpicPieceBalance(ctx, g.Account, "a", 10300000, 7, time.Now())
		creditDone <- e
	}()
	<-creditStarted
	select {
	case e := <-creditDone:
		close(release)
		<-craftDone
		t.Fatalf("credit bypassed the crafting critical section: %v", e)
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	if e := <-craftDone; e != nil {
		t.Fatal(e)
	}
	if e := <-creditDone; e != nil {
		t.Fatal(e)
	}
	a, _, _ := g.Account.Load(ctx, "a")
	if a.Metadata[repo.AccountEpicPieceMetadataKey(10300000)] != "7" || a.Metadata["other"] != "keep" {
		t.Fatal("credit lost", a)
	}
	// The remaining 7 pieces must reject both characters without a grant.
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, character := range []string{"19", "20"} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			errs <- u.WithinEpicBookAssets(ctx, "a", id, func(d repo.EpicBookPieceDebitor, _ repo.CharacterRecord, _ repo.AccountInventoryRepository, _ repo.InventoryRepository) error {
				_, e := d.DebitPiece(ctx, 10300000, 1000, time.Now())
				return e
			})
		}(character)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if !errors.Is(e, repo.ErrEpicBookPieceInsufficient) {
			t.Fatal(e)
		}
	}
}
