package mail

import (
	"context"
	"reflect"
	"strconv"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestMailMaterialFullTabKeepsAttachmentAndGold(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	mustSaveCharacter(t, repos, dnfrepo.CharacterRecord{CharacterID: "77", Stats: map[string]int64{"gold": 10}})
	slots := map[string]dnfrepo.ItemStack{}
	for slot := 121; slot <= 152; slot++ {
		slots["0:"+strconv.Itoa(slot)] = dnfrepo.ItemStack{ItemID: 999, Count: 99}
	}
	for slot := 3; slot <= 8; slot++ {
		slots["0:"+strconv.Itoa(slot)] = dnfrepo.ItemStack{ItemID: 999, Count: 99}
	}
	if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "77", Slots: slots}); err != nil {
		t.Fatal(err)
	}
	mail := dnfrepo.MailRecord{MailID: "1", Gold: 100, Attachments: []dnfrepo.MailAttachment{{ItemID: 3175, Count: 1}}}
	if err := repos.Mailbox.Save(ctx, dnfrepo.MailboxRecord{CharacterID: "77", Mails: map[string]dnfrepo.MailRecord{"1": mail}}); err != nil {
		t.Fatal(err)
	}
	if _, err := NewHandler().Handle(ctx, alignedcmd.Request{Opcode: uint16(dnfenum.CmdPacketMailboxExtractItem), SelectedCharacterID: 77,
		Repositories: repos, MailboxItemResolver: mailboxClaimTestResolver(121, 176, 99), Body: currentMailIDList(1)}); err != nil {
		t.Fatal(err)
	}
	box, _, _ := repos.Mailbox.Load(ctx, "77")
	character, _, _ := repos.Character.Load(ctx, "77")
	inventory, _, _ := repos.Inventory.Load(ctx, "77")
	got := box.Mails["1"]
	if got.Claimed || got.Deleted || got.Gold != mail.Gold || !reflect.DeepEqual(got.Attachments, mail.Attachments) || character.Stats["gold"] != 10 || !reflect.DeepEqual(inventory.Slots, slots) {
		t.Fatal("材料页满时，邮件附件、金币或背包被改变")
	}
}

func TestMailMaterialPlacementStaysInCategory(t *testing.T) {
	resolution := alignedcmd.MailboxItemResolution{Kind: "stackable", SlotStart: 121, SlotEnd: 176, StackLimit: 99, AllowQuickSlots: true}
	t.Run("empty_quick_slots", func(t *testing.T) {
		inventory := dnfrepo.InventoryRecord{Slots: map[string]dnfrepo.ItemStack{}}
		refreshes, err := addAttachmentToInventory(&inventory, dnfrepo.MailAttachment{ItemID: 3175, Count: 2}, resolution)
		if err != nil {
			t.Fatal(err)
		}
		if len(refreshes) != 1 || refreshes[0].SlotIndex != 121 || inventory.Slots["0:121"].Count != 2 || len(inventory.Slots) != 1 {
			t.Fatalf("邮件材料必须进入材料页：refresh=%+v slots=%+v", refreshes, inventory.Slots)
		}
	})
	t.Run("merge_existing_quick_stack_before_material", func(t *testing.T) {
		inventory := dnfrepo.InventoryRecord{Slots: map[string]dnfrepo.ItemStack{"0:3": {ItemID: 3175, Count: 5}, "0:121": {ItemID: 3175, Count: 7}}}
		if _, err := addAttachmentToInventory(&inventory, dnfrepo.MailAttachment{ItemID: 3175, Count: 2}, resolution); err != nil {
			t.Fatal(err)
		}
		if inventory.Slots["0:3"].Count != 7 || inventory.Slots["0:121"].Count != 7 || len(inventory.Slots) != 2 {
			t.Fatalf("邮件材料错误合并：%+v", inventory.Slots)
		}
	})
	t.Run("full_material_tab_falls_back_to_empty_quick_slots", func(t *testing.T) {
		inventory := dnfrepo.InventoryRecord{Slots: map[string]dnfrepo.ItemStack{}}
		for slot := 121; slot <= 152; slot++ {
			inventory.Slots["0:"+strconv.Itoa(slot)] = dnfrepo.ItemStack{ItemID: 999, Count: 99}
		}
		before := dnfrepo.CloneInventory(inventory)
		refreshes, err := addAttachmentToInventory(&inventory, dnfrepo.MailAttachment{ItemID: 3175, Count: 1}, resolution)
		if err != nil || len(refreshes) != 1 || refreshes[0].SlotIndex != 3 || inventory.Slots["0:3"].Count != 1 {
			t.Fatalf("邮件材料满页未回落：%+v err=%v", refreshes, err)
		}
		delete(inventory.Slots, "0:3")
		if !reflect.DeepEqual(inventory, before) {
			t.Fatal("回落改变已有材料页")
		}
	})
}

