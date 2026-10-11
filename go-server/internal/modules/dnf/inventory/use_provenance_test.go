package inventory

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// 第二轮（可用性判据读运行时 PVF + 前缀 bug 两族）的判据与证伪。
// 语义依据见 use_upgrad_buff.go / use_limited_waste.go 顶部注释。
//
// 线上现场（dnf90.out，本轮要修的那一类）：
//
//	msg="dnfbridge use-stackable rejected; sent failure ack" ... item_code=10007484
//	error_code=13 reason="... use stackable requires PVF-proven [waste] provenance ..."
//
// 而 10007484 在真档 Script.pvf 里**就是** `[waste]`：
//
//	stackable/ect/chn_random/chn_20150115_randombox_item/chn_20150115_heartofher_10007484.stk
//	[stackable type] `[waste]` 0
//
// 它被拒的原因是库存行缺 `stackable_type` 元数据（线上 215 行、其中 141 行有
// pvf_path 但缺该键），不是道具本身有什么问题。

const (
	heartOfHerItemID    int64 = 10007484
	heartOfHerPVFPath         = "stackable/ect/chn_random/chn_20150115_randombox_item/chn_20150115_heartofher_10007484.stk"
	heartOfHerStackType       = "[waste] 0"
)

// stackableUseProvenanceTestResolver 是 dnfbridge 解析器的同形替身：
// 只对登记过的 item id 给答案，其它一律 Found=false（= "PVF 没有答案"）。
func stackableUseProvenanceTestResolver(entries map[int64]alignedcmd.StackableUseProvenanceResolution) alignedcmd.StackableUseProvenanceResolver {
	return func(itemID int64) (alignedcmd.StackableUseProvenanceResolution, error) {
		resolution, ok := entries[itemID]
		if !ok {
			return alignedcmd.StackableUseProvenanceResolution{}, nil
		}
		return resolution, nil
	}
}

func heartOfHerStack() dnfrepo.ItemStack {
	return dnfrepo.ItemStack{
		ItemID: heartOfHerItemID,
		Count:  5690,
		Extra: map[string]string{
			"item_kind": "stackable",
			"pvf_path":  heartOfHerPVFPath,
			// 关键：**没有** stackable_type（线上 215 行就是这个形状）。
		},
	}
}

// TestStackableUseProvenanceReadsRuntimePVFWhenMetadataMissing 是根因修复的单元判据：
// 元数据缺 stackable_type 时，运行时 PVF 的 `[waste]` 仍然能证明它可用。
func TestStackableUseProvenanceReadsRuntimePVFWhenMetadataMissing(t *testing.T) {
	t.Setenv("DNFBRIDGE_ITEM_USE_PVF_PROVENANCE", "")
	resolver := stackableUseProvenanceTestResolver(map[int64]alignedcmd.StackableUseProvenanceResolution{
		heartOfHerItemID: {
			ItemID:        heartOfHerItemID,
			PVFPath:       heartOfHerPVFPath,
			StackableType: heartOfHerStackType,
			Found:         true,
		},
	})
	path, stackableType, ok := stackableUseProvenance(heartOfHerStack(), resolver)
	if !ok {
		t.Fatal("runtime-PVF-proven [waste] stack must pass even without stackable_type metadata")
	}
	if path != heartOfHerPVFPath || stackableType != "[waste]" {
		t.Fatalf("path/type = %q/%q", path, stackableType)
	}
}

// TestStackableUseProvenanceLegacySwitchRestoresMetadataOnlyContract 是同一用例的
// 证伪对照组：回退开关打开时，这条行重新被拒（复现线上 error_code=13 的那条判据）。
func TestStackableUseProvenanceLegacySwitchRestoresMetadataOnlyContract(t *testing.T) {
	t.Setenv("DNFBRIDGE_ITEM_USE_PVF_PROVENANCE", "legacy")
	resolver := stackableUseProvenanceTestResolver(map[int64]alignedcmd.StackableUseProvenanceResolution{
		heartOfHerItemID: {
			ItemID:        heartOfHerItemID,
			PVFPath:       heartOfHerPVFPath,
			StackableType: heartOfHerStackType,
			Found:         true,
		},
	})
	if _, _, ok := stackableUseProvenance(heartOfHerStack(), resolver); ok {
		t.Fatal("legacy mode must not read the runtime PVF")
	}
}

