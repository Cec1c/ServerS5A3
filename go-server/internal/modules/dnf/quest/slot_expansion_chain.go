package quest

import "sort"

// slotExpansionChainMaxDepth 是 [pre required quest] 反向闭包的深度上限。
//
// 真档 Script.pvf 里三条槽位授权链的深度都是 1（授权任务 -> "资格 (1/2)"），
// 上限只是防止 PVF 换代后出现环 / 超长链时把整本任务手册一次标完成。
// 触顶不会报错：返回的就是已经收集到的那一层，调用方照写。
const slotExpansionChainMaxDepth = 8

// SlotExpansionQuestChainIDs 返回「授权该槽位的任务」+ 它沿 [pre required quest]
// 反向可达的全部祖先任务（升序、去重）。questID 不在目录里时返回 nil。
//
// 为什么需要这个函数（现场：GM 工具开启耳环槽后任务依然存在）：
//
// 真档 Script.pvf 的耳环链是
//
//	2635 `n_quest/new_quest/MetroCenter/system_90_earring_01.qst`
//	     [name] `穿戴耳环 (1/2)` / [grade] `[side]` / [type] `[hunt monster]`
//	     [attribute] `slot expansion` —— 没有 [pre required quest]
//	2636 `n_quest/new_quest/MetroCenter/system_90_earring_02.qst`
//	     [name] `穿戴耳环 (2/2)` / [pre required quest] 2635 / [type] `[seeking]`
//	     [reward type] `[slot expansion]` / [reward int data] 2
//
// 只有 2636 带 `[reward type] [slot expansion]`，所以
// extraEquipmentSlotQuestIDs 一类的判据只能找到 2636；2635 是一条普通的
// 外传狩猎任务，开槽的位写完之后它仍然留在任务手册里 —— 操作者看到的现象。
//
// 判据（只用 PVF 字节，不发明数据）：
//   - 起点是调用方给的授权任务 id 本身，永远包含在结果里；
//   - 只沿 `[pre required quest]` 的**边**反向走：每个 group 是 AND、group 之间是
//     OR，反向闭包把两边的成员都收进来（多收的是同一条教程链上的同胞任务，
//     少收才会让操作者继续看到残留任务）；
//   - **不跨越到别的槽位授权链**：祖先自己带 `[reward type] [slot expansion]`
//     时停在那一条，不再往上走（避免"开耳环槽"顺手动到左右槽的链）；
//   - `[pre required quest]` 点名了目录里不存在的 id 时**跳过**而不是写一行：
//     本仓不合成任务定义，也不写 PVF 里不存在的任务行；
//   - 结果升序去重，便于日志与断言逐字节比对。
func (c *Catalog) SlotExpansionQuestChainIDs(questID int64) []int64 {
	if c == nil || questID <= 0 {
		return nil
	}
	definition, found := c.Find(questID)
	if !found {
		return nil
	}
	chosen := map[int64]struct{}{questID: {}}
	frontier := []Definition{definition}
	for depth := 0; depth < slotExpansionChainMaxDepth && len(frontier) > 0; depth++ {
		next := make([]Definition, 0, len(frontier))
		for _, current := range frontier {
			for _, group := range current.PreRequiredGroups {
				for _, required := range group {
					if required <= 0 {
						continue
					}
					if _, exists := chosen[required]; exists {
						continue
					}
					parent, exists := c.Find(required)
					if !exists {
						continue
					}
					chosen[required] = struct{}{}
					// 另一条槽位授权链的终点：收下这一层就停，不再往上走。
					if normalizeQuestTag(parent.RewardType) == "slot expansion" {
						continue
					}
					next = append(next, parent)
				}
			}
		}
		frontier = next
	}
	ids := make([]int64, 0, len(chosen))
	for id := range chosen {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(left, right int) bool { return ids[left] < ids[right] })
	return ids
}
