package raid

import (
	"context"
	"crypto/sha256"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

func rewardOwnerFixture(t *testing.T) (repo.Group, *RewardOwner, RewardCommand, *atomic.Int32) {
	t.Helper()
	g := memory.NewMemoryGroup()
	ctx := context.Background()
	if err := g.Character.Save(ctx, repo.CharacterRecord{CharacterID: "31", AccountID: "account", Stats: map[string]int64{"existing": 7}}); err != nil {
		t.Fatal(err)
	}
	if err := g.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: "31", Slots: map[string]repo.ItemStack{"0:1": {ItemID: 1, Count: 2}}}); err != nil {
		t.Fatal(err)
	}
	owner, err := NewRewardOwner(g)
	if err != nil {
		t.Fatal(err)
	}
	calls := &atomic.Int32{}
	cmd := RewardCommand{AccountID: "account", CharacterID: "31", InstanceID: "00112233445566778899aabbccddeeff", Mode: 1, Phase: 0, Kind: 1,
		MinimumClears: 1, OwnedClears: 1, Fingerprint: sha256.Sum256([]byte("frozen actual reward")), CompletedAt: time.Date(2026, 10, 7, 8, 0, 0, 0, time.UTC),
		Project: func(a *RewardAssets) (RewardChanges, error) {
			calls.Add(1)
			a.Inventory.Slots["0:2"] = repo.ItemStack{ItemID: 2, Count: 3}
			a.AccountInventory.Slots["0:362"] = repo.ItemStack{ItemID: 3, Count: 4}
			return RewardChanges{Inventory: true, AccountInventory: true}, nil
		}}
	return g, owner, cmd, calls
}

func TestRaidRewardOwnerConcurrentOnceAndReceiptSurvivesNewOwner(t *testing.T) {
	g, owner, cmd, calls := rewardOwnerFixture(t)
	var wg sync.WaitGroup
	var first, replay atomic.Int32
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, e := owner.Grant(context.Background(), cmd)
			if e != nil {
				t.Errorf("grant: %v", e)
				return
			}
			if r.Replayed {
				replay.Add(1)
			} else {
				first.Add(1)
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 1 || first.Load() != 1 || replay.Load() != 31 {
		t.Fatalf("calls=%d first=%d replay=%d", calls.Load(), first.Load(), replay.Load())
	}
	owner2, err := NewRewardOwner(g)
	if err != nil {
		t.Fatal(err)
	}
	r, err := owner2.Grant(context.Background(), cmd)
	if err != nil || !r.Replayed || calls.Load() != 1 {
		t.Fatalf("new owner replay=%+v err=%v calls=%d", r, err, calls.Load())
	}
	character, _, _ := g.Character.Load(context.Background(), "31")
	if character.Stats["existing"] != 7 || len(character.Stats) != 7 {
		t.Fatalf("receipt/stats %+v", character.Stats)
	}
	inv, _, _ := g.Inventory.Load(context.Background(), "31")
	account, _, _ := g.AccountInventory.Load(context.Background(), "account")
	if inv.Slots["0:1"].Count != 2 || inv.Slots["0:2"].Count != 3 || account.Slots["0:362"].Count != 4 {
		t.Fatal("inventory/account grant missing or consumed existing item")
	}
}

func TestRaidRewardOwnerRejectsDifferentOutcomeOrCardAndAllowsDifferentKindPhase(t *testing.T) {
	_, owner, cmd, calls := rewardOwnerFixture(t)
	if _, e := owner.Grant(nil, cmd); e != nil {
		t.Fatal(e)
	}
	changed := cmd
	changed.Card = 1
	if _, e := owner.Grant(nil, changed); !errors.Is(e, ErrRewardReceipt) {
		t.Fatalf("second card: %v", e)
	}
	changed = cmd
	changed.Fingerprint = sha256.Sum256([]byte("rerolled"))
	if _, e := owner.Grant(nil, changed); !errors.Is(e, ErrRewardReceipt) {
		t.Fatalf("second outcome: %v", e)
	}
	changed = cmd
	changed.Kind = 3
	if r, e := owner.Grant(nil, changed); e != nil || r.Replayed {
		t.Fatalf("different kind=%+v %v", r, e)
	}
	changed = cmd
	changed.Phase = 1
	if r, e := owner.Grant(nil, changed); e != nil || r.Replayed {
		t.Fatalf("different phase=%+v %v", r, e)
	}
	if calls.Load() != 3 {
		t.Fatalf("calls=%d", calls.Load())
	}
}

func TestRaidRewardOwnerRollbackAndRetry(t *testing.T) {
	g, owner, cmd, calls := rewardOwnerFixture(t)
	project := cmd.Project
	want := errors.New("no free slot for second reward")
	cmd.Project = func(a *RewardAssets) (RewardChanges, error) {
		_, _ = project(a)
		return RewardChanges{Inventory: true, AccountInventory: true}, want
	}
	if _, e := owner.Grant(nil, cmd); !errors.Is(e, want) {
		t.Fatal(e)
	}
	inv, _, _ := g.Inventory.Load(context.Background(), "31")
	account, found, _ := g.AccountInventory.Load(context.Background(), "account")
	character, _, _ := g.Character.Load(context.Background(), "31")
	if len(inv.Slots) != 1 || found && len(account.Slots) != 0 || len(character.Stats) != 1 {
		t.Fatal("failure leaked inventory, account or receipt")
	}
	cmd.Project = project
	if r, e := owner.Grant(nil, cmd); e != nil || r.Replayed {
		t.Fatalf("retry=%+v %v", r, e)
	}
	if calls.Load() != 2 {
		t.Fatalf("retry calls=%d", calls.Load())
	}
}

func TestRaidRewardOwnerInvalidAuthorityNeverProjects(t *testing.T) {
	cases := []struct {
		name   string
		change func(*RewardCommand)
		want   error
	}{
		{"cross account", func(c *RewardCommand) { c.AccountID = "other" }, ErrRewardOwnership},
		{"missing character", func(c *RewardCommand) { c.CharacterID = "99" }, ErrRewardOwnership},
		{"no owned clear", func(c *RewardCommand) { c.OwnedClears = 0 }, ErrRewardIneligible},
		{"no clear policy", func(c *RewardCommand) { c.MinimumClears = 0 }, ErrRewardIneligible},
		{"invalid instance", func(c *RewardCommand) { c.InstanceID = "0" }, ErrRewardReceipt},
		{"unsupported mode", func(c *RewardCommand) { c.Mode = 3 }, ErrRewardReceipt},
		{"Luke final zero policy", func(c *RewardCommand) { c.Mode, c.Phase, c.MinimumClears = 2, 1, 0 }, ErrRewardIneligible},
		{"invalid phase", func(c *RewardCommand) { c.Phase = 2 }, ErrRewardReceipt},
		{"invalid kind", func(c *RewardCommand) { c.Kind = 0 }, ErrRewardReceipt},
		{"invalid card", func(c *RewardCommand) { c.Card = 20 }, ErrRewardReceipt},
		{"missing fingerprint", func(c *RewardCommand) { c.Fingerprint = [32]byte{} }, ErrRewardReceipt},
		{"missing projector", func(c *RewardCommand) { c.Project = nil }, ErrRewardReceipt},
		{"missing date", func(c *RewardCommand) { c.CompletedAt = time.Time{} }, ErrRewardReceipt},
		{"noncanonical account", func(c *RewardCommand) { c.AccountID = " account" }, ErrRewardReceipt},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			g, o, c, calls := rewardOwnerFixture(t)
			test.change(&c)
			if _, e := o.Grant(nil, c); !errors.Is(e, test.want) {
				t.Fatalf("got=%v want=%v", e, test.want)
			}
			if calls.Load() != 0 {
				t.Fatal("unauthorized projector")
			}
			inv, _, _ := g.Inventory.Load(context.Background(), "31")
			if len(inv.Slots) != 1 {
				t.Fatal("invalid mutation")
			}
		})
	}
}

