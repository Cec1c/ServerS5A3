package dnfbridge

const currentDungeonCarbonCrystalItemID uint32 = 10163135

// 90级史诗制作的领主固定产出，不参加 WorldDrop 或普通材料随机池。
// 等级取地下城的 basis level；疲劳豁免沿用进图和房间扣除使用的同一规则。
// Epic Road / Silent Forest 的票券入图路径在提交票据后直接完成，不执行房间疲劳扣除；
// 它们即使 DGN 没有 [no fatigue] 也必须按免疲劳模式排除。
// 旧资源未包含该物品时保持原行为，不向不配套的 PVF 注入物品编号。
func currentDungeonCarbonCrystalEligible(runtime *runtimeDungeonState, drops *pvfDungeonDropCatalog) bool {
	if runtime == nil || drops == nil || runtime.epicEntry != nil || currentDungeonUsesNoFatigue(runtime.Dungeon) {
		return false
	}
	level := runtime.Dungeon.Metadata.BasisLevel
	if !level.Set || level.Value < 55 {
		return false
	}
	// itemRefs 在目录构造时一次建立，此后只读；物品正文仍由正常生成链路解析。
	reference, present := drops.itemRefs[currentDungeonCarbonCrystalItemID]
	return present && reference.kind == dungeonDropItemStackable
}
