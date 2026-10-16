package repository

import "testing"

func TestMonsterCardUpgradeIdentitySeparatesDifferentAttributes(t *testing.T) {
	card := ItemStack{ItemID: 3619, Count: 5, Extra: map[string]string{"pvf_path": "stackable/monstercard/example.stk"}}
	upgraded := card
	SetMonsterCardUpgradeCount(&upgraded, 1)
	if CanMergeItemStack(card, upgraded) || CanMergeItemStack(upgraded, card) {
		t.Fatal("原始卡片与升级卡片被判定可叠加")
	}
	if level, _ := MonsterCardUpgradeCount(card); level != 0 {
		t.Fatal("复制一张升级时改变了原堆级数")
	}
	other := upgraded
	SetMonsterCardUpgradeCount(&other, 2)
	if CanMergeItemStack(upgraded, other) {
		t.Fatal("不同升级次数的卡片被判定可叠加")
	}
	same := upgraded
	if !CanMergeItemStack(upgraded, same) {
		t.Fatal("同ID同级同身份卡片不能叠加")
	}
	bare := ItemStack{ItemID: 3619, Count: 1}
	if CanMergeItemStack(upgraded, bare) || !CanMergeItemStack(card, bare) {
		t.Fatal("未带级数字段的原卡没有按0级比较")
	}
	raw := make([]byte, 119)
	raw[0x12] = 1
	legacy := ItemStack{ItemID: 3619, Count: 1, RawEntry: raw}
	if !CanMergeItemStack(upgraded, legacy) || CanMergeItemStack(card, legacy) {
		t.Fatal("历史库存行升级字节未参与卡片身份")
	}
	bad := upgraded
	bad.Extra = map[string]string{"monster_card_upgrade_count": "1", "byte_12": "2"}
	if CanMergeItemStack(upgraded, bad) {
		t.Fatal("冲突的卡片级数字段被合并")
	}
}

func TestOtherStackableUnknownByteKeepsMergeContract(t *testing.T) {
	a := ItemStack{ItemID: 15, Count: 1, Extra: map[string]string{"byte_12": "1"}}
	b := ItemStack{ItemID: 15, Count: 1, Extra: map[string]string{"byte_12": "2"}}
	if !CanMergeItemStack(a, b) {
		t.Fatal("卡片身份修复扩大到未识别的其他堆叠品")
	}
}
