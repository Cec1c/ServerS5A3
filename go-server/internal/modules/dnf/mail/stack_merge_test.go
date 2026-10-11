package mail

import (
	"testing"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// 领取到的堆叠品必须能并进"同一路径以外"发出来的那一行。
// 现场（角色 3，2026-09-24）：更名卡停在 7:190（点券套餐发放：带 0x77 raw 行 +
// 6 个记账键）和 7:191（邮件领取：只有身份键）两行里，玩家只能手动把卡拖到一起；
// 爆竹同理（0:65 有 raw 行 → 领到的 5 个另开一格）。
func TestCanMailStackIgnoresGrantBookkeepingAndCachedRawRow(t *testing.T) {
	expire := time.Date(2028, time.April, 4, 22, 0, 0, 0, time.UTC)
	existing := dnfrepo.ItemStack{
		ItemID:   25,
		Count:    3,
		RawEntry: make([]byte, 0x77),
		Extra: map[string]string{
			"item_kind":                "stackable",
			"pvf_path":                 "stackable/cash/creature/rename_card.stk",
			"stackable_type":           "[creature]",
			"source":                   "cera_package",
			"last_grant_source":        "cera_package",
			"cera_package_source_item": "490701976",
		},
	}
	claimed := dnfrepo.ItemStack{
		ItemID: 25,
		Count:  1,
		Extra: map[string]string{
			"item_kind":      "stackable",
			"pvf_path":       "stackable/cash/creature/rename_card.stk",
			"stackable_type": "[creature]",
		},
	}
	if !canMailStack(existing, claimed) {
		t.Fatal("claimed rename card must merge into the cera-package row")
	}

	// 一边有实例到期时间、另一边没写，仍应合成一份；两边都写了但不同则拒绝。
	firecracker := dnfrepo.ItemStack{ItemID: 490701730, Count: 10, Extra: map[string]string{"stackable_type": "[random reward item]"}}
	claimedFirecracker := dnfrepo.ItemStack{
		ItemID:   490701730,
		Count:    5,
		ExpireAt: expire,
		Extra:    map[string]string{"stackable_type": "[random reward item]", "expire_time": "1838498400"},
	}
	if !canMailStack(firecracker, claimedFirecracker) {
		t.Fatal("firecracker claim must merge with the row that has no instance expiry")
	}
	firecracker.ExpireAt = expire.Add(time.Hour)
	if canMailStack(firecracker, claimedFirecracker) {
		t.Fatal("two different instance expiries must not merge")
	}

	// 身份不同的（PVF 路径不一样）绝不能合。
	other := dnfrepo.ItemStack{
		ItemID: 25,
		Count:  1,
		Extra:  map[string]string{"pvf_path": "stackable/cash/creature/creature_food.stk", "stackable_type": "[feed]"},
	}
	if canMailStack(existing, other) {
		t.Fatal("different PVF identity must not merge")
	}
}
