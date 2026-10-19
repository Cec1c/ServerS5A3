package pvp

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"sync"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/inventory"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

func totalRewardFixture(t *testing.T) (repo.Group, *Catalog, time.Time) {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC)
	r := memory.NewMemoryGroup()
	raw, _ := json.Marshal(TotalProgress{Stats: map[string]int64{CertificationKey: 99, WinsKey: 99}})
	if err := r.Account.Save(ctx, repo.AccountRecord{AccountID: "a", Metadata: map[string]string{TotalProgressMetadataKey: string(raw), "keep": "yes"}}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"1", "2", "3"} {
		if err := r.Character.Save(ctx, repo.CharacterRecord{AccountID: "a", CharacterID: id, Stats: map[string]int64{EmblemsKey: 7, WinsKey: 12}}); err != nil {
			t.Fatal(err)
		}
		if err := r.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: id, Slots: map[string]repo.ItemStack{"0:10": {ItemID: 99, Count: 4}}}); err != nil {
			t.Fatal(err)
		}
	}
	c := totalProgressTestCatalog(t)
	c.Normal[0].Emblems, c.Normal[0].RewardItemID, c.Normal[0].RewardItemCount = 10, 99, 2
	c.Normal[1].Emblems, c.Normal[1].RewardItemID, c.Normal[1].RewardItemCount = 100, 99, 3
	c.Normal[2].Emblems, c.Normal[2].RewardItemID, c.Normal[2].RewardItemCount = 20, 99, 3
	return r, c, now
}

func totalPlaceTestReward(record repo.InventoryRecord, id, count int64, now time.Time) (repo.InventoryRecord, error) {
	out, err := inventory.PlaceStackableReward(record, alignedcmd.MagicBoxRewardItem{ItemID: id, Kind: "stackable", StackLimit: 5, SlotStart: 10, SlotEnd: 12, PVFPath: "fixture.stk"}, count)
	if errors.Is(err, inventory.ErrRandomRewardInventoryFull) {
		return repo.InventoryRecord{}, ErrTotalRewardInventoryFull
	}
	return out, err
}

func TestTotalTargetRewardsResumeAfterProgressAndAtomicFailure(t *testing.T) {
	r, c, now := totalRewardFixture(t)
	ctx := context.Background()
	before, err := GrantTotalTargets(ctx, r, c, "a", "1", now, totalPlaceTestReward)
	if err != nil || len(before.Granted) > 0 || before.Character.Stats[EmblemsKey] != 7 {
		t.Fatal(before, err)
	}
	match := TotalMatchFacts{Match: Match{Key: "reward-threshold", StartedAt: now, CompletedAt: now.Add(time.Minute), Won: true}, Remaining: 1}
	if _, err := CompleteTotal(ctx, r, c, "a", "1", [3]string{"1", "2", "3"}, match); err != nil {
		t.Fatal(err)
	}
	accountBefore, _, _ := r.Account.Load(ctx, "a")
	characterBefore, _, _ := r.Character.Load(ctx, "1")
	invBefore, _, _ := r.Inventory.Load(ctx, "1")
	bad := r
	bad.RentalAssets = renameFailCommit{r.RentalAssets}
	if _, err := GrantTotalTargets(ctx, bad, c, "a", "1", now, totalPlaceTestReward); err == nil {
		t.Fatal("injected failure accepted")
	}
	a, _, _ := r.Account.Load(ctx, "a")
	ch, _, _ := r.Character.Load(ctx, "1")
	inv, _, _ := r.Inventory.Load(ctx, "1")
	if !reflect.DeepEqual(a, accountBefore) || !reflect.DeepEqual(ch, characterBefore) || !reflect.DeepEqual(inv, invBefore) {
		t.Fatal("partial reward escaped rollback")
	}
	got, err := GrantTotalTargets(ctx, r, c, "a", "1", now, totalPlaceTestReward)
	if err != nil || !reflect.DeepEqual(got.Granted, []uint16{800, 824}) || got.Character.Stats[EmblemsKey] != 37 || got.Character.Stats[WinsKey] != 12 {
		t.Fatal(got, err)
	}
	if got.Items[10].Count != 5 || got.Items[11].Count != 4 {
		t.Fatal("reward stacks not merged/split", got.Items)
	}
	for _, id := range []string{"1", "2", "1"} {
		retry, err := GrantTotalTargets(ctx, r, c, "a", id, now.Add(24*time.Hour), totalPlaceTestReward)
		if err != nil || len(retry.Granted) != 0 {
			t.Fatal("duplicate reward", retry, err)
		}
	}
	// A replay of the match receipt preserves the account paid markers.
	if _, err := CompleteTotal(ctx, r, c, "a", "1", [3]string{"1", "2", "3"}, match); err != nil {
		t.Fatal(err)
	}
	if retry, err := GrantTotalTargets(ctx, r, c, "a", "1", now, totalPlaceTestReward); err != nil || len(retry.Granted) != 0 || retry.Character.Stats[EmblemsKey] != 37 {
		t.Fatal(retry, err)
	}
}

