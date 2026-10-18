package memory

import (
	"context"
	"errors"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"reflect"
	"sync"
	"testing"
	"time"
)

func TestBoosterLedgerMemoryCommitFailureAndCancellationAreAtomic(t *testing.T) {
	for _, mode := range []string{"success", "error", "panic", "cancel", "abort", "scope"} {
		t.Run(mode, func(t *testing.T) {
			g := seedEpicBookAssets(t)
			u := g.AccountAssets.(*memoryAccountCharacterAssetUnitOfWork)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			a0, _, _ := g.Account.Load(ctx, "a")
			s0, _, _ := g.AccountInventory.Load(ctx, "a")
			c0, _, _ := g.Character.Load(ctx, "19")
			i0, _, _ := g.Inventory.Load(ctx, "19")
			if mode == "error" || mode == "panic" || mode == "cancel" {
				u.inventory = &epicBookFailSave{InventoryRepository: g.Inventory, mode: mode, cancel: cancel}
			}
			var err error
			panicked := false
			func() {
				defer func() { panicked = recover() != nil }()
				err = u.WithinAccountCharacterLedgerAssets(ctx, "a", "19", func(a repo.AccountRepository, s repo.AccountInventoryRepository, c repo.CharacterRepository, i repo.InventoryRepository, _ repo.EquipmentRepository) error {
					if mode == "scope" {
						_, _, e := a.Load(ctx, "other")
						return e
					}
					account, _, e := a.Load(ctx, "a")
					if e != nil {
						return e
					}
					account.Metadata[repo.AccountEpicPieceMetadataKey(10300000)] = "1030"
					if e = a.Save(ctx, account); e != nil {
						return e
					}
					if e = s.Save(ctx, repo.AccountInventoryRecord{AccountID: "a", Slots: map[string]repo.ItemStack{}}); e != nil {
						return e
					}
					ch := repo.CloneCharacter(c0)
					ch.Stats = map[string]int64{"gold": 42}
					if e = c.Save(ctx, ch); e != nil {
						return e
					}
					if e = i.Save(ctx, repo.InventoryRecord{CharacterID: "19", Slots: map[string]repo.ItemStack{"0:9": {ItemID: 600, Count: 1}}}); e != nil {
						return e
					}
					if mode == "abort" {
						return errors.New("礼包奖励失败")
					}
					return nil
				})
			}()
			a, _, _ := g.Account.Load(context.Background(), "a")
			s, _, _ := g.AccountInventory.Load(context.Background(), "a")
			c, _, _ := g.Character.Load(context.Background(), "19")
			i, _, _ := g.Inventory.Load(context.Background(), "19")
			if mode == "success" {
				if err != nil || a.Metadata[repo.AccountEpicPieceMetadataKey(10300000)] != "1030" || a.Metadata["other"] != "keep" || len(s.Slots) != 0 || c.Stats["gold"] != 42 || i.Slots["0:9"].Count != 1 {
					t.Fatal("礼包事务提交失败", err)
				}
			} else if (!panicked && err == nil) || !reflect.DeepEqual(a, a0) || !reflect.DeepEqual(s, s0) || !reflect.DeepEqual(c, c0) || !reflect.DeepEqual(i, i0) {
				t.Fatal("礼包失败后资产未完整恢复", err, panicked)
			}
		})
	}
}

func TestBoosterLedgerConcurrentGroundCreditDoesNotLosePieces(t *testing.T) {
	g := seedEpicBookAssets(t)
	ctx := context.Background()
	u := g.AccountAssets.(repo.AccountCharacterLedgerAssetUnitOfWork)
	var wg sync.WaitGroup
	for n := 0; n < 30; n++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			err := u.WithinAccountCharacterLedgerAssets(ctx, "a", "19", func(a repo.AccountRepository, _ repo.AccountInventoryRepository, _ repo.CharacterRepository, _ repo.InventoryRepository, _ repo.EquipmentRepository) error {
				v, _, e := a.Load(ctx, "a")
				if e != nil {
					return e
				}
				k := repo.AccountEpicPieceMetadataKey(10300000)
				v.Metadata[k] = repo.FormatAccountEpicPieceBalance(repo.ParseAccountEpicPieceBalance(v.Metadata[k]) + 30)
				return a.Save(ctx, v)
			})
			if err != nil {
				t.Error(err)
			}
		}()
		go func() {
			defer wg.Done()
			if _, e := repo.AddAccountEpicPieceBalance(ctx, g.Account, "a", 10300000, 1, time.Now()); e != nil {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	a, _, _ := g.Account.Load(ctx, "a")
	if repo.AccountEpicPieceBalances(a)[10300000] != 1930 {
		t.Fatal("并发礼包及地面拾取丢失碎片", a.Metadata)
	}
}