func TestMailClaimUsesPersistedExpansionForLockedMaterialCells(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	mustSaveCharacter(t, repos, dnfrepo.CharacterRecord{CharacterID: "77", Stats: map[string]int64{"gold": 10}})
	slots := map[string]dnfrepo.ItemStack{}
	for slot := 121; slot <= 152; slot++ {
		slots["0:"+strconv.Itoa(slot)] = dnfrepo.ItemStack{ItemID: 999, Count: 99}
	}
	for slot := 3; slot <= 8; slot++ {
		slots["0:"+strconv.Itoa(slot)] = dnfrepo.ItemStack{ItemID: 999, Count: 99}
	}
	if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "77", Slots: slots}); err != nil {
		t.Fatal(err)
	}
	if err := repos.Settings.Save(ctx, dnfrepo.SettingsRecord{
		Scope:  dnfrepo.CharacterContainerStateScope("77"),
		Values: map[string]string{"main_list_param16": "8", "avatar_list_param16": "0", "personal_cargo_list_param16": "8"},
	}); err != nil {
		t.Fatal(err)
	}
	mail := dnfrepo.MailRecord{MailID: "1", Gold: 100, Attachments: []dnfrepo.MailAttachment{{ItemID: 3175, Count: 1}}}
	if err := repos.Mailbox.Save(ctx, dnfrepo.MailboxRecord{CharacterID: "77", Mails: map[string]dnfrepo.MailRecord{"1": mail}}); err != nil {
		t.Fatal(err)
	}
	if _, err := NewHandler().Handle(ctx, alignedcmd.Request{Opcode: uint16(dnfenum.CmdPacketMailboxExtractItem), SelectedCharacterID: 77,
		Repositories: repos, MailboxItemResolver: mailboxClaimTestResolver(121, 176, 99), Body: currentMailIDList(1)}); err != nil {
		t.Fatal(err)
	}
	box, _, _ := repos.Mailbox.Load(ctx, "77")
	character, _, _ := repos.Character.Load(ctx, "77")
	inventory, _, _ := repos.Inventory.Load(ctx, "77")
	if !box.Mails["1"].Claimed || character.Stats["gold"] != 110 || inventory.Slots["0:153"].ItemID != 3175 || inventory.Slots["0:153"].Count != 1 {
		t.Fatalf("已购扩容应允许附件进入新增材料格并原子领取金币: mail=%+v gold=%d slots=%+v", box.Mails["1"], character.Stats["gold"], inventory.Slots)
	}
	delete(inventory.Slots, "0:153")
	if !reflect.DeepEqual(inventory.Slots, slots) {
		t.Fatal("领取改变了既有物品")
	}
}

func TestMailPlacementQuickSlotsRequireExplicitEligibility(t *testing.T) {
	for _, tc := range []struct {
		name       string
		resolution alignedcmd.MailboxItemResolution
		want       int16
	}{
		{"消耗品", alignedcmd.MailboxItemResolution{SlotStart: 65, SlotEnd: 120, StackLimit: 99, PreferQuickSlot: true}, 3},
		{"不可堆叠消耗品", alignedcmd.MailboxItemResolution{SlotStart: 65, SlotEnd: 120, StackLimit: 1, PreferQuickSlot: true}, 3},
		{"装备", alignedcmd.MailboxItemResolution{SlotStart: 9, SlotEnd: 64, StackLimit: 1}, 9},
		{"任务品", alignedcmd.MailboxItemResolution{SlotStart: 177, SlotEnd: 232, StackLimit: 99}, 177},
		{"宠物消耗品", alignedcmd.MailboxItemResolution{TargetListType: 7, SlotStart: 189, SlotEnd: 238, StackLimit: 99, PreferQuickSlot: true}, 189},
	} {
		t.Run(tc.name, func(t *testing.T) {
			slots := mailboxClaimStackSlots(tc.resolution)
			if len(slots) == 0 || slots[0] != tc.want {
				t.Fatalf("落格顺序=%v，期望首格=%d", slots, tc.want)
			}
		})
	}
}

func TestMailEquipmentFullPageFallsBackWithoutMerging(t *testing.T) {
	inventory := dnfrepo.InventoryRecord{Slots: map[string]dnfrepo.ItemStack{"0:3": {ItemID: 700, Count: 1, Extra: map[string]string{"item_kind": "equipment"}}}}
	for slot := 9; slot <= 64; slot++ {
		inventory.Slots["0:"+strconv.Itoa(slot)] = dnfrepo.ItemStack{ItemID: 999, Count: 1}
	}
	resolution := alignedcmd.MailboxItemResolution{Kind: "equipment", EquipmentType: "[weapon]", SlotStart: 9, SlotEnd: 64, StackLimit: 1, AllowQuickSlots: true, DefaultExtra: map[string]string{"item_kind": "equipment", "equipment_type": "[weapon]"}}
	before := dnfrepo.CloneInventory(inventory)
	refreshes, err := addAttachmentToInventory(&inventory, dnfrepo.MailAttachment{ItemID: 700, Count: 2}, resolution)
	if err != nil || len(refreshes) != 2 || refreshes[0].SlotIndex != 4 || refreshes[1].SlotIndex != 5 {
		t.Fatalf("装备邮件满栏回落错误：refresh=%+v err=%v", refreshes, err)
	}
	for _, key := range []string{"0:4", "0:5"} {
		if stack := inventory.Slots[key]; stack.ItemID != 700 || stack.Count != 1 || stack.Extra["item_kind"] != "equipment" {
			t.Fatalf("邮件装备实例被合并或丢失：%s %+v", key, stack)
		}
		delete(inventory.Slots, key)
	}
	if !reflect.DeepEqual(inventory, before) {
		t.Fatal("邮件回落改变了已有装备或分类页")
	}
}