// TestStackableUseProvenanceFallsBackToMetadataWhenPVFHasNoAnswer 覆盖三种"PVF 没有答案"
// 的形态：解析器为零 / Found=false / 报错——都必须逐字节回到改动前的元数据判据。
func TestStackableUseProvenanceFallsBackToMetadataWhenPVFHasNoAnswer(t *testing.T) {
	t.Setenv("DNFBRIDGE_ITEM_USE_PVF_PROVENANCE", "")
	metadataProven := func() dnfrepo.ItemStack {
		stack := heartOfHerStack()
		stack.Extra["stackable_type"] = heartOfHerStackType
		return stack
	}
	failing := func(int64) (alignedcmd.StackableUseProvenanceResolution, error) {
		return alignedcmd.StackableUseProvenanceResolution{}, errors.New("PVF unavailable")
	}
	unresolved := stackableUseProvenanceTestResolver(nil)

	for name, resolver := range map[string]alignedcmd.StackableUseProvenanceResolver{
		"nil resolver":           nil,
		"error resolver":         failing,
		"unresolved item":        unresolved,
		"found without type":     stackableUseProvenanceTestResolver(map[int64]alignedcmd.StackableUseProvenanceResolution{heartOfHerItemID: {ItemID: heartOfHerItemID, PVFPath: heartOfHerPVFPath, Found: true}}),
		"resolved other item":    stackableUseProvenanceTestResolver(map[int64]alignedcmd.StackableUseProvenanceResolution{heartOfHerItemID: {ItemID: 42, PVFPath: heartOfHerPVFPath, StackableType: "[waste]", Found: true}}),
		"resolved empty path":    stackableUseProvenanceTestResolver(map[int64]alignedcmd.StackableUseProvenanceResolution{heartOfHerItemID: {ItemID: heartOfHerItemID, StackableType: "[waste]", Found: true}}),
		"resolved empty type":    stackableUseProvenanceTestResolver(map[int64]alignedcmd.StackableUseProvenanceResolution{heartOfHerItemID: {ItemID: heartOfHerItemID, PVFPath: heartOfHerPVFPath, StackableType: "   ", Found: true}}),
		"metadata-only resolver": nil,
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, ok := stackableUseProvenance(metadataProven(), resolver); !ok {
				t.Fatal("metadata-proven [waste] stack must still pass when the runtime PVF has no answer")
			}
			// 同一条判据下，缺元数据又缺 PVF 的行仍然是 fail-closed。
			if _, _, ok := stackableUseProvenance(heartOfHerStack(), resolver); ok {
				t.Fatal("stack without any evidence must stay rejected")
			}
		})
	}
}

// TestStackableUseProvenanceRejectsWhenRuntimePVFSaysNotConsumable：
// 运行时 PVF 是权威——它说这一族不是可右键使用的消耗品就不放行（证据更强，同为 fail-closed）。
func TestStackableUseProvenanceRejectsWhenRuntimePVFSaysNotConsumable(t *testing.T) {
	t.Setenv("DNFBRIDGE_ITEM_USE_PVF_PROVENANCE", "")
	stack := heartOfHerStack()
	stack.Extra["stackable_type"] = heartOfHerStackType // 元数据说 [waste]，PVF 说 [material]
	resolver := stackableUseProvenanceTestResolver(map[int64]alignedcmd.StackableUseProvenanceResolution{
		heartOfHerItemID: {ItemID: heartOfHerItemID, PVFPath: "stackable/material/piece_iron.stk", StackableType: "[material]", Found: true},
	})
	if _, _, ok := stackableUseProvenance(stack, resolver); ok {
		t.Fatal("runtime PVF is authoritative: [material] must not pass the op44 use contract")
	}
}

