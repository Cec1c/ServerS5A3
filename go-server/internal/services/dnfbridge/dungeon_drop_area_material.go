package dnfbridge

const (
	currentDungeonAreaMaterialWeight uint32 = 100
	currentDungeonBirthBudItemID     uint32 = 10161610
)

// 参考90CN轮回源码的区域材料映射。现用PVF有诞生之芽定义及魔界来源说明，
// 但没有普通生成率、怪物item项或独立/世界掉落行，需在服务端接入区域材料池。
// 310..315是可选择的魔界普通图；316专用裂缝、317任务图不进入该范围。
// 仅提供材料池条目，概率、选物、地面账本和拾取继续由已有流程负责。
func currentDungeonAreaMaterialPoolEntry(dungeonID int64) (dungeonMonsterDropPoolEntry, bool) {
	if dungeonID < 310 || dungeonID > 315 {
		return dungeonMonsterDropPoolEntry{}, false
	}
	return dungeonMonsterDropPoolEntry{
		ItemID: currentDungeonBirthBudItemID,
		Weight: currentDungeonAreaMaterialWeight,
	}, true
}
