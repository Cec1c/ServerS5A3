// 本文件定义"堆叠品身份判据"：判断一个新发放/领取的堆栈能不能并进已有的那一行。
package repository

import "strings"

// ItemStackIdentityExtraKeys 是决定堆叠品身份的 Extra 键：这些键不同就说明不是同一个
// 道具实例族，绝不允许合并。判据是"两边都写了值时才比较"，缺省即兼容——因为不同发放
// 路径给同一个道具写的身份键本来就不一样全（GM 邮件只带 item_kind/pvf_path，
// 官方发放还会带 stackable_type）。
//
// 不在这个列表里的键都不参与合并判定：
//   - 发放记账键：source / last_grant_source / cera_package_source_item /
//     booster_source_item / cera_package_overflow_mail / expiration_source …
//   - PVF 派生值：stack_limit / usable_period_days（由道具定义决定，不是实例身份）
//   - 缓存的 0x77 行镜像：raw_entry_hex / current_raw_entry_77 / raw_entry_77
//
// ItemStack.RawEntry 与 ItemStack.Count 同样不参与判定：raw 行是行内序号+数量的镜像，
// 本来就随数量变化。
var ItemStackIdentityExtraKeys = []string{"pvf_path", "item_kind", "stackable_type", "expire_time", "expire_unix"}

// CanMergeItemStack 判断 incoming（新发放/新领取的堆栈）能不能并进 existing 那一行。
//
// 只看会不会改变道具语义的东西：
//  1. 物品 id 与绑定必须一致；
//  2. 实例到期时间：两边都写了值且不同 → 拒绝；只有一边写了 → 按"未知"处理，仍可合并
//     （堆叠品本来就该合成一份，未写到期时间的那一边只是没投影出这个字段）；
//  3. 身份键（ItemStackIdentityExtraKeys）：两边都写了且不同 → 拒绝。
//
// 现场（角色 3，2026-09-24）：宠物饲料一行 7:189 ×600 与一行 7:190 ×300 的 Extra
// 完全相同（连 source/cera_package_source_item 都一样），却被拆成两行；判定函数在
// internal/services/dnfbridge/cera_shop_grant.go，与这里的判据无关（它在 PVF 没有
// [stack limit] 时整段跳过合并）。此函数是"合并判定"的唯一实现，mail / 背包随机奖励 /
// 盒子礼包 / 点券发放都走它，避免各写一套又各自漂移。
func CanMergeItemStack(existing, incoming ItemStack) bool {
	if existing.ItemID != incoming.ItemID || existing.Bind != incoming.Bind {
		return false
	}
	if !existing.ExpireAt.IsZero() && !incoming.ExpireAt.IsZero() && !existing.ExpireAt.Equal(incoming.ExpireAt) {
		return false
	}
	for _, key := range ItemStackIdentityExtraKeys {
		if !ItemStackIdentityExtraCompatible(existing.Extra, incoming.Extra, key) {
			return false
		}
	}
	return true
}

// ItemStackIdentityExtraCompatible 对单个身份键做"缺省即兼容"的比较：
// 两边任意一边没写这个键就算兼容，都写了就必须大小写不敏感地相等。
func ItemStackIdentityExtraCompatible(left, right map[string]string, key string) bool {
	l := strings.TrimSpace(left[key])
	r := strings.TrimSpace(right[key])
	return l == "" || r == "" || strings.EqualFold(l, r)
}
