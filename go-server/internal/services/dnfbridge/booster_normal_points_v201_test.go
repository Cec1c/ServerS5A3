package dnfbridge

import (
	"context"
	"errors"
	"strconv"
	"testing"

	"longheng.io/server/internal/modules/dnf/cerashop"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

func mustNormalPointBoosterCatalog(t *testing.T, sourceItemID, rewardItemID, rewardCount uint32) *pvfDungeonDropCatalog {
	t.Helper()
	source := dungeonDropCatalogTestSource{
		"monster/monster.lst":     "",
		"equipment/equipment.lst": "",
		"stackable/stackable.lst": "970001 `cash/normal_point_box.stk`\n970002 `material/normal_point.stk`\n971001 `cash/other_normal_point_box.stk`\n",
		"stackable/cash/normal_point_box.stk": "[stackable type]\n`[booster]` 0\n" +
			"[stack limit]\n100\n" +
			"[booster info]\n[etc]\n1 970002 10000 2000\n[/etc]\n[/booster info]\n",
		"stackable/cash/other_normal_point_box.stk": "[stackable type]\n`[booster]` 0\n" +
			"[stack limit]\n100\n" +
			"[booster info]\n[etc]\n1 970002 10000 2\n[/etc]\n[/booster info]\n",
		"stackable/material/normal_point.stk": "[stackable type]\n`[material]` 4\n" +
			"[attach type]\n`[free]`\n" +
			"[stack limit]\n1000000\n",
	}
	if sourceItemID != 970001 || rewardItemID != 970002 || rewardCount != 2000 {
		source["stackable/stackable.lst"] = formatNormalPointPVFList(sourceItemID, rewardItemID)
		source["stackable/cash/normal_point_box.stk"] = formatNormalPointPVFBox(rewardItemID, rewardCount)
	}
	catalog, err := newPVFDungeonDropCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

func formatNormalPointPVFList(sourceItemID, rewardItemID uint32) string {
	return strconv.FormatUint(uint64(sourceItemID), 10) + " `cash/normal_point_box.stk`\n" +
		strconv.FormatUint(uint64(rewardItemID), 10) + " `material/normal_point.stk`\n"
}

func formatNormalPointPVFBox(rewardItemID, rewardCount uint32) string {
	return "[stackable type]\n`[booster]` 0\n[stack limit]\n100\n[booster info]\n[etc]\n1 " +
		strconv.FormatUint(uint64(rewardItemID), 10) + " 10000 " +
		strconv.FormatUint(uint64(rewardCount), 10) + "\n[/etc]\n[/booster info]\n"
}

type normalPointBoosterFixture struct {
	service *Service
	session *gameSession
	group   dnfrepo.Group
}

func newNormalPointBoosterFixture(
	t *testing.T,
	catalog *pvfDungeonDropCatalog,
	sourceItemID int64,
	cera string,
) *normalPointBoosterFixture {
	t.Helper()
	ctx := context.Background()
	group := dnfrepomemory.NewMemoryGroup()
	if err := group.Account.Save(ctx, dnfrepo.AccountRecord{
		AccountID: "account-1",
		Metadata:  map[string]string{"account_cera": cera},
	}); err != nil {
		t.Fatal(err)
	}
	if err := group.AccountInventory.Save(ctx, dnfrepo.AccountInventoryRecord{
		AccountID: "account-1",
		Slots:     map[string]dnfrepo.ItemStack{},
	}); err != nil {
		t.Fatal(err)
	}
	if err := group.Character.Save(ctx, dnfrepo.CharacterRecord{
		CharacterID: "19",
		AccountID:   "account-1",
		Stats:       map[string]int64{},
	}); err != nil {
		t.Fatal(err)
	}
	if err := group.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: "19",
		Slots: map[string]dnfrepo.ItemStack{
			"0:76": {ItemID: sourceItemID, Count: 1},
		},
	}); err != nil {
		t.Fatal(err)
	}
	service := currentBoosterTestService(catalog, group)
	session := &gameSession{
		conn:                &bufferConn{},
		connID:              "booster-normal-points-v201",
		accountID:           "account-1",
		selectedCharacterID: 19,
	}
	return &normalPointBoosterFixture{service: service, session: session, group: group}
}