// TestPrefixFamiliesAreGatedByTheirOwnSwitches 锁死"一个族一个开关"：
// 每个回退字面量只影响自己那一族，[waste] 与其它族不受牵连。
func TestPrefixFamiliesAreGatedByTheirOwnSwitches(t *testing.T) {
	t.Setenv("DNFBRIDGE_ITEM_UPGRAD_BUFF_USE", "")
	t.Setenv("DNFBRIDGE_ITEM_USE_PVF_PROVENANCE", "")
	reset := func() {
		t.Setenv("DNFBRIDGE_ITEM_UNLIMITED_WASTE_USE", "")
		t.Setenv("DNFBRIDGE_ITEM_WASTE_INSTANT_USE", "")
	}

	reset()
	for _, tc := range []struct {
		stackableType string
		want          bool
	}{
		{"[waste]", true},
		{"[waste] 4", true},
		{"`[waste]` 4", true},
		{"[unlimited waste]", true},
		{"[unlimited waste] 1", true},
		{"`[unlimited waste]` 0", true},
		{"[waste instant item]", true},
		{"[waste instant item] 1", true},
		{"[expert town potion]", false},
		{"[disguise]", false},
		{"[throw]", true},
		{"[dye]", false},
		{"[coupon]", false},
		{"x [waste] 1", false},
		{"", false},
	} {
		if got := stackableTypeIsPVFProvenUseConsumable(tc.stackableType); got != tc.want {
			t.Fatalf("default: %q = %t, want %t", tc.stackableType, got, tc.want)
		}
	}

	t.Run("unlimited waste rollback", func(t *testing.T) {
		reset()
		t.Setenv("DNFBRIDGE_ITEM_UNLIMITED_WASTE_USE", "legacy")
		if stackableTypeIsPVFProvenUseConsumable("[unlimited waste] 1") {
			t.Fatal("legacy unlimited-waste switch must reject the family")
		}
		if !stackableTypeIsPVFProvenUseConsumable("[waste] 4") || !stackableTypeIsPVFProvenUseConsumable("[waste instant item] 1") {
			t.Fatal("rolling back one family must not touch [waste] or [waste instant item]")
		}
		if !stackableTypeSkipsSourceConsumption("[unlimited waste] 1") == false {
			t.Fatal("rollback must also disable the no-consume branch")
		}
	})
	t.Run("waste instant item rollback", func(t *testing.T) {
		reset()
		t.Setenv("DNFBRIDGE_ITEM_WASTE_INSTANT_USE", "off")
		if stackableTypeIsPVFProvenUseConsumable("[waste instant item] 1") {
			t.Fatal("legacy waste-instant-item switch must reject the family")
		}
		if !stackableTypeIsPVFProvenUseConsumable("[unlimited waste] 1") {
			t.Fatal("rolling back one family must not touch the other")
		}
	})
	t.Run("consume semantics per family", func(t *testing.T) {
		reset()
		if !stackableTypeSkipsSourceConsumption("[unlimited waste] 1") {
			t.Fatal("[unlimited waste] must not consume the source stack")
		}
		if stackableTypeSkipsSourceConsumption("[waste] 4") || stackableTypeSkipsSourceConsumption("[waste instant item] 1") {
			t.Fatal("only [unlimited waste] skips consumption")
		}
	})
}

