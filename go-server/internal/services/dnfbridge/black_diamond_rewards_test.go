package dnfbridge

import (
	"context"
	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/inventory"
	"longheng.io/server/internal/modules/dnf/premium"
	doc "longheng.io/server/internal/modules/dnf/pvf"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	archive "longheng.io/server/internal/platform/pvf"
	"os"
	"testing"
	"time"
)

func TestNewBlackDiamondPVFContractRequiresUseAndRenewsThirtyDays(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_BLACK_DIAMOND_PVF")
	if path == "" {
		t.Skip("patched black diamond PVF not supplied")
	}
	a, err := archive.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := buildCurrentPremiumCatalog(a)
	if err != nil {
		t.Fatal(err)
	}
	info, found := catalog.contractsByItem[premium.BlackDiamondThirtyDayItemID]
	if !found || info.PremiumType != premium.TypeBlackDiamond || info.DurationSeconds != 30*86400 {
		t.Fatalf("new contract=%+v", info)
	}
	if _, exists := currentPremiumInventoryContracts(catalog)[info.ItemID]; exists {
		t.Fatal("new black diamond voucher would auto-activate on acquisition")
	}
	if kind, duration := currentMagicBoxRewardContract(catalog, info.ItemID); kind != 0 || duration != 0 {
		t.Fatal("magic box would consume new voucher before player uses it")
	}
	repos := memory.NewMemoryGroup()
	ctx := context.Background()
	if err = repos.Account.Save(ctx, repo.AccountRecord{AccountID: "blackdiamond"}); err != nil {
		t.Fatal(err)
	}
	if err = repos.Character.Save(ctx, repo.CharacterRecord{CharacterID: "77", AccountID: "blackdiamond"}); err != nil {
		t.Fatal(err)
	}
	if err = repos.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: "77", Slots: map[string]repo.ItemStack{"0:4": {ItemID: info.ItemID, Count: 2}}}); err != nil {
		t.Fatal(err)
	}
	owner, err := inventory.NewOwner(repos)
	if err != nil {
		t.Fatal(err)
	}
	cmd := inventory.NewUseStackableCommand(alignedcmd.Request{AccountID: "blackdiamond", SelectedCharacterID: 77}, inventory.UseStackableRequest{SlotIndex: 4, ListType: 0, InstanceValue: 0x11223344, ItemCode: int32(info.ItemID)})
	resolver := func(id int64) (alignedcmd.PremiumContractResolution, error) {
		return alignedcmd.PremiumContractResolution{ItemID: info.ItemID, PremiumType: info.PremiumType, DurationSeconds: info.DurationSeconds}, nil
	}
	result, err := owner.UseStackable(ctx, cmd, resolver)
	if err != nil || !result.PremiumActivated || result.RemainingCount != 1 {
		t.Fatalf("first use=%+v err=%v", result, err)
	}
	account, _, _ := repos.Account.Load(ctx, "blackdiamond")
	firstExpire := premium.ExpireAt(account, premium.TypeBlackDiamond)
	if remaining := firstExpire - time.Now().Unix(); remaining < 30*86400-5 || remaining > 30*86400 {
		t.Fatalf("first expiry remaining=%d", remaining)
	}
	result, err = owner.UseStackable(ctx, cmd, resolver)
	if err != nil || !result.PremiumActivated || result.RemainingCount != 0 {
		t.Fatalf("second use=%+v err=%v", result, err)
	}
	account, _, _ = repos.Account.Load(ctx, "blackdiamond")
	if got := premium.ExpireAt(account, premium.TypeBlackDiamond); got != firstExpire+30*86400 {
		t.Fatalf("renewal expiry=%d want=%d", got, firstExpire+30*86400)
	}
}

func TestBlackDiamondExtraCardCommitsWithFreeRewardExactlyOnce(t *testing.T) {
	plan, e := newDungeonCardRewardPlan(dungeonCardPlanIdentity{CharacterID: "99", DungeonID: 1}, "black-diamond-test", dungeonCardRewardBundle{Gold: 10}, dungeonCardRewardBundle{Gold: 30}, dungeonCardRewardBundle{Gold: 20})
	if e != nil {
		t.Fatal(e)
	}
	state, e := newDungeonCardState(plan)
	if e != nil {
		t.Fatal(e)
	}
	repos := memory.NewMemoryGroup()
	ctx := context.Background()
	repos.Character.Save(ctx, repo.CharacterRecord{CharacterID: "99", Stats: map[string]int64{"gold": 5, "fatigue": 23}})
	reservation, _, e := state.reserveSelection(dungeonCardSideFree, 1)
	if e != nil {
		t.Fatal(e)
	}
	result, e := deliverDungeonCardReservation(ctx, state, reservation, repos, 0, time.Now())
	if e != nil {
		t.Fatal(e)
	}
	if result.GoldAfter != 35 {
		t.Fatalf("main and black diamond bonus not committed together: %+v", result)
	}
	replay, _, e := state.reserveSelection(dungeonCardSideFree, 1)
	if e != nil || replay.grant {
		t.Fatal("extra card replay granted twice")
	}
	character, _, _ := repos.Character.Load(ctx, "99")
	if character.Stats["gold"] != 35 || character.Stats["fatigue"] != 23 {
		t.Fatalf("durable result=%v", character.Stats)
	}
}
func TestRealPVFBlackDiamondVendingGroups(t *testing.T) {
	source := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if source == "" {
		t.Skip()
	}
	a, e := archive.Open(source)
	if e != nil {
		t.Fatal(e)
	}
	for _, machine := range []struct {
		path string
		coin int64
	}{{"etc/pcroom.vm", 7454}, {"etc/pcroom3.vm", 10005506}} {
		text, e := a.ReadText(machine.path)
		if e != nil {
			t.Fatal(e)
		}
		d, e := doc.Parse(machine.path, text)
		if e != nil {
			t.Fatal(e)
		}
		cost, rows, found := parseVendingGroup(d, 1, machine.coin)
		if !found || cost != 1 || len(rows) == 0 {
			t.Fatalf("machine=%s cost=%d rows=%d found=%t", machine.path, cost, len(rows), found)
		}
		t.Logf("machine=%s coin=%d cost=%d outcomes=%d", machine.path, machine.coin, cost, len(rows))
	}
}
