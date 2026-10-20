package dnfbridge

import (
	"os"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// TestAlignedStackableUseProvenanceResolverOnlyOnUseOpcodes 锁死装载范围：
// 只有 op44（use stackable）与 op251（共享特效道具，复用同一条 owner 路径）
// 才装这个解析器；其余 opcode 一律不装（为零 = 领域层走库存元数据判据）。
func TestAlignedStackableUseProvenanceResolverOnlyOnUseOpcodes(t *testing.T) {
	service := &Service{}
	if resolver := service.alignedStackableUseProvenanceResolverForCommand(dnfenum.CmdPacketMoveItemspace); resolver != nil {
		t.Fatal("resolver must not be armed for op19")
	}
	if resolver := service.alignedStackableUseProvenanceResolverForCommand(dnfenum.CmdPacketDeleteItem); resolver != nil {
		t.Fatal("resolver must not be armed for op18")
	}
}

// TestRealPVFStackableUseProvenanceResolution 用真档 Script.pvf 复核解析器给出的
// **运行时 PVF 身份**（item id -> stackable.lst -> .stk 的 [stackable type]）。
//
// 这是第一层根因修复的证据来源：判据不再只读库存行的 Extra["stackable_type"]，
// 所以「元数据缺键、但 PVF 明确是 [waste]」的行（线上 141 行有 pvf_path 的实例，
// 含 10007484 她的心意）不会被误拒。
//
// 只读，且只在 DNFBRIDGE_REAL_PVF_SMOKE 指向真档时运行。
func TestRealPVFStackableUseProvenanceResolution(t *testing.T) {
	pvfPath := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to verify the use-stackable provenance resolver")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: pvfPath})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		itemID    int64
		wantPath  string
		wantToken string
	}{
		// 线上回归实例：她的心意，PVF 就是 [waste]（dnf90.out 拒收 2 次的那一件）。
		{10007484, "stackable/ect/chn_random/chn_20150115_randombox_item/chn_20150115_heartofher_10007484.stk", "[waste]"},
		// 已知能用的对照件（线上 50 条 op44 成功样本全在这一族）。
		{2660671, "stackable/cash/hands_remy_shine.stk", "[waste]"},
		// 前缀 bug 两族。
		{490700370, "stackable/cash/chn_490700370.stk", "[unlimited waste]"},
		{51, "stackable/cash/owl_unlimited.stk", "[unlimited waste]"},
		{6516, "stackable/fp/towercompptn_health.stk", "[waste instant item]"},
		// 机制混合容器族（本轮判定不适用，见报告逐族表）。
		{2600025, "stackable/professional/potion/secret_reinforcement.stk", "[expert town potion]"},
		// 第一轮已修的族（解析器给出的身份必须与那一轮的 PVF 取证一致）。
		{490006368, "stackable/490006001/chn_490006368.stk", "[item upgrad buff usual upgrade]"},
	}
	for _, tc := range cases {
		resolution, err := resolveCurrentStackableUseProvenance(catalog, tc.itemID)
		if err != nil {
			t.Fatalf("resolve item=%d: %v", tc.itemID, err)
		}
		if !resolution.Found || resolution.ItemID != tc.itemID {
			t.Fatalf("item=%d resolution = %+v", tc.itemID, resolution)
		}
		if resolution.PVFPath != tc.wantPath {
			t.Fatalf("item=%d path = %q, want %q", tc.itemID, resolution.PVFPath, tc.wantPath)
		}
		if !strings.Contains(resolution.StackableType, tc.wantToken) {
			t.Fatalf("item=%d stackable type = %q, want it to contain %q", tc.itemID, resolution.StackableType, tc.wantToken)
		}
	}

	// "没有答案"必须原样报出，绝不翻译成"拒绝"。
	for _, itemID := range []int64{0, -1, 999999999, 35381 /* 装备，不是 stackable */} {
		resolution, err := resolveCurrentStackableUseProvenance(catalog, itemID)
		if err != nil {
			t.Fatalf("item=%d must not error: %v", itemID, err)
		}
		if resolution.Found {
			t.Fatalf("item=%d must not resolve: %+v", itemID, resolution)
		}
	}
}
