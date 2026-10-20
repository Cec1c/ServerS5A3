package dnfbridge

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	dnfrevivecoin "longheng.io/server/internal/modules/dnf/revivecoin"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// Candidate validation uses disposable memory repositories, never production
// balances or inventories. Each real shelf row must buy and open successfully.
func TestRealPVFGiftShopCandidatePurchaseAndOpen(t *testing.T) {
	path, planPath, reportPath := os.Getenv("DNFBRIDGE_GIFT_CANDIDATE"), os.Getenv("DNFBRIDGE_GIFT_PLAN"), os.Getenv("DNFBRIDGE_GIFT_TRANSACTION_REPORT")
	if path == "" || planPath == "" || reportPath == "" {
		t.Skip("candidate transaction audit paths required")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFCeraShopCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatal(err)
	}
	var plan struct {
		Rows []struct {
			ID    uint32 `json:"item_id"`
			Price uint32 `json:"price"`
		} `json:"rows"`
	}
	if err = json.Unmarshal(data, &plan); err != nil {
		t.Fatal(err)
	}
	type outcome struct {
		ID         uint32 `json:"id"`
		Commodity  uint32 `json:"commodity"`
		RewardRows int    `json:"reward_rows"`
		Error      string `json:"error,omitempty"`
	}
	rows := []outcome{}
	failed := 0
	for _, wanted := range plan.Rows {
		r := outcome{ID: wanted.ID}
		var product currentCeraShopProduct
		count := 0
		for _, v := range catalog.products {
			if v.ItemID == wanted.ID && v.Section == "package" {
				product = v
				count++
			}
		}
		if count != 1 || product.CeraPrice != wanted.Price || product.Count != 1 {
			r.Error = "catalog identity/count/price mismatch"
		} else {
			r.Commodity = product.CommodityID
			r.Error = candidateGiftPurchaseOpen(t, catalog, product, &r.RewardRows)
		}
		if r.Error != "" {
			failed++
		}
		rows = append(rows, r)
	}
	report := map[string]any{"passed": failed == 0 && len(rows) > 0, "tested": len(rows), "failed": failed, "repository": "disposable_memory", "rows": rows}
	encoded, _ := json.MarshalIndent(report, "", "  ")
	if err = os.WriteFile(reportPath, encoded, 0600); err != nil {
		t.Fatal(err)
	}
	if failed > 0 {
		t.Fatalf("gift transactions: %d/%d failed; see report", failed, len(rows))
	}
	t.Logf("%d real gift rows purchased and opened with exact rewards", len(rows))
}

