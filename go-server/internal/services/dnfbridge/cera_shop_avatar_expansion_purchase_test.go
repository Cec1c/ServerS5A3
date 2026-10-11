package dnfbridge

import (
	"context"
	"strconv"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

// End-to-end coin-shop purchase path for the avatar-inventory expansion coupon:
// shop row -> classification (stage 1 = item 2683901) -> checkout (1980 Cera) ->
// persisted container stage (AvatarExpansion = 7).
//
// It also pins the two rejections that must never charge Cera: re-buying the
// same stage and skipping a stage. The live etc/cerashop.etc sells these as
// commodity 102644..102651 (stages 1..8, 1980 Cera each); the fixture below uses
// the same row shape and the real item PVF directory.
func TestCommitCurrentCeraShopPurchaseAppliesAvatarInventoryExpansionEndToEnd(t *testing.T) {
	catalog := mustCurrentCeraShopTestCatalogWithAvatarExpansion(t)
	repositories := dnfrepomemory.NewMemoryGroup()
	ctx := context.Background()
	seedCeraShopAccountCera(t, ctx, repositories, "account-1", 5000)
	if err := repositories.Character.Save(ctx, dnfrepo.CharacterRecord{
		CharacterID: "31",
		AccountID:   "account-1",
		Stats:       make(map[string]int64),
	}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: "31",
		Slots:       make(map[string]dnfrepo.ItemStack),
	}); err != nil {
		t.Fatal(err)
	}
	service := &Service{
		options:            options{accountID: "account-1"},
		repositoryProvider: func() (dnfrepo.Group, bool) { return repositories, true },
	}
	session := &gameSession{selectedCharacterID: 31}
	buy := func(commodityID uint32) (currentCeraShopCommitResult, error) {
		return service.commitCurrentCeraShopPurchase(ctx, session, catalog, currentCeraShopPurchaseRequest{
			PaymentMode: currentCeraShopPaymentModeCera,
			Items:       []currentCeraShopPurchaseRequestItem{{CommodityID: commodityID}},
		})
	}

	result, err := buy(102644)
	if err != nil {
		t.Fatalf("buying stage 1: %v", err)
	}
	if result.AvatarInventoryExpansion != 7 {
		t.Fatalf("expansion = %d, want 7 (stage 1)", result.AvatarInventoryExpansion)
	}
	if result.CeraAfter != 5000-1980 {
		t.Fatalf("cera after = %d, want %d", result.CeraAfter, 5000-1980)
	}
	state, found, err := dnfrepo.LoadCharacterContainerState(ctx, repositories.Settings, "31")
	if err != nil || !found {
		t.Fatalf("container state found=%t err=%v", found, err)
	}
	if state.AvatarExpansion != 7 {
		t.Fatalf("persisted avatar expansion = %d, want 7", state.AvatarExpansion)
	}
	if got := loadCeraShopAccountCera(t, ctx, repositories, "account-1"); got != 3020 {
		t.Fatalf("account cera = %d, want 3020", got)
	}

	if _, err := buy(102644); err == nil {
		t.Fatal("re-buying the same expansion stage must be rejected")
	}
	if _, err := buy(102646); err == nil {
		t.Fatal("skipping an expansion stage must be rejected")
	}
	if got := loadCeraShopAccountCera(t, ctx, repositories, "account-1"); got != 3020 {
		t.Fatalf("rejected purchases charged cera: balance = %d, want 3020", got)
	}
	state, _, err = dnfrepo.LoadCharacterContainerState(ctx, repositories.Settings, "31")
	if err != nil || state.AvatarExpansion != 7 {
		t.Fatalf("rejected purchases changed the container state: expansion=%d err=%v", state.AvatarExpansion, err)
	}

	result, err = buy(102645)
	if err != nil {
		t.Fatalf("buying stage 2: %v", err)
	}
	if result.AvatarInventoryExpansion != 14 {
		t.Fatalf("stage 2 expansion = %d, want 14", result.AvatarInventoryExpansion)
	}
	state, _, _ = dnfrepo.LoadCharacterContainerState(ctx, repositories.Settings, "31")
	if state.AvatarExpansion != 14 {
		t.Fatalf("persisted avatar expansion after stage 2 = %d, want 14", state.AvatarExpansion)
	}
}

// The fixture mirrors the live shop row shape: [item] rows 102644/102645/102646
// grant the stage 1/2/3 coupons (items 2683901..2683903) for 1980 Cera, and the
// items live under the official stackable/cash/chn_20140819_avatar_inven/ path
// that currentCeraShopAvatarInventoryUpgradeStage recognises.
func mustCurrentCeraShopTestCatalogWithAvatarExpansion(t *testing.T) *pvfCeraShopCatalog {
	t.Helper()
	catalog, err := newPVFCeraShopCatalog(bridgePVFSource{
		"etc/cerashop.etc": "[item]\n" +
			"102644 2683901 1 0 0 1980 `avatar inventory expansion stage 1` 0 0\n" +
			"102645 2683902 1 0 0 1980 `avatar inventory expansion stage 2` 0 0\n" +
			"102646 2683903 1 0 0 1980 `avatar inventory expansion stage 3` 0 0\n" +
			"[/item]\n" +
			"[premium]\n[/premium]\n[creature]\n[/creature]\n[coin]\n[/coin]\n" +
			"[material]\n[/material]\n[recoveryitem]\n[/recoveryitem]\n" +
			"[package]\n[/package]\n[visual]\n[/visual]\n[avatar]\n[/avatar]\n" +
			"[selectable character premium]\n[/selectable character premium]\n" +
			"[charac premium package]\n[/charac premium package]\n",
		"monster/monster.lst":     "",
		"equipment/equipment.lst": "",
		"stackable/stackable.lst": "2683901 `cash/chn_20140819_avatar_inven/avatar_inven_2683901.stk`\n" +
			"2683902 `cash/chn_20140819_avatar_inven/avatar_inven_2683902.stk`\n" +
			"2683903 `cash/chn_20140819_avatar_inven/avatar_inven_2683903.stk`\n",
		"stackable/cash/chn_20140819_avatar_inven/avatar_inven_2683901.stk": avatarExpansionCouponPVF(1),
		"stackable/cash/chn_20140819_avatar_inven/avatar_inven_2683902.stk": avatarExpansionCouponPVF(2),
		"stackable/cash/chn_20140819_avatar_inven/avatar_inven_2683903.stk": avatarExpansionCouponPVF(3),
	})
	if err != nil {
		t.Fatalf("build cera shop catalog with avatar expansion: %v", err)
	}
	return catalog
}

func avatarExpansionCouponPVF(stage int) string {
	label := strconv.Itoa(stage)
	return "[name]\n`avatar inventory expansion stage " + label + "`\n" +
		"[stackable type]\n`[etc]`\n[stack limit]\n1\n"
}