// TestHandlerUseStackableMetadataMissingWastePotionAppliesExactAck 是线上实例
// 10007484 她的心意 的端到端回归：同一个 op44 请求，改动前 owner 落库失败
// （bridge 只能回 error_code=13），改动后扣 1 件并回与 [waste] 同形的成功 ACK。
func TestHandlerUseStackableMetadataMissingWastePotionAppliesExactAck(t *testing.T) {
	t.Setenv("DNFBRIDGE_ITEM_USE_PVF_PROVENANCE", "")
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	stack := heartOfHerStack()
	stack.Count = 2
	saveTestInventory(t, ctx, repos, dnfrepo.InventoryRecord{
		CharacterID: "6",
		Slots:       map[string]dnfrepo.ItemStack{slotKey(listTypeMain, 5): stack},
	})

	got, err := NewHandler().Handle(ctx, alignedcmd.Request{
		Opcode: uint16(dnfenum.CmdPacketUseStackable),
		Body: []byte{
			0x05, 0x00,
			listTypeMain,
			0x11, 0x22, 0x00, 0x00,
			0xBC, 0xB3, 0x98, 0x00, // 10007484 = 0x0098B3BC
			0x00, 0x00, 0x00, 0x00,
		},
		AccountID:           "acc",
		SelectedCharacterID: 6,
		Repositories:        repos,
		StackableUseProvenanceResolver: stackableUseProvenanceTestResolver(map[int64]alignedcmd.StackableUseProvenanceResolution{
			heartOfHerItemID: {ItemID: heartOfHerItemID, PVFPath: heartOfHerPVFPath, StackableType: heartOfHerStackType, Found: true},
		}),
	})
	if err != nil {
		t.Fatalf("Handle error = %v", err)
	}
	if !got.Handled || !got.ResponseAllowed || len(got.UpperResponses) != 1 {
		t.Fatalf("result = %+v", got)
	}
	wantBody := []byte{1, 5, 0, listTypeMain, 0x11, 0x22, 0x00, 0x00, 0xBC, 0xB3, 0x98, 0x00}
	if response := got.UpperResponses[0]; string(response.Body) != string(wantBody) {
		t.Fatalf("response body = %x want = %x", response.Body, wantBody)
	}
	loaded := loadTestInventory(t, ctx, repos, "6")
	if stack := loaded.Slots[slotKey(listTypeMain, 5)]; stack.Count != 1 {
		t.Fatalf("source stack count = %d, want 1", stack.Count)
	}
}

// TestHandlerUseStackableUnlimitedWasteKeepsSourceCount 锁死 [unlimited waste] 的
// "用而不耗"：成功 ACK 照发，但源行数量与库里的行都原样不动。
func TestHandlerUseStackableUnlimitedWasteKeepsSourceCount(t *testing.T) {
	t.Setenv("DNFBRIDGE_ITEM_UNLIMITED_WASTE_USE", "")
	t.Setenv("DNFBRIDGE_ITEM_USE_PVF_PROVENANCE", "")
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	saveTestInventory(t, ctx, repos, dnfrepo.InventoryRecord{
		CharacterID: "6",
		Slots: map[string]dnfrepo.ItemStack{
			slotKey(listTypeMain, 12): {
				ItemID: 490700370,
				Count:  5,
				Extra: map[string]string{
					"item_kind":      "stackable",
					"pvf_path":       "stackable/cash/chn_490700370.stk",
					"stackable_type": "[unlimited waste] 1",
				},
			},
		},
	})
	got, err := NewHandler().Handle(ctx, alignedcmd.Request{
		Opcode: uint16(dnfenum.CmdPacketUseStackable),
		Body: []byte{
			0x0C, 0x00,
			listTypeMain,
			0x33, 0x44, 0x00, 0x00,
			0x52, 0x7E, 0x3F, 0x1D, // 490700370 = 0x1D3F7E52
			0x00, 0x00, 0x00, 0x00,
		},
		AccountID:           "acc",
		SelectedCharacterID: 6,
		Repositories:        repos,
	})
	if err != nil {
		t.Fatalf("Handle error = %v", err)
	}
	if !got.Handled || !got.ResponseAllowed || len(got.UpperResponses) != 1 {
		t.Fatalf("result = %+v", got)
	}
	if !strings.Contains(got.Reason, "unlimited_use=true") || !strings.Contains(got.Reason, "changed=false") {
		t.Fatalf("reason must record the unlimited-use branch: %s", got.Reason)
	}
	loaded := loadTestInventory(t, ctx, repos, "6")
	if stack := loaded.Slots[slotKey(listTypeMain, 12)]; stack.Count != 5 {
		t.Fatalf("unlimited source stack count = %d, want 5", stack.Count)
	}
}

