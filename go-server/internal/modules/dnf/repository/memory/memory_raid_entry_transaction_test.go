package memory

import (
	"context"
	"errors"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"reflect"
	"strconv"
	"testing"
)

type failRaidSettingsOnce struct {
	repo.SettingsRepository
	fail bool
}

func (s *failRaidSettingsOnce) Save(ctx context.Context, r repo.SettingsRecord) error {
	if err := s.SettingsRepository.Save(ctx, r); err != nil {
		return err
	}
	if s.fail {
		s.fail = false
		return errors.New("模拟日次数提交失败")
	}
	return nil
}
func (s *failRaidSettingsOnce) Delete(ctx context.Context, key string) error {
	return s.SettingsRepository.(interface {
		Delete(context.Context, string) error
	}).Delete(ctx, key)
}
func TestRaidEntryAssetsRollbackEveryTicketAndDailyReceipt(t *testing.T) {
	for _, exists := range []bool{false, true} {
		t.Run(strconv.FormatBool(exists), func(t *testing.T) {
			ctx := context.Background()
			r := NewMemoryGroup()
			scope := "raid:fiendwar:daily_entries"
			if exists {
				if err := r.Settings.Save(ctx, repo.SettingsRecord{Scope: scope, Values: map[string]string{"day": "old"}}); err != nil {
					t.Fatal(err)
				}
			}
			before, found, _ := r.Settings.Load(ctx, scope)
			ids := []string{}
			for n := 1; n <= 8; n++ {
				id := strconv.Itoa(n)
				ids = append(ids, id)
				if err := r.Character.Save(ctx, repo.CharacterRecord{CharacterID: id, AccountID: "a" + id}); err != nil {
					t.Fatal(err)
				}
				if err := r.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: id, Slots: map[string]repo.ItemStack{"0:9": {ItemID: 10, Count: 1}}}); err != nil {
					t.Fatal(err)
				}
			}
			u := r.CharacterTrade.(*memoryCharacterTradeUnitOfWork)
			u.settings = &failRaidSettingsOnce{SettingsRepository: r.Settings, fail: true}
			callback := func(c repo.CharacterRepository, i repo.InventoryRepository, s repo.SettingsRepository) error {
				for _, id := range ids {
					bag, _, err := i.Load(ctx, id)
					if err != nil {
						return err
					}
					delete(bag.Slots, "0:9")
					if err := i.Save(ctx, bag); err != nil {
						return err
					}
				}
				if _, _, err := s.Load(ctx, "unrelated"); err == nil {
					t.Fatal("越出事务次数作用域")
				}
				return s.Save(ctx, repo.SettingsRecord{Scope: scope, Values: map[string]string{"day": "today", "account:a1": "1"}})
			}
			if err := u.WithinRaidEntryAssets(ctx, ids, scope, callback); err == nil {
				t.Fatal("未返回提交错误")
			}
			for _, id := range ids {
				bag, _, _ := r.Inventory.Load(ctx, id)
				if bag.Slots["0:9"].Count != 1 {
					t.Fatalf("成员%s门票未恢复", id)
				}
			}
			after, afterFound, _ := r.Settings.Load(ctx, scope)
			if found != afterFound || found && !reflect.DeepEqual(before, after) {
				t.Fatal("失败仍写入每日次数")
			}
			if err := u.WithinRaidEntryAssets(ctx, ids, scope, callback); err != nil {
				t.Fatal(err)
			}
			for _, id := range ids {
				bag, _, _ := r.Inventory.Load(ctx, id)
				if len(bag.Slots) != 0 {
					t.Fatalf("成员%s成功后门票仍在", id)
				}
			}
			after, afterFound, _ = r.Settings.Load(ctx, scope)
			if !afterFound || after.Values["account:a1"] != "1" {
				t.Fatal("成功缺少次数账本")
			}
		})
	}
}