func candidateGiftPurchaseOpen(t *testing.T, catalog *pvfCeraShopCatalog, product currentCeraShopProduct, rewardRows *int) string {
	t.Helper()
	ctx := context.Background()
	repositories := dnfrepomemory.NewMemoryGroup()
	seedCeraShopAccountCera(t, ctx, repositories, "candidate-gift", 1000000)
	if err := repositories.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "19", AccountID: "candidate-gift"}); err != nil {
		return err.Error()
	}
	if err := repositories.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "19", Slots: map[string]dnfrepo.ItemStack{}}); err != nil {
		return err.Error()
	}
	service := &Service{options: options{accountID: "candidate-gift"}, pvfItemCatalog: catalog.items, ceraShopCatalog: catalog, repositoryProvider: func() (dnfrepo.Group, bool) { return repositories, true }}
	session := &gameSession{selectedCharacterID: 19}
	request, err := parseCurrentCeraShopPurchaseRequest(currentCeraShopTestRequestBody(product.CommodityID))
	if err != nil {
		return err.Error()
	}
	if _, err = service.commitCurrentCeraShopPurchase(ctx, session, catalog, request); err != nil {
		return "purchase: " + err.Error()
	}
	if balance := loadCeraShopAccountCera(t, ctx, repositories, "candidate-gift"); balance != 1000000-int64(product.CeraPrice) {
		return "exact price deduction failed"
	}
	inventory, _, err := repositories.Inventory.Load(ctx, "19")
	if err != nil {
		return err.Error()
	}
	sourceSlot := int16(-1)
	for key, stack := range inventory.Slots {
		if stack.ItemID == int64(product.ItemID) && stack.Count == 1 {
			list, slot, ok := parseSceneInventorySlotKey(key)
			if ok && list == 0 {
				sourceSlot = slot
			}
		}
	}
	if sourceSlot < 0 {
		return "purchased gift absent from main inventory"
	}
	definition, err := resolveCurrentCeraPackageDefinition(catalog.items, product.ItemID)
	if err != nil {
		return err.Error()
	}
	choices := []currentCeraPackageChoice{}
	for _, id := range definition.AvatarItemIDs {
		choices = append(choices, currentCeraPackageChoice{ItemID: id, Option: 0})
	}
	result, err := service.commitCurrentCeraPackage(ctx, session, catalog.items, definition, currentCeraPackageOpenRequest{SourceSlot: sourceSlot, Choices: choices})
	if err != nil {
		return "open: " + err.Error()
	}
	*rewardRows = result.RewardRows
	inventory, _, err = repositories.Inventory.Load(ctx, "19")
	if err != nil {
		return err.Error()
	}
	actual := map[uint32]int64{}
	for _, stack := range inventory.Slots {
		actual[uint32(stack.ItemID)] += stack.Count
	}
	if actual[product.ItemID] != 0 {
		return "source gift was not consumed"
	}
	expected := map[uint32]int64{}
	for _, reward := range definition.Rewards {
		id := reward.Definition.ItemID
		if dnfrevivecoin.IsReward(int64(id)) {
			id = uint32(dnfrevivecoin.WalletItemID)
		}
		expected[id] += int64(reward.Count)
	}
	if count := expected[uint32(dnfrevivecoin.WalletItemID)]; count > 0 {
		wallet, found := inventory.Slots[dnfrevivecoin.WalletKey()]
		if !found || wallet.ItemID != dnfrevivecoin.WalletItemID || wallet.Count != count {
			return "revive coins were not credited to the fixed wallet slot"
		}
	}
	for id, count := range expected {
		if actual[id] != count {
			return fmt.Sprintf("reward %d count=%d want=%d", id, actual[id], count)
		}
		delete(actual, id)
	}
	if len(actual) != 0 {
		return fmt.Sprintf("unexpected rewards %v", actual)
	}
	return ""
}

func TestRealPVFGiftFateWrapperEveryChoiceCommits(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_GIFT_CANDIDATE")
	if path == "" {
		t.Skip("candidate PVF required")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	checked := 0
	for _, source := range []uint32{490709900, 490709901, 490709902} {
		definition, err := resolveCurrentBoosterDefinition(catalog, source, currentBoosterRequestSelection)
		if err != nil {
			t.Fatal(err)
		}
		if len(definition.Selection) < 2 {
			t.Fatalf("wrapper %d has no real alternatives", source)
		}
		for _, candidate := range definition.Selection {
			repositories := dnfrepomemory.NewMemoryGroup()
			seedCeraShopAccountCera(t, ctx, repositories, "fate-candidate", 0)
			if err := repositories.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "19", AccountID: "fate-candidate"}); err != nil {
				t.Fatal(err)
			}
			if err := repositories.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "19", Slots: map[string]dnfrepo.ItemStack{"0:75": {ItemID: int64(source), Count: 2}}}); err != nil {
				t.Fatal(err)
			}
			service := &Service{options: options{accountID: "fate-candidate"}, pvfItemCatalog: catalog, repositoryProvider: func() (dnfrepo.Group, bool) { return repositories, true }}
			request := currentBoosterOpenRequest{Kind: currentBoosterRequestSelection, SourceSlot: 75, SelectionContext: 0, Selections: []currentBoosterSelectionRequest{{ItemID: candidate.ItemID, Option: candidate.Option}}, SelectedItemID: candidate.ItemID}
			result, err := service.commitCurrentBooster(ctx, &gameSession{selectedCharacterID: 19}, catalog, definition, request)
			if err != nil {
				t.Fatalf("wrapper %d choice %d: %v", source, candidate.ItemID, err)
			}
			if result.SourceRemaining != 1 || len(result.Rewards) != 1 || result.Rewards[0].ItemID != candidate.ItemID || result.Rewards[0].Count != candidate.Count {
				t.Fatalf("wrapper %d wrong selected reward %+v", source, result)
			}
			checked++
		}
	}
	t.Logf("all %d Fate wrapper alternatives committed independently", checked)
}
