package inventory

import (
	"context"
	"longheng.io/server/internal/modules/dnf/alignedcmd"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	"testing"
)

func TestRankUpgradeColorlessActualMaterialAndRollback(t *testing.T) {
	for _, failure := range []bool{false, true} {
		t.Run(map[bool]string{false: "提交", true: "后段失败"}[failure], func(t *testing.T) {
			ctx := context.Background()
			g := memory.NewMemoryGroup()
			seedUpgradeLateFailureWalletFixture(t, ctx, g)
			if err := g.AccountInventory.Save(ctx, repo.AccountInventoryRecord{AccountID: "acc", Slots: map[string]repo.ItemStack{"0:358": {ItemID: 3037, Count: 12}}}); err != nil {
				t.Fatal(err)
			}
			if failure {
				g.AccountAssets = upgradeLateFailureAccountAssets{base: g.AccountAssets, probe: &upgradeLateFailureProbe{saveErr: errUpgradeLateBagSave}}
			}
			owner, err := NewOwner(g)
			if err != nil {
				t.Fatal(err)
			}
			result, err := owner.Upgrade(ctx, Command{Operation: "upgrade_item", Mode: "reinforce", AccountID: "acc", SelectedCharacterID: 77, TargetSlotIndex: 9, TargetItemTemplateID: 700, MaterialSlotIndex: 358, OptionalTicketSlot: -1, UpgradeSuccessWeight: 100000, UpgradeMaterialItemID: 3037, UpgradeMaterialCount: 10, UpgradeCostResolver: func(string, int64, int, bool, uint16) (alignedcmd.UpgradeCostResolution, error) {
				return alignedcmd.UpgradeCostResolution{Resolved: true, GoldCost: 20}, nil
			}})
			if failure && (err == nil || result.Success) || !failure && (err != nil || !result.Success) {
				t.Fatal(result, err)
			}
			summary, e := g.Activity.Summary(ctx, repo.ActivityQuery{AccountID: "acc"})
			if e != nil {
				t.Fatal(e)
			}
			stock, _, e := g.AccountInventory.Load(ctx, "acc")
			if e != nil {
				t.Fatal(e)
			}
			wantSpent, wantStock := int64(10), int64(2)
			if failure {
				wantSpent, wantStock = 0, 12
			}
			if summary.Metrics[repo.ActivityColorless] != wantSpent || summary.Metrics[repo.ActivityGoldReinforce] != wantSpent*2 || stock.Slots["0:358"].Count != wantStock {
				t.Fatal(summary, stock)
			}
		})
	}
}
