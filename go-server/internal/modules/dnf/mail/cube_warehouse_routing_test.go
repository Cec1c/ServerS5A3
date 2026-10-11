package mail

import (
	"context"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

// 本文件钉住"小晶块/灵魂一律进晶块仓库"在**邮件领取**这条路径上的行为。
//
// 现场（2026-09-28 18:38，packet_log.txt 196757/196766/196770）：GM 面板发
// "无色小晶块"邮件 ⇒ 玩家领 op95 ⇒ 服务端按普通 [material] 把 3037 写进主背包
// 材料页（op14 row slot=130）⇒ 客户端 1.30 s 后 connection reset。
// 修法见 dnfbridge/cube_warehouse_routing.go。

type warehouseCreditCall struct {
	slot   int16
	itemID uint32
	count  int64
	extra  map[string]string
}

func cubeWarehouseMailFixtures(t *testing.T, itemID int64, count int64) (context.Context, dnfrepo.Group, uint32) {
	t.Helper()
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	mustSaveCharacter(t, repos, dnfrepo.CharacterRecord{
		CharacterID: "77",
		AccountID:   "acc-a",
		Name:        "receiver",
		Stats:       map[string]int64{},
	})
	if err := repos.Mailbox.Save(ctx, dnfrepo.MailboxRecord{
		CharacterID: "77",
		Mails: map[string]dnfrepo.MailRecord{
			"123": {
				MailID:      "123",
				Attachments: []dnfrepo.MailAttachment{{ItemID: itemID, Count: count}},
			},
		},
	}); err != nil {
		t.Fatalf("save mailbox: %v", err)
	}
	return ctx, repos, 123
}

// TestMailboxClaimRoutesCubeToAccountWarehouseWithoutBagRow 是修复的正向夹具：
// 解析结果标了 AccountWarehouseSlot ⇒ 背包一个字节都不写、固定格入账一次、
// 刷新行就是 (list 0, 358)。
func TestMailboxClaimRoutesCubeToAccountWarehouseWithoutBagRow(t *testing.T) {
	ctx, repos, mailID := cubeWarehouseMailFixtures(t, 3037, 9999)
	calls := make([]warehouseCreditCall, 0, 1)
	resolver := func(itemID uint32) (alignedcmd.MailboxItemResolution, error) {
		return alignedcmd.MailboxItemResolution{
			PVFPath:              "stackable/material/cubepiece_clear.stk",
			Kind:                 "stackable",
			SlotStart:            121,
			SlotEnd:              176,
			StackLimit:           9999,
			AccountWarehouseSlot: 358,
			DefaultExtra: map[string]string{
				"item_kind":      "stackable",
				"pvf_path":       "stackable/material/cubepiece_clear.stk",
				"stackable_type": "[material]",
			},
		}, nil
	}
	creditor := func(_ context.Context, slot int16, itemID uint32, count int64, extra map[string]string) (int64, error) {
		calls = append(calls, warehouseCreditCall{slot: slot, itemID: itemID, count: count, extra: extra})
		return 1000 + count, nil
	}
	request := alignedcmd.Request{
		Opcode:              uint16(dnfenum.CmdPacketMailboxExtractItem),
		SelectedCharacterID: 77,
		Repositories:        repos,
		MailboxItemResolver: resolver,
		WarehouseCreditor:   creditor,
		Body:                currentMailIDList(mailID),
	}
	got, err := NewHandler().Handle(ctx, request)
	if err != nil {
		t.Fatalf("Handle error = %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("warehouse credits = %+v, want exactly one", calls)
	}
	if calls[0].slot != 358 || calls[0].itemID != 3037 || calls[0].count != 9999 {
		t.Fatalf("warehouse credit = %+v, want slot 358 item 3037 count 9999", calls[0])
	}
	if calls[0].extra["pvf_path"] != "stackable/material/cubepiece_clear.stk" {
		t.Fatalf("warehouse credit extra = %+v, want the claim projection identity", calls[0].extra)
	}
	inventory, ok, err := repos.Inventory.Load(ctx, "77")
	if err != nil {
		t.Fatalf("load inventory error=%v", err)
	}
	if ok && len(inventory.Slots) != 0 {
		t.Fatalf("inventory slots = %+v, want no bag row for a warehouse-routed cube", inventory.Slots)
	}
	if len(got.ItemSlotRefreshes) != 1 ||
		got.ItemSlotRefreshes[0] != (alignedcmd.ItemSlotRefresh{ListType: 0, SlotIndex: 358}) {
		t.Fatalf("item slot refreshes = %+v, want the crystal warehouse slot 0:358", got.ItemSlotRefreshes)
	}
	if len(got.PostActions) != 0 {
		t.Fatalf("post actions = %+v, want none", got.PostActions)
	}
}

// TestMailboxClaimKeepsCubeInBagWithoutWarehouseCreditor 是反向夹具（逐字节回退）：
// 桥接层没注入闭包（= 开关回退档）时，标了 AccountWarehouseSlot 的附件照旧入包，
// 与改动前完全一致。
func TestMailboxClaimKeepsCubeInBagWithoutWarehouseCreditor(t *testing.T) {
	ctx, repos, mailID := cubeWarehouseMailFixtures(t, 3037, 9999)
	resolver := func(itemID uint32) (alignedcmd.MailboxItemResolution, error) {
		return alignedcmd.MailboxItemResolution{
			PVFPath:              "stackable/material/cubepiece_clear.stk",
			Kind:                 "stackable",
			SlotStart:            121,
			SlotEnd:              176,
			StackLimit:           9999,
			AccountWarehouseSlot: 358,
		}, nil
	}
	request := alignedcmd.Request{
		Opcode:              uint16(dnfenum.CmdPacketMailboxExtractItem),
		SelectedCharacterID: 77,
		Repositories:        repos,
		MailboxItemResolver: resolver,
		Body:                currentMailIDList(mailID),
	}
	if _, err := NewHandler().Handle(ctx, request); err != nil {
		t.Fatalf("Handle error = %v", err)
	}
	inventory, ok, err := repos.Inventory.Load(ctx, "77")
	if err != nil || !ok {
		t.Fatalf("load inventory ok=%v error=%v", ok, err)
	}
	stack, found := inventory.Slots["0:3"]
	if !found || stack.ItemID != 3037 || stack.Count != 9999 {
		t.Fatalf("inventory = %+v, want the cube still on the bag path (legacy)", inventory.Slots)
	}
}

// TestMailboxClaimKeepsCubeInBagWhenResolverDoesNotMarkIt 钉住"解析结果没标
// AccountWarehouseSlot ⇒ 与改动前逐字节相同"（这正是开关 off 档的情形：dnfbridge
// 不置位，mail 模块连分支都不进）。
func TestMailboxClaimKeepsCubeInBagWhenResolverDoesNotMarkIt(t *testing.T) {
	ctx, repos, mailID := cubeWarehouseMailFixtures(t, 3037, 12)
	calls := 0
	creditor := func(context.Context, int16, uint32, int64, map[string]string) (int64, error) {
		calls++
		return 0, nil
	}
	request := alignedcmd.Request{
		Opcode:              uint16(dnfenum.CmdPacketMailboxExtractItem),
		SelectedCharacterID: 77,
		Repositories:        repos,
		MailboxItemResolver: mailboxClaimTestResolver(121, 176, 9999),
		WarehouseCreditor:   creditor,
		Body:                currentMailIDList(mailID),
	}
	if _, err := NewHandler().Handle(ctx, request); err != nil {
		t.Fatalf("Handle error = %v", err)
	}
	if calls != 0 {
		t.Fatalf("warehouse credits = %d, want 0 when the resolver does not mark the slot", calls)
	}
	inventory, ok, err := repos.Inventory.Load(ctx, "77")
	if err != nil || !ok {
		t.Fatalf("load inventory ok=%v error=%v", ok, err)
	}
	if stack := inventory.Slots["0:3"]; stack.ItemID != 3037 || stack.Count != 12 {
		t.Fatalf("inventory = %+v, want the legacy bag placement", inventory.Slots)
	}
}

// TestMailboxClaimRejectsCubeSlotThatDisagreesWithTheMapping：解析器给的格与
// dnfrepo 的唯一映射不一致时 fail closed（来源不得被消耗）。
func TestMailboxClaimRejectsCubeSlotThatDisagreesWithTheMapping(t *testing.T) {
	ctx, repos, mailID := cubeWarehouseMailFixtures(t, 3037, 5)
	resolver := func(itemID uint32) (alignedcmd.MailboxItemResolution, error) {
		return alignedcmd.MailboxItemResolution{
			SlotStart:            121,
			SlotEnd:              176,
			StackLimit:           9999,
			AccountWarehouseSlot: 357, // 357 是蓝色小晶块，不是无色
		}, nil
	}
	creditor := func(context.Context, int16, uint32, int64, map[string]string) (int64, error) {
		t.Fatal("creditor must not be called for a mismatched slot")
		return 0, nil
	}
	request := alignedcmd.Request{
		Opcode:              uint16(dnfenum.CmdPacketMailboxExtractItem),
		SelectedCharacterID: 77,
		Repositories:        repos,
		MailboxItemResolver: resolver,
		WarehouseCreditor:   creditor,
		Body:                currentMailIDList(mailID),
	}
	got, err := NewHandler().Handle(ctx, request)
	if err != nil {
		t.Fatalf("Handle error = %v", err)
	}
	assertMailboxExtractFailure(t, got)
	box, ok, err := repos.Mailbox.Load(ctx, "77")
	if err != nil || !ok {
		t.Fatalf("load mailbox ok=%v error=%v", ok, err)
	}
	if box.Mails["123"].Claimed {
		t.Fatal("mail must stay claimable after a failed warehouse routing")
	}
	inventory, ok, err := repos.Inventory.Load(ctx, "77")
	if err != nil {
		t.Fatalf("load inventory error=%v", err)
	}
	if ok && len(inventory.Slots) != 0 {
		t.Fatalf("inventory = %+v, want zero writes on rejection", inventory.Slots)
	}
}

// assertMailboxExtractFailure 断言 op95 回的是失败体（本仓既有口径：错误码体）。
func assertMailboxExtractFailure(t *testing.T, got alignedcmd.Result) {
	t.Helper()
	if len(got.ItemSlotRefreshes) != 0 {
		t.Fatalf("item slot refreshes = %+v, want none on rejection", got.ItemSlotRefreshes)
	}
	if len(got.UpperResponses) == 0 {
		t.Fatal("rejection must still answer op95")
	}
	body := got.UpperResponses[0].Body
	if len(body) == 0 || body[0] == 1 {
		t.Fatalf("op95 body = %x, want a failure body", body)
	}
}
