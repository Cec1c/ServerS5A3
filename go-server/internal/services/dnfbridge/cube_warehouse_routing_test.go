package dnfbridge

import (
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// 本文件钉住"不管如何获得小晶块都进入晶块仓库"这条规则的三块地基：
//
//	1. item→固定格 的**唯一映射**（dnfrepo）与 disjoint 路径既有的映射表逐项一致；
//	2. 开关 DNFBRIDGE_CUBE_WAREHOUSE_ROUTING 的归一化（默认 = 修复档）；
//	3. 批量分解聚合行的合并口径（数量相加、行序 = 槽升序）。

// TestCubeWarehouseMappingMatchesTheDisjointFixedSlotTable 是"唯一真源"的对拍：
// 任何一边改动而另一边没跟，这条用例立刻变红。
func TestCubeWarehouseMappingMatchesTheDisjointFixedSlotTable(t *testing.T) {
	if len(currentDisjointWarehouseFixedSlots) == 0 {
		t.Fatal("disjoint fixed-slot table is empty")
	}
	for itemID, slot := range currentDisjointWarehouseFixedSlots {
		got, ok := dnfrepo.AccountWarehouseSlotForItem(int64(itemID))
		if !ok {
			t.Fatalf("item %d is a fixed-slot warehouse item for disjoint but not for dnfrepo", itemID)
		}
		if got != slot {
			t.Fatalf("item %d: dnfrepo slot=%d disjoint slot=%d", itemID, got, slot)
		}
		if back, ok := dnfrepo.AccountWarehouseItemForSlot(slot); !ok || back != int64(itemID) {
			t.Fatalf("slot %d reverse lookup=%d ok=%v want %d", slot, back, ok, itemID)
		}
		if !dnfrepo.IsAccountSharedInventorySlot(dnfrepo.MainInventoryListType, slot) {
			t.Fatalf("slot %d is outside the account shared range", slot)
		}
	}
	// 现场那件道具（无色小晶块）必须落在 358 —— 与线上 DB `dnf_account_inventory_items`
	// 的 `dnf:10001 0:358 3037` 行一致。
	if slot, ok := dnfrepo.AccountWarehouseSlotForItem(3037); !ok || slot != 358 {
		t.Fatalf("clear cube 3037 slot=%d ok=%v want 358", slot, ok)
	}
	// 非晶块道具不得被误判成仓库道具。
	for _, itemID := range []int64{0, 1, 3166, 3167, 10300783} {
		if slot, ok := dnfrepo.AccountWarehouseSlotForItem(itemID); ok {
			t.Fatalf("item %d must not map to a warehouse slot, got %d", itemID, slot)
		}
	}
}

// TestCubeWarehouseRoutingModeNormalization：默认档 = 修复后的行为（领取期改道 +
// 登录期清扫），只有显式回退字面量才回到改动前。
func TestCubeWarehouseRoutingModeNormalization(t *testing.T) {
	for _, test := range []struct {
		value string
		want  cubeWarehouseRoutingMode
	}{
		{value: "", want: cubeWarehouseRoutingOn},
		{value: "typo", want: cubeWarehouseRoutingOn},
		{value: "on", want: cubeWarehouseRoutingOn},
		{value: "  ON ", want: cubeWarehouseRoutingOn},
		{value: "warehouse", want: cubeWarehouseRoutingOn},
		{value: "legacy", want: cubeWarehouseRoutingLegacy},
		{value: "  LEGACY ", want: cubeWarehouseRoutingLegacy},
		{value: "off", want: cubeWarehouseRoutingLegacy},
		{value: "0", want: cubeWarehouseRoutingLegacy},
		{value: "false", want: cubeWarehouseRoutingLegacy},
		{value: "disabled", want: cubeWarehouseRoutingLegacy},
	} {
		if got := readCubeWarehouseRoutingMode(test.value); got != test.want {
			t.Fatalf("mode(%q)=%v want %v", test.value, got, test.want)
		}
	}
	t.Setenv(cubeWarehouseRoutingEnv, cubeWarehouseRoutingValueOff)
	if (&Service{}).cubeWarehouseRoutingEnabled() {
		t.Fatal("回退字面量 legacy 必须关掉领取期改道与登录期清扫")
	}
	t.Setenv(cubeWarehouseRoutingEnv, "")
	if !(&Service{}).cubeWarehouseRoutingEnabled() {
		t.Fatal("未设置开关时必须是修复档")
	}
}

// TestMailboxClaimTypeRepairGateNormalization：op95 附件类型修补（默认档）与它的
// 回退字面量。
func TestMailboxClaimTypeRepairGateNormalization(t *testing.T) {
	for _, test := range []struct {
		value string
		want  bool
	}{
		{value: "", want: true},
		{value: "typo", want: true},
		{value: "on", want: true},
		{value: "legacy", want: false},
		{value: "  LEGACY ", want: false},
		{value: "off", want: false},
		{value: "0", want: false},
		{value: "false", want: false},
		{value: "disabled", want: false},
	} {
		t.Setenv(mailboxClaimTypeRepairEnv, test.value)
		if got := (&Service{}).mailboxClaimTypeRepairEnabled(); got != test.want {
			t.Fatalf("mailboxClaimTypeRepairEnabled(%q)=%v want %v", test.value, got, test.want)
		}
	}
}

// TestCurrentDisjointBatchAggregatedRewardsMergesBySlotAndItem 钉住"一个框"的数据口径：
// 同一 (槽,物品) 的本次获得量相加，行序 = 槽升序（同槽再按物品升序），
// 而 Count（堆叠终值）取最后一次看到的权威量。
func TestCurrentDisjointBatchAggregatedRewardsMergesBySlotAndItem(t *testing.T) {
	items := []currentDisjointBatchItem{
		{Slot: 60, Rewards: []currentDisjointRewardSlot{
			{Slot: 358, ItemID: 3037, Count: 14, Granted: 14},
			{Slot: 121, ItemID: 3166, Count: 1, Granted: 1},
			{Slot: 362, ItemID: 10099773, Count: 1, Granted: 1},
		}},
		{Slot: 61, Rewards: []currentDisjointRewardSlot{
			{Slot: 358, ItemID: 3037, Count: 15, Granted: 1},
			{Slot: 121, ItemID: 3166, Count: 2, Granted: 1},
			{Slot: 362, ItemID: 10099773, Count: 2, Granted: 1},
		}},
	}
	merged := currentDisjointBatchAggregatedRewards(items)
	want := []currentDisjointRewardSlot{
		{Slot: 121, ItemID: 3166, Count: 2, Granted: 2},
		{Slot: 358, ItemID: 3037, Count: 15, Granted: 15},
		{Slot: 362, ItemID: 10099773, Count: 2, Granted: 2},
	}
	if len(merged) != len(want) {
		t.Fatalf("merged=%+v want %+v", merged, want)
	}
	for index := range want {
		if merged[index] != want[index] {
			t.Fatalf("merged[%d]=%+v want %+v", index, merged[index], want[index])
		}
	}
	if got := currentDisjointBatchAggregatedRewards(nil); got != nil {
		t.Fatalf("empty batch aggregation=%+v want nil", got)
	}
}

// TestBuildCurrentDisjointBatchAggregateSuccessBodyUsesTheProvenShape 把聚合 ACK 的
// 字节形状钉死：`01 | i16 第一件 | u8 list=0 | i16 tool | u8 行数 | (u16 槽,u32 物品,u32 本次获得)`。
func TestBuildCurrentDisjointBatchAggregateSuccessBodyUsesTheProvenShape(t *testing.T) {
	batch := currentDisjointBatchRequest{ToolSlot: -1, Context: 0x13d, Slots: []int16{60, 61}}
	items := []currentDisjointBatchItem{
		{Slot: 60, Rewards: []currentDisjointRewardSlot{{Slot: 358, ItemID: 3037, Count: 14, Granted: 14}}},
		{Slot: 61, Rewards: []currentDisjointRewardSlot{{Slot: 358, ItemID: 3037, Count: 15, Granted: 1}}},
	}
	body := buildCurrentDisjointBatchAggregateSuccessBody(batch, items)
	if len(body) != 1+2+1+2+1+10 {
		t.Fatalf("aggregate body len=%d body=%x", len(body), body)
	}
	if body[0] != 1 {
		t.Fatalf("aggregate body[0]=%d want success 1", body[0])
	}
	if source := int16(uint16(body[1]) | uint16(body[2])<<8); source != 60 {
		t.Fatalf("aggregate source slot=%d want 60 (first decomposed slot)", source)
	}
	if body[3] != dnfrepo.MainInventoryListType {
		t.Fatalf("aggregate list=%d want 0", body[3])
	}
	if tool := int16(uint16(body[4]) | uint16(body[5])<<8); tool != -1 {
		t.Fatalf("aggregate tool=%d want -1", tool)
	}
	if body[6] != 1 {
		t.Fatalf("aggregate row count=%d want 1 (merged)", body[6])
	}
	if slot := int16(uint16(body[7]) | uint16(body[8])<<8); slot != 358 {
		t.Fatalf("aggregate row slot=%d want 358", slot)
	}
	if item := uint32(body[9]) | uint32(body[10])<<8 | uint32(body[11])<<16 | uint32(body[12])<<24; item != 3037 {
		t.Fatalf("aggregate row item=%d want 3037", item)
	}
	if granted := uint32(body[13]) | uint32(body[14])<<8 | uint32(body[15])<<16 | uint32(body[16])<<24; granted != 15 {
		t.Fatalf("aggregate row granted=%d want 15 (14+1)", granted)
	}
}