func TestRaidRewardOwnerRejectsPartialReceipt(t *testing.T) {
	g, o, c, calls := rewardOwnerFixture(t)
	character, _, _ := g.Character.Load(context.Background(), "31")
	character.Stats["raid_reward_v1_1_00112233445566778899aabbccddeeff_0_1_card"] = 1
	if e := g.Character.Save(context.Background(), character); e != nil {
		t.Fatal(e)
	}
	if _, e := o.Grant(nil, c); !errors.Is(e, ErrRewardReceipt) {
		t.Fatal(e)
	}
	if calls.Load() != 0 {
		t.Fatal("partial receipt rerolled/regranted")
	}
}

type rewardFailReceiptSave struct{ repo.CharacterRepository }

func (r rewardFailReceiptSave) Save(context.Context, repo.CharacterRecord) error {
	return errors.New("injected receipt save failure")
}

type rewardFailAssetCommit struct {
	repo.AccountCharacterAssetUnitOfWork
	lateCommit bool
}

func (u rewardFailAssetCommit) WithinAccountCharacterAssets(ctx context.Context, account, character string, fn func(repo.AccountInventoryRepository, repo.CharacterRepository, repo.InventoryRepository, repo.EquipmentRepository) error) error {
	return u.AccountCharacterAssetUnitOfWork.WithinAccountCharacterAssets(ctx, account, character, func(a repo.AccountInventoryRepository, c repo.CharacterRepository, i repo.InventoryRepository, e repo.EquipmentRepository) error {
		if !u.lateCommit {
			c = rewardFailReceiptSave{c}
		}
		if err := fn(a, c, i, e); err != nil {
			return err
		}
		return errors.New("injected late transaction failure")
	})
}

func TestRaidRewardOwnerRollbackAfterInventoryAndAccountSave(t *testing.T) {
	for _, late := range []bool{false, true} {
		t.Run(map[bool]string{false: "receipt_save", true: "late_commit"}[late], func(t *testing.T) {
			g, _, cmd, calls := rewardOwnerFixture(t)
			wrapped := g
			wrapped.AccountAssets = rewardFailAssetCommit{g.AccountAssets, late}
			owner, err := NewRewardOwner(wrapped)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := owner.Grant(nil, cmd); err == nil {
				t.Fatal("injected failure ignored")
			}
			inv, _, _ := g.Inventory.Load(context.Background(), "31")
			account, found, _ := g.AccountInventory.Load(context.Background(), "account")
			character, _, _ := g.Character.Load(context.Background(), "31")
			if len(inv.Slots) != 1 || found && len(account.Slots) != 0 || len(character.Stats) != 1 || calls.Load() != 1 {
				t.Fatal("late failure leaked inventory, account or receipt")
			}
			clean, _ := NewRewardOwner(g)
			if r, e := clean.Grant(nil, cmd); e != nil || r.Replayed {
				t.Fatalf("clean retry=%+v %v", r, e)
			}
		})
	}
}