// TestHandlerUseStackableUnlimitedWasteLegacySwitchStaysBlocked：回退开关打开时同一请求
// 重新落回 fail-closed（对照 [unlimited waste] 的修复不是"把拒绝改成成功"）。
func TestHandlerUseStackableUnlimitedWasteLegacySwitchStaysBlocked(t *testing.T) {
	t.Setenv("DNFBRIDGE_ITEM_UNLIMITED_WASTE_USE", "legacy")
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	saveTestInventory(t, ctx, repos, dnfrepo.InventoryRecord{
		CharacterID: "6",
		Slots: map[string]dnfrepo.ItemStack{
			slotKey(listTypeMain, 12): {
				ItemID: 490700370,
				Count:  5,
				Extra: map[string]string{
					"item_kind":      "stackable",
					"pvf_path":       "stackable/cash/chn_490700370.stk",
					"stackable_type": "[unlimited waste] 1",
				},
			},
		},
	})
	got, err := NewHandler().Handle(ctx, alignedcmd.Request{
		Opcode: uint16(dnfenum.CmdPacketUseStackable),
		Body: []byte{
			0x0C, 0x00,
			listTypeMain,
			0x33, 0x44, 0x00, 0x00,
			0x52, 0x7E, 0x3F, 0x1D,
			0x00, 0x00, 0x00, 0x00,
		},
		AccountID:           "acc",
		SelectedCharacterID: 6,
		Repositories:        repos,
	})
	if err != nil {
		t.Fatalf("Handle error = %v", err)
	}
	if got.ResponseAllowed || len(got.UpperResponses) != 0 {
		t.Fatalf("legacy result must stay blocked: %+v", got)
	}
	loaded := loadTestInventory(t, ctx, repos, "6")
	if stack := loaded.Slots[slotKey(listTypeMain, 12)]; stack.Count != 5 {
		t.Fatalf("legacy mode mutated the stack: %+v", stack)
	}
}

// TestHandlerUseStackableWasteInstantItemDecrements：[waste instant item] 是普通药水形状
// （扣数是正确的），端到端确认它真的扣 1 而不是走"用而不耗"。
func TestHandlerUseStackableWasteInstantItemDecrements(t *testing.T) {
	t.Setenv("DNFBRIDGE_ITEM_WASTE_INSTANT_USE", "")
	t.Setenv("DNFBRIDGE_ITEM_USE_PVF_PROVENANCE", "")
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	saveTestInventory(t, ctx, repos, dnfrepo.InventoryRecord{
		CharacterID: "6",
		Slots: map[string]dnfrepo.ItemStack{
			slotKey(listTypeMain, 20): {
				ItemID: 6516,
				Count:  3,
				Extra: map[string]string{
					"item_kind":      "stackable",
					"pvf_path":       "stackable/fp/towercompptn_health.stk",
					"stackable_type": "[waste instant item] 1",
				},
			},
		},
	})
	got, err := NewHandler().Handle(ctx, alignedcmd.Request{
		Opcode: uint16(dnfenum.CmdPacketUseStackable),
		Body: []byte{
			0x14, 0x00,
			listTypeMain,
			0x55, 0x66, 0x00, 0x00,
			0x74, 0x19, 0x00, 0x00, // 6516 = 0x1974
			0x00, 0x00, 0x00, 0x00,
		},
		AccountID:           "acc",
		SelectedCharacterID: 6,
		Repositories:        repos,
	})
	if err != nil {
		t.Fatalf("Handle error = %v", err)
	}
	if !got.Handled || !got.ResponseAllowed || len(got.UpperResponses) != 1 {
		t.Fatalf("result = %+v", got)
	}
	if strings.Contains(got.Reason, "unlimited_use=true") {
		t.Fatalf("[waste instant item] must consume: %s", got.Reason)
	}
	loaded := loadTestInventory(t, ctx, repos, "6")
	if stack := loaded.Slots[slotKey(listTypeMain, 20)]; stack.Count != 2 {
		t.Fatalf("instant-item stack count = %d, want 2", stack.Count)
	}
}

