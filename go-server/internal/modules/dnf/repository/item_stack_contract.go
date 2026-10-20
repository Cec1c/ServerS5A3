// 本文件是"一行到底能不能堆叠、上限是多少"的唯一判据实现。
//
// 为什么单独成文件：现场至少有两条路径要拿这个判据——
//   - 手动拖拽合并（`internal/modules/dnf/inventory/owner.go` 的 canStack/canSplitStack，
//     op19 的 stack/split/swap 三态就是它决定的）；
//   - 交易收下道具（`internal/modules/dnf/itemtrade/owner.go`，并入既有行前必须先确认
//     两边都是可堆叠的同一件东西，否则会把装备/时装折进行里）。
//
// 它和 CanMergeItemStack 的分工：
//   - CanMergeItemStack（item_stack_identity.go）= "是不是同一个实例族"（身份比较，缺省即兼容）；
//   - StackContract（本文件）= "这一行有没有 PVF 写明的堆叠资格与上限"（严格判据）。
//
// 现场取证（2026-09-28，线上库只读普查 1892 行）：PVF 可堆叠、却缺
// `item_kind=stackable` / `pvf_path=stackable/*` 契约键的行**只有**强化/增幅摧毁补偿
// 那一族（item 3243 角色 6/15/16 共 12 行、item 3244 角色 5/6 共 4 行）以及一行历史
// 遗留的更名卡；正是它们让 op19 拖拽被判成"不可堆叠"，服务端回 swap 而不是 stack，
// 玩家看到的就是"这些道具不能正常堆叠"。
package repository

import (
	"math"
	"strconv"
	"strings"
)

// StackContract 读出一行的 PVF 堆叠契约：规范化的 pvf_path 与 [stack limit]。
//
//	ok == false：这一行**不可堆叠**（没有 Extra、item_kind 不是 stackable、
//	             pvf_path 不以 stackable/ 开头或含 `../`、上限与自身数量自相矛盾）。
//	limit == 0 ：PVF 没写 [stack limit]（或写成 0）= 无上限，与真实 PVF 里
//	             宠物饲料 / 强烈之痕迹 3243 / 浓缩的纯洁之骸 3244 的形状一致。
//	limit  > 0 ：严格上限（当前客户端与 86JP 参考实现把数量放在有符号 32 位里，
//	             无上限档也要求数量 <= MaxInt32）。
func StackContract(stack ItemStack) (string, int64, bool) {
	if stack.ItemID <= 0 || stack.Extra == nil {
		return "", 0, false
	}
	if !strings.EqualFold(strings.TrimSpace(stack.Extra["item_kind"]), "stackable") {
		return "", 0, false
	}
	pvfPath := strings.ToLower(strings.TrimSpace(strings.ReplaceAll(stack.Extra["pvf_path"], "\\", "/")))
	if !strings.HasPrefix(pvfPath, "stackable/") || strings.Contains(pvfPath, "../") {
		return "", 0, false
	}
	raw := strings.TrimSpace(stack.Extra["stack_limit"])
	if raw == "" {
		if stack.Count <= 0 || stack.Count > math.MaxInt32 {
			return "", 0, false
		}
		return pvfPath, 0, true
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value < 0 || stack.Count <= 0 {
		return "", 0, false
	}
	if value == 0 {
		if stack.Count > math.MaxInt32 {
			return "", 0, false
		}
		return pvfPath, 0, true
	}
	if stack.Count > value {
		return "", 0, false
	}
	return pvfPath, value, true
}