func TestTotalTargetRewardsFullInventoryDefersWithoutPartialItems(t *testing.T) {
	r, c, now := totalRewardFixture(t)
	ctx := context.Background()
	a, _, _ := r.Account.Load(ctx, "a")
	p, _ := ReadTotalProgress(a)
	p.Stats[CertificationKey] = 100
	raw, _ := json.Marshal(p)
	a.Metadata[TotalProgressMetadataKey] = string(raw)
	_ = r.Account.Save(ctx, a)
	inv, _, _ := r.Inventory.Load(ctx, "1")
	inv.Slots["0:11"] = repo.ItemStack{ItemID: 88, Count: 1}
	inv.Slots["0:12"] = repo.ItemStack{ItemID: 88, Count: 1}
	_ = r.Inventory.Save(ctx, inv)
	got, err := GrantTotalTargets(ctx, r, c, "a", "1", now, totalPlaceTestReward)
	if err != nil || !reflect.DeepEqual(got.Pending, []uint16{800}) || len(got.Granted) != 0 || got.Character.Stats[EmblemsKey] != 7 || got.Items[10].Count != 4 {
		t.Fatal(got, err)
	}
	delete(inv.Slots, "0:11")
	_ = r.Inventory.Save(ctx, inv)
	got, err = GrantTotalTargets(ctx, r, c, "a", "1", now, totalPlaceTestReward)
	if err != nil || !reflect.DeepEqual(got.Granted, []uint16{800}) || got.Items[10].Count != 5 || got.Items[11].Count != 1 || got.Character.Stats[EmblemsKey] != 17 {
		t.Fatal(got, err)
	}
}

func TestTotalTargetRewardsAccountScopeConcurrentCharactersAndOverflow(t *testing.T) {
	r, c, now := totalRewardFixture(t)
	ctx := context.Background()
	a, _, _ := r.Account.Load(ctx, "a")
	p, _ := ReadTotalProgress(a)
	p.Stats[CertificationKey] = 333
	raw, _ := json.Marshal(p)
	a.Metadata[TotalProgressMetadataKey] = string(raw)
	_ = r.Account.Save(ctx, a)
	ch, _, _ := r.Character.Load(ctx, "1")
	ch.Stats[EmblemsKey] = math.MaxInt32
	_ = r.Character.Save(ctx, ch)
	if _, err := GrantTotalTargets(ctx, r, c, "a", "1", now, totalPlaceTestReward); err == nil {
		t.Fatal("overflow accepted")
	}
	if _, err := GrantTotalTargets(ctx, r, c, "foreign", "1", now, totalPlaceTestReward); err == nil {
		t.Fatal("foreign owner accepted")
	}
	ch.Stats[EmblemsKey] = 7
	_ = r.Character.Save(ctx, ch)
	var wg sync.WaitGroup
	results := make(chan TotalRewards, 2)
	errs := make(chan error, 2)
	for _, id := range []string{"1", "2"} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			got, err := GrantTotalTargets(ctx, r, c, "a", id, now, totalPlaceTestReward)
			results <- got
			errs <- err
		}(id)
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	count := 0
	for r := range results {
		count += len(r.Granted)
	}
	if count != 2 {
		t.Fatal("account chain paid twice", count)
	}
	var wallet, items int64
	for _, id := range []string{"1", "2"} {
		ch, _, _ := r.Character.Load(ctx, id)
		wallet += ch.Stats[EmblemsKey]
		inv, _, _ := r.Inventory.Load(ctx, id)
		for _, stack := range inv.Slots {
			items += stack.Count
		}
	}
	if wallet != 124 || items != 13 {
		t.Fatal("concurrent reward totals", wallet, items)
	}
}
