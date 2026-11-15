package packageitem

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestMagicBoxCommittedSlotsMatchActualInventoryChanges(t *testing.T) {
	for _, mode := range []string{"single", "batch", "material_other_list", "overflow", "contract"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			g := dnfrepomemory.NewMemoryGroup()
			slots := map[string]dnfrepo.ItemStack{
				"0:67":  {ItemID: 10007368, Count: 10, Extra: map[string]string{"item_kind": "stackable"}},
				"0:70":  {ItemID: 2600014, Count: 5, Extra: map[string]string{"item_kind": "stackable"}},
				"0:121": {ItemID: 10007367, Count: 10, Extra: map[string]string{"item_kind": "stackable"}},
				"0:75":  {ItemID: 500001, Count: 5},
			}
			materialSlot := int16(121)
			if mode == "material_other_list" {
				slots["1:4"] = slots["0:121"]
				delete(slots, "0:121")
				materialSlot = -1
			}
			saveMagicBoxFixture(t, ctx, g, slots)
			before, _, _ := g.Inventory.Load(ctx, "77")
			owner, err := NewOwner(g)
			if err != nil {
				t.Fatal(err)
			}
			resolution := alignedcmd.MagicBoxResolution{Kind: "random", MaterialItemID: 10007367, MaterialCountPerUse: 1, Groups: []alignedcmd.MagicBoxRewardGroup{{DrawCount: 1, Entries: []alignedcmd.MagicBoxRewardEntry{{ItemID: 2600014, Weight: 100, Count: 2}}}}}
			reward := alignedcmd.MagicBoxRewardItem{ItemID: 2600014, Kind: "stackable", SlotStart: 65, SlotEnd: 120}
			if mode == "overflow" {
				reward.SlotStart, reward.SlotEnd = 100, 110 // outside the default unlocked page
			}
			if mode == "contract" {
				reward.PremiumType, reward.PremiumDurationSeconds = 22, 86400
			}
			var result MagicBoxResult
			if mode == "batch" {
				result, err = owner.ApplyMagicBoxExpand(ctx, magicBoxExpandTestCommand(67, materialSlot, 10), staticMagicBoxResolver(resolution), staticMagicBoxRewardResolver(map[int64]alignedcmd.MagicBoxRewardItem{2600014: reward}))
			} else {
				result, err = owner.ApplyMagicBox(ctx, magicBoxTestCommand(67, materialSlot), staticMagicBoxResolver(resolution), staticMagicBoxRewardResolver(map[int64]alignedcmd.MagicBoxRewardItem{2600014: reward}))
			}
			if err != nil || !result.Success {
				t.Fatalf("grant failed: %v %+v", err, result)
			}
			after, _, _ := g.Inventory.Load(ctx, "77")
			actual := make(magicBoxSlotChanges)
			for key, stack := range before.Slots {
				if current, found := after.Slots[key]; !found || !reflect.DeepEqual(stack, current) {
					actual.record(key)
				}
			}
			for key, stack := range after.Slots {
				if previous, found := before.Slots[key]; !found || !reflect.DeepEqual(stack, previous) {
					actual.record(key)
				}
			}
			if !reflect.DeepEqual(actual.slots(), result.CommittedItemSlots) {
				t.Fatalf("reported=%v actual=%v", result.CommittedItemSlots, actual.slots())
			}
		})
	}
}

type abortCommittedMagicBoxAssets struct {
	dnfrepo.AccountCharacterAssetUnitOfWork
	ledger dnfrepo.AccountCharacterLedgerMailboxAssetUnitOfWork
}

func (u abortCommittedMagicBoxAssets) WithinAccountCharacterLedgerMailboxAssets(ctx context.Context, a, c string, apply func(dnfrepo.AccountRepository, dnfrepo.AccountInventoryRepository, dnfrepo.CharacterRepository, dnfrepo.InventoryRepository, dnfrepo.EquipmentRepository, dnfrepo.MailboxRepository) error) error {
	return u.ledger.WithinAccountCharacterLedgerMailboxAssets(ctx, a, c, func(ar dnfrepo.AccountRepository, ai dnfrepo.AccountInventoryRepository, cr dnfrepo.CharacterRepository, ir dnfrepo.InventoryRepository, er dnfrepo.EquipmentRepository, mr dnfrepo.MailboxRepository) error {
		if err := apply(ar, ai, cr, ir, er, mr); err != nil {
			return err
		}
		return errors.New("abort after all grant writes")
	})
}

func TestMagicBoxFailedCommitDoesNotReturnSlotProjection(t *testing.T) {
	g := dnfrepomemory.NewMemoryGroup()
	saveMagicBoxFixture(t, context.Background(), g, magicBoxBaseSlots())
	before, _, _ := g.Inventory.Load(context.Background(), "77")
	g.AccountAssets = abortCommittedMagicBoxAssets{g.AccountAssets, g.AccountAssets.(dnfrepo.AccountCharacterLedgerMailboxAssetUnitOfWork)}
	result := handleMagicBoxRequest(t, g, staticMagicBoxResolver(magicBoxTestResolution()), staticMagicBoxRewardResolver(magicBoxTestRewardItems()), magicBoxRequestBody(10, 121))
	if result.ItemGrantProjection != nil || result.ResponseAllowed || len(result.ItemSlotRefreshes) != 0 || len(result.PostActions) != 0 {
		t.Fatal("aborted transaction returned a committed item projection")
	}
	after, _, _ := g.Inventory.Load(context.Background(), "77")
	if !reflect.DeepEqual(before, after) {
		t.Fatal("failed grant changed inventory")
	}
}
