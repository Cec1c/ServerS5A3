package repository

import "math"

// 现用PVF stackable/10304001/10304529.stk：[material]，账号绑定宿命之恒。
const BattlePassFateItemID = 10304529

// 只供实际发货事务比较前后入账，不用于交易、仓库转移或邮件创建。
func BattlePassFateCount(groups ...map[string]ItemStack) int64 {
	var total int64
	for _, slots := range groups {
		for _, item := range slots {
			if item.ItemID == BattlePassFateItemID && item.Count > 0 {
				total += min(item.Count, math.MaxInt64-total)
			}
		}
	}
	return total
}
