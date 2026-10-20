package repository

import (
	"testing"
	"time"
)

// 合并判据只看身份：同 item、Extra 只差发放记账键、一边还带缓存的 0x77 raw 行 →
// 必须合并（现场：宠物饲料 7:189 ×600 与 7:190 ×300 的 Extra 连 source 都一样）。
func TestCanMergeItemStackIgnoresGrantBookkeepingAndRawRow(t *testing.T) {
	existing := ItemStack{
		ItemID:   24,
		Count:    600,
		RawEntry: make([]byte, 0x77),
		Extra: map[string]string{
			"item_kind":                "stackable",
			"pvf_path":                 "stackable/cash/creature/creature_food.stk",
			"stackable_type":           "[feed]",
			"source":                   "cera_package",
			"last_grant_source":        "cera_package",
			"cera_package_source_item": "490701976",
			"stack_limit":              "1000",
		},
	}
	incoming := ItemStack{
		ItemID: 24,
		Count:  300,
		Extra: map[string]string{
			"item_kind":      "stackable",
			"pvf_path":       "stackable/cash/creature/creature_food.stk",
			"stackable_type": "[feed]",
			"source":         "cera_shop_pet_consumable",
		},
	}
	if !CanMergeItemStack(existing, incoming) {
		t.Fatal("same identity with different grant bookkeeping must merge")
	}
	// 身份键大小写不敏感。
	upper := incoming
	upper.Extra = map[string]string{"stackable_type": "[FEED]", "item_kind": "Stackable"}
	if !CanMergeItemStack(existing, upper) {
		t.Fatal("identity keys must compare case-insensitively")
	}
	// 只有一边写了身份键 → 缺省即兼容。
	sparse := ItemStack{ItemID: 24, Count: 1, Extra: map[string]string{"pvf_path": "stackable/cash/creature/creature_food.stk"}}
	if !CanMergeItemStack(existing, sparse) {
		t.Fatal("missing identity keys must stay compatible")
	}
}

func TestCanMergeItemStackRejectsIdentityAndExpirationConflicts(t *testing.T) {
	expire := time.Date(2028, time.April, 4, 22, 0, 0, 0, time.UTC)
	feed := ItemStack{
		ItemID: 24,
		Count:  300,
		Extra: map[string]string{
			"item_kind":      "stackable",
			"pvf_path":       "stackable/cash/creature/creature_food.stk",
			"stackable_type": "[feed]",
		},
	}
	for _, test := range []struct {
		name     string
		incoming ItemStack
	}{
		{
			name:     "different pvf path",
			incoming: ItemStack{ItemID: 24, Extra: map[string]string{"pvf_path": "stackable/cash/creature/rename_card.stk"}},
		},
		{
			name:     "different stackable type",
			incoming: ItemStack{ItemID: 24, Extra: map[string]string{"stackable_type": "[creature]"}},
		},
		{
			name:     "different item",
			incoming: ItemStack{ItemID: 25, Extra: map[string]string{"stackable_type": "[feed]"}},
		},
		{
			name:     "different bind",
			incoming: ItemStack{ItemID: 24, Bind: true, Extra: map[string]string{"stackable_type": "[feed]"}},
		},
		{
			name:     "both sides carry different instance expiry",
			incoming: ItemStack{ItemID: 24, ExpireAt: expire, Extra: map[string]string{"stackable_type": "[feed]"}},
		},
	} {
		existing := feed
		if test.name == "both sides carry different instance expiry" {
			existing.ExpireAt = expire.Add(time.Hour)
		}
		if CanMergeItemStack(existing, test.incoming) {
			t.Fatalf("%s must not merge", test.name)
		}
	}

	// 只有一边写了实例到期时间 → 仍按可合并处理（另一半只是没投影出这个字段）。
	dated := ItemStack{ItemID: 24, ExpireAt: expire, Extra: map[string]string{"stackable_type": "[feed]", "expire_time": "1838498400"}}
	if !CanMergeItemStack(feed, dated) {
		t.Fatal("one-sided instance expiry must stay mergeable")
	}
	// 身份键冲突比较的是"非空值"，空串视为缺省。
	blank := ItemStack{ItemID: 24, Extra: map[string]string{"pvf_path": "   ", "stackable_type": "[feed]"}}
	if !CanMergeItemStack(feed, blank) {
		t.Fatal("whitespace-only identity key must count as missing")
	}
}
