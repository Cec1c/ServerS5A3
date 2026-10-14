package repository

import (
	"strconv"
	"strings"
)

// 所有发奖、手工搬运与整理共用同一升级身份比较。
func MonsterCardUpgradeIdentityCompatible(left, right ItemStack) bool {
	if !HasMonsterCardIdentity(left) && !HasMonsterCardIdentity(right) {
		return true
	}
	l, lok := MonsterCardUpgradeCount(left)
	r, rok := MonsterCardUpgradeCount(right)
	return lok && rok && l == r
}

// HasMonsterCardIdentity 只识别明确的怪物卡片，保留其他道具未知字节的旧口径。
func HasMonsterCardIdentity(stack ItemStack) bool {
	if _, ok := stack.Extra["monster_card_upgrade_count"]; ok {
		return true
	}
	if strings.EqualFold(strings.Trim(stack.Extra["item_category"], "` []"), "monster card") {
		return true
	}
	return strings.Contains(strings.ToLower(strings.ReplaceAll(stack.Extra["pvf_path"], "\\", "/")), "/monstercard/")
}

// MonsterCardUpgradeCount 采用与库存显示相同的字段优先级；未写字段的原卡
// 等价0级，显式无效或相互冲突的级数不能被普通叠加路径吞掉。
func MonsterCardUpgradeCount(stack ItemStack) (byte, bool) {
	var level byte
	found := false
	for _, key := range []string{"monster_card_upgrade_count", "enchant_upgrade_count", "byte_12", "value_12"} {
		text, exists := stack.Extra[key]
		if !exists {
			continue
		}
		value, err := strconv.ParseUint(strings.TrimSpace(text), 10, 8)
		if err != nil || (found && level != byte(value)) {
			return 0, false
		}
		level, found = byte(value), true
	}
	if found {
		return level, true
	}
	if len(stack.RawEntry) > 0x12 {
		return stack.RawEntry[0x12], true
	}
	return 0, true
}

// SetMonsterCardUpgradeCount 同时保存语义身份和当前EXE库存行+0x12。
// 调用者可以安全地从同一堆原卡复制一张，原堆的元数据和字节保持。
func SetMonsterCardUpgradeCount(stack *ItemStack, level byte) {
	if stack == nil {
		return
	}
	extra := make(map[string]string, len(stack.Extra)+4)
	for key, value := range stack.Extra {
		extra[key] = value
	}
	value := strconv.Itoa(int(level))
	extra["item_category"] = "monster card"
	for _, key := range []string{"monster_card_upgrade_count", "enchant_upgrade_count", "byte_12"} {
		extra[key] = value
	}
	if _, exists := extra["value_12"]; exists {
		extra["value_12"] = value
	}
	stack.Extra = extra
	if len(stack.RawEntry) > 0x12 {
		stack.RawEntry = append([]byte(nil), stack.RawEntry...)
		stack.RawEntry[0x12] = level
	}
}
