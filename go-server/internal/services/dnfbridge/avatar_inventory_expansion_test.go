package dnfbridge

import "testing"

// 装扮栏扩展券的识别必须只看运行时 PVF 路径：2683901..2683908 是 8 个已验证阶段
// （每阶段 +7 格），同目录里 2683909..2683915 属于该家族但超出客户端档位表，
// 必须被识别出来并在结账时拒绝，而不是当成普通道具或扣了点券不生效。
func TestCurrentCeraShopAvatarInventoryUpgradeStage(t *testing.T) {
	cases := []struct {
		path  string
		stage uint16
		ok    bool
	}{
		{"stackable/cash/chn_20140819_avatar_inven/avatar_inven_2683901.stk", 1, true},
		{"stackable/cash/chn_20140819_avatar_inven/avatar_inven_2683904.stk", 4, true},
		{"stackable/cash/chn_20140819_avatar_inven/avatar_inven_2683908.stk", 8, true},
		{"stackable\\cash\\chn_20140819_avatar_inven\\avatar_inven_2683902.stk", 2, true},
		{"stackable/cash/chn_20140819_avatar_inven/avatar_inven_2683909.stk", 0, true},
		{"stackable/cash/chn_20140819_avatar_inven/avatar_inven_2683915.stk", 0, true},
		{"stackable/cash/inven_upgradekit1.stk", 0, false},
		{"stackable/cash/chn_20140819_magi/recipe/chn_magi_rcp_06_2683921.stk", 0, false},
		{"stackable/cash/chn_20140819_avatar_inven/avatar_inven_abc.stk", 0, false},
	}
	for _, tc := range cases {
		stage, ok := currentCeraShopAvatarInventoryUpgradeStage(dungeonDropItemDefinition{
			ItemID:        2683901,
			Kind:          dungeonDropItemStackable,
			PVFPath:       tc.path,
			StackableType: "[etc]",
		})
		if stage != tc.stage || ok != tc.ok {
			t.Fatalf("path=%q stage=%d ok=%t, want stage=%d ok=%t", tc.path, stage, ok, tc.stage, tc.ok)
		}
	}
	// 非 stackable（例如设计图被解析成别的 kind）不能被当成扩展券。
	if _, ok := currentCeraShopAvatarInventoryUpgradeStage(dungeonDropItemDefinition{
		Kind:    dungeonDropItemEquipment,
		PVFPath: "stackable/cash/chn_20140819_avatar_inven/avatar_inven_2683901.stk",
	}); ok {
		t.Fatal("non-stackable definition must not be recognized as an avatar expansion coupon")
	}
}