func TestNormalPointV201BoosterCredits2000ToAccountWallet(t *testing.T) {
	fixture := newNormalPointBoosterFixture(
		t,
		mustNormalPointBoosterCatalog(t, 970001, 970002, 2000),
		970001,
		"500",
	)
	if err := fixture.service.handleCurrentBoosterItem(fixture.session, []byte{76, 0}); err != nil {
		t.Fatal(err)
	}

	account, found, err := fixture.group.Account.Load(context.Background(), "account-1")
	if err != nil || !found {
		t.Fatalf("load account found=%t err=%v", found, err)
	}
	if got := cerashop.Balance(account); got != 2500 {
		t.Fatalf("account_cera=%d want=2500 metadata=%v", got, account.Metadata)
	}
	inventory, found, err := fixture.group.Inventory.Load(context.Background(), "19")
	if err != nil || !found {
		t.Fatalf("load inventory found=%t err=%v", found, err)
	}
	if _, found := inventory.Slots["0:76"]; found {
		t.Fatal("source box was not consumed")
	}
	for key, stack := range inventory.Slots {
		if stack.ItemID == 970002 {
			t.Fatalf("normal point material leaked into inventory key=%s stack=%+v", key, stack)
		}
	}
	accountInventory, found, err := fixture.group.AccountInventory.Load(context.Background(), "account-1")
	if err != nil || !found {
		t.Fatalf("load account inventory found=%t err=%v", found, err)
	}
	for key, stack := range accountInventory.Slots {
		if stack.ItemID == 970002 {
			t.Fatalf("normal point material leaked into account inventory key=%s stack=%+v", key, stack)
		}
	}
}

func TestNormalPointV201BoosterOnlyCreditsGatedSource(t *testing.T) {
	fixture := newNormalPointBoosterFixture(
		t,
		mustNormalPointBoosterCatalog(t, 971001, 970002, 2),
		971001,
		"500",
	)
	if err := fixture.service.handleCurrentBoosterItem(fixture.session, []byte{76, 0}); err != nil {
		t.Fatal(err)
	}

	account, _, err := fixture.group.Account.Load(context.Background(), "account-1")
	if err != nil {
		t.Fatal(err)
	}
	if got := cerashop.Balance(account); got != 500 {
		t.Fatalf("other source changed account_cera=%d", got)
	}
	inventory, _, err := fixture.group.Inventory.Load(context.Background(), "19")
	if err != nil {
		t.Fatal(err)
	}
	foundReward := false
	for _, stack := range inventory.Slots {
		if stack.ItemID == 970002 && stack.Count == 2 {
			foundReward = true
			break
		}
	}
	if !foundReward {
		t.Fatalf("other source did not retain normal point item semantics: %+v", inventory.Slots)
	}
}

func TestNormalPointV201BoosterRejectsInvalidWalletStates(t *testing.T) {
	reward := currentBoosterReward{
		Definition: dungeonDropItemDefinition{ItemID: 970002},
		Count:      2,
	}
	t.Run("negative", func(t *testing.T) {
		account := &dnfrepo.AccountRecord{
			Metadata: map[string]string{"account_cera": "-1"},
		}
		_, err := currentBoosterCreditNormalPoints(account, 970001, reward)
		if !errors.Is(err, errCurrentBoosterNormalPointBalanceInvalid) {
			t.Fatalf("err=%v", err)
		}
	})
	t.Run("overflow", func(t *testing.T) {
		account := &dnfrepo.AccountRecord{
			Metadata: map[string]string{
				"account_cera": strconv.FormatInt(cerashop.MaxWalletBalance-1, 10),
			},
		}
		_, err := currentBoosterCreditNormalPoints(account, 970001, reward)
		if !errors.Is(err, cerashop.ErrWalletOverflow) {
			t.Fatalf("err=%v", err)
		}
	})
}
