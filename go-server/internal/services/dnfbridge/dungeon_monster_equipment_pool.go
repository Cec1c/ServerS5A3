package dnfbridge

// 普通杀怪只选择现用概率表实际可抽到的白、蓝、紫、粉装备。
// 等级窗口、模板生成权重及白装回退来自现用PVF；奖励概率仍由死亡规划器决定。
// 与翻牌复用只读模板缓存，不改变异界、独立奖励或深渊的装备来源。
func (c *pvfDungeonDropCatalog) selectOrdinaryMonsterEquipment(rng *currentDungeonDropLCG, level int, down, up, rarity int64) (uint32, bool, error) {
	if rarity < 0 || rarity > 3 {
		return 0, false, nil
	}
	return c.selectGeneratedEquipment(rng, level, down, up, rarity)
}