// TestRealPVFStackableUseProvenanceSmoke 用真档 Script.pvf 复核本轮的每个 PVF 结论：
// 类型串、[unlimited waste] 与 [waste] 的"同一类消耗品"对照、以及 [expert town potion]
// 不被放行。只在 DNFBRIDGE_REAL_PVF_SMOKE 指向真档时运行，且只读。
func TestRealPVFStackableUseProvenanceSmoke(t *testing.T) {
	pvfPath := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("DNFBRIDGE_REAL_PVF_SMOKE not set")
	}
	archive, err := platformpvf.Open(pvfPath)
	if err != nil {
		t.Fatalf("open pvf: %v", err)
	}
	read := func(path string) *dnfpvf.Document {
		text, err := archive.ReadText(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		document, err := dnfpvf.Parse(path, text)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		return document
	}

	// 1) 线上回归实例：10007484 她的心意 在真档里就是 [waste]。
	heartOfHer := read(heartOfHerPVFPath)
	if got, _ := heartOfHer.Text("stackable type"); stackableTypeToken(got) != "[waste]" {
		t.Fatalf("%s stackable type = %q, want [waste]", heartOfHerPVFPath, got)
	}

	// 2) 前缀 bug 两族在真档里的类型串。
	unlimited := read("stackable/cash/chn_490700370.stk")
	if got, _ := unlimited.Text("stackable type"); stackableTypeToken(got) != currentUnlimitedWasteToken {
		t.Fatalf("chn_490700370 stackable type = %q", got)
	}
	instant := read("stackable/fp/towercompptn_health.stk")
	if got, _ := instant.Text("stackable type"); stackableTypeToken(got) != currentWasteInstantItemToken {
		t.Fatalf("towercompptn_health stackable type = %q", got)
	}

	// 3) [unlimited waste] 与 [waste] 是"同一类消耗品"的字节级对照：
	//    同名道具（闪亮的雷米援助）的效果块逐字相同，只有类型记号不同。
	wasteTwin := read("stackable/cash/hands_remy_shine.stk")
	if got, _ := wasteTwin.Text("stackable type"); stackableTypeToken(got) != "[waste]" {
		t.Fatalf("hands_remy_shine stackable type = %q", got)
	}
	for _, section := range []string{"name", "hp recovery", "mp recovery", "active status recovery", "item group name", "move wav"} {
		left, leftOK := wasteTwin.Text(section)
		right, rightOK := unlimited.Text(section)
		if !leftOK || !rightOK || left != right {
			t.Fatalf("%s differs: [waste]=%q(%t) [unlimited waste]=%q(%t)", section, left, leftOK, right, rightOK)
		}
	}

	// 4) "unlimited" = 使用后不会被消耗：族内 [explain] 原文。
	keepsItem := 0
	for _, path := range []string{
		"stackable/cash/chn_20151217_fast_growup_newbie_package/chn_2015_newbie_recovery_potion_10008480.stk",
		"stackable/10301001/10301306.stk",
		"stackable/490006001/chn_490006989.stk",
		"stackable/cash/owl_unlimited.stk",
	} {
		explain, _ := read(path).Text("explain")
		if !strings.Contains(explain, "不会被消耗") && !strings.Contains(explain, "不消耗") && !strings.Contains(explain, "永久使用") {
			t.Fatalf("%s explain does not state the item is kept: %q", path, explain)
		}
		keepsItem++
	}
	if keepsItem == 0 {
		t.Fatal("no [unlimited waste] explain examined")
	}

	// 5) [expert town potion] 不在放行集合里（该族是机制混合容器，见报告逐族表）。
	expert := read("stackable/professional/potion/secret_reinforcement.stk")
	expertType, _ := expert.Text("stackable type")
	if stackableTypeToken(expertType) != "[expert town potion]" {
		t.Fatalf("secret_reinforcement stackable type = %q", expertType)
	}
	if stackableTypeIsPVFProvenUseConsumable(expertType) {
		t.Fatalf("%q must stay outside the op44 accept set", expertType)
	}
	if stackableTypeSkipsSourceConsumption(expertType) {
		t.Fatalf("%q must not enter the no-consume branch", expertType)
	}
}
