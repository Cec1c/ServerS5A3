package dnfbridge

import (
	"context"
	"os"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	dnfinventory "longheng.io/server/internal/modules/dnf/inventory"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// 春节欢庆爆竹（[random reward item]）走普通 op44 右键路径：完整走一遍
// inventory owner + 真实运行时 PVF 的随机奖励解析，验证
//  1. owner 接受这个道具家族（此前只有 [waste]/[random reward item] 会被接受，
//     而 bridge 侧确实会把这个解析器传进 UseStackable），
//  2. 每次使用源道具都会减少 1（纯动画结果也要消耗），
//  3. 命中 1% 的出奖分支时，奖励 490701733 会真的落进背包。
//
// 出奖是随机的（99000 纯动画 / 1000 出奖），所以这里给一个足够大的栈并在循环里
// 等到第一次出奖；2000 次里一次都不出的概率约 0.99^2000 ≈ 2e-9，可以忽略。
func TestRealPVFFirecrackerUseConsumesSourceAndCanGrantThroughOwner(t *testing.T) {
	pvfPath := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to verify the firecracker use path")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: pvfPath})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	resolver := func(itemID int64) (alignedcmd.RandomRewardItemResolution, error) {
		return resolveCurrentRandomRewardItem(catalog, catalog.source, itemID)
	}
	resolution, err := resolver(490701730)
	if err != nil {
		t.Fatalf("resolve firecracker: %v", err)
	}
	if resolution.SourceItemID != 490701730 || len(resolution.Outcomes) != 2 {
		t.Fatalf("firecracker resolution = %+v", resolution)
	}

	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	const total = 2000
	if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: "79",
		Slots: map[string]dnfrepo.ItemStack{
			"0:65": {ItemID: 490701730, Count: total},
		},
	}); err != nil {
		t.Fatalf("save inventory: %v", err)
	}
	owner, err := dnfinventory.NewOwner(repos)
	if err != nil {
		t.Fatal(err)
	}

	remaining := int64(total)
	granted := false
	for attempt := 0; attempt < total && !granted; attempt++ {
		result, err := owner.UseStackable(ctx, dnfinventory.NewUseStackableCommand(alignedcmd.Request{
			AccountID:           "acc",
			SelectedCharacterID: 79,
		}, dnfinventory.UseStackableRequest{
			SlotIndex:     65,
			ListType:      dnfrepo.MainInventoryListType,
			InstanceValue: 0x11223344,
			ItemCode:      490701730,
		}), nil, resolver)
		if err != nil {
			t.Fatalf("use %d: %v", attempt, err)
		}
		if !result.Changed || result.RemainingCount != remaining-1 {
			t.Fatalf("use %d result = %+v (remaining=%d)", attempt, result, remaining)
		}
		remaining--
		if result.RandomRewardItemID != 0 {
			if result.RandomRewardItemID != 490701733 {
				t.Fatalf("granted item = %d, want 490701733", result.RandomRewardItemID)
			}
			granted = true
		}
	}
	if !granted {
		t.Fatalf("no reward in %d uses: the outcome grant path is broken", total)
	}

	loaded, ok, err := repos.Inventory.Load(ctx, "79")
	if err != nil || !ok {
		t.Fatalf("load inventory ok=%t err=%v", ok, err)
	}
	source := loaded.Slots["0:65"]
	if source.ItemID != 490701730 || source.Count != remaining {
		t.Fatalf("source stack = %+v, want item 490701730 count %d", source, remaining)
	}
	foundReward := false
	for _, stack := range loaded.Slots {
		if stack.ItemID == 490701733 {
			foundReward = true
		}
	}
	if !foundReward {
		t.Fatalf("granted reward 490701733 was not persisted: %+v", loaded.Slots)
	}
}
