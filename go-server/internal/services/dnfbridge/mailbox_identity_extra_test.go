package dnfbridge

import "testing"

// 领取端必须保留道具身份：pet.isCurrentPetRenameCard 按 pvf_path + stackable_type
// 判定更名卡，官方发放路径（点券套餐/商城/盒子）都会写这两个键，而 GM 面板邮件
// 没有附件 Extra。更名卡没有 [expiration date]，所以旧的到期投影（只在有到期日时
// 才写身份键）根本不会给它补——领出来的卡就"看得见、改不了名"。
func TestMailboxItemResolutionKeepsIdentityForCreatureStackable(t *testing.T) {
	resolution, err := mailboxItemResolutionFromPVF(dungeonDropItemDefinition{
		ItemID:        25,
		Kind:          dungeonDropItemStackable,
		PVFPath:       "stackable/cash/creature/rename_card.stk",
		StackableType: "[creature]",
		SlotStart:     65,
		SlotEnd:       120,
		StackLimit:    1000,
	}, false)
	if err != nil {
		t.Fatalf("resolve rename card: %v", err)
	}
	if resolution.DefaultExtra["pvf_path"] != "stackable/cash/creature/rename_card.stk" {
		t.Fatalf("pvf_path missing from claim projection: %+v", resolution.DefaultExtra)
	}
	if resolution.DefaultExtra["stackable_type"] != "[creature]" {
		t.Fatalf("stackable_type missing from claim projection: %+v", resolution.DefaultExtra)
	}
	if resolution.DefaultExtra["item_kind"] != "stackable" {
		t.Fatalf("item_kind missing from claim projection: %+v", resolution.DefaultExtra)
	}
	// 宠物更名卡仍然必须落宠物栏消耗品段（玩家期望 + A21 参考实现）。
	if resolution.TargetListType != currentPetInventoryListType ||
		resolution.SlotStart != currentCeraShopPetConsumableSlotStart ||
		resolution.SlotEnd != currentCeraShopPetConsumableSlotEnd {
		t.Fatalf("rename card target list changed: %+v", resolution)
	}
}

// 附件自己带了身份键时不能被改写。
func TestMailboxItemResolutionIdentityMergeKeepsAttachmentValues(t *testing.T) {
	merged := mergeMailboxItemIdentityExtra(map[string]string{
		"pvf_path": "stackable/cash/player_owned.stk",
	}, dungeonDropItemDefinition{
		ItemID:        490701730,
		Kind:          dungeonDropItemStackable,
		PVFPath:       "stackable/cash/chn_490701730.stk",
		StackableType: "[random reward item]",
	})
	if merged["pvf_path"] != "stackable/cash/player_owned.stk" {
		t.Fatalf("attachment identity was overwritten: %+v", merged)
	}
	if merged["stackable_type"] != "[random reward item]" || merged["item_kind"] != "stackable" {
		t.Fatalf("missing identity keys not filled: %+v", merged)
	}
}
