// 本文件定义角色"成长方向"（grow_type）的权威真源访问器。
//
// 真源对照（2026-09-25 定案，证据见 `90CN_grow_type真源与分支技能门禁-取证与修复报告.md`）：
//
//	载体                            谁写                                          谁读
//	dnf_characters.grow_type 列     quest/finish_owner.go:394（转职/觉醒任务原子提交） 本文件的 accessor
//	                                gmtool/api_characters.go:837（GM 面板）           （载入时由
//	                                dnfbridge/character_created_defaults.go:56（建号=0） mysql mergeCharacterMirrorStats
//	dnf_character_stats             **无人写**：replaceCharacterExtraStats 主动跳过       写进 Stats，
//	                                mirror 键，所以该表永远不会有 grow_type 行           故两者同源）
//
// 也就是说：`Stats["grow_type"]` 不是第二个真源，它只是 `dnf_characters.grow_type`
// 列在 `CharacterRecord` 里的投影。旧代码把它当普通 stat 用裸 map 读取，无法区分
// "键缺失（仓储没水合）" 与 "值就是 0（未转职）" —— 这两种情况在分支技能门禁里
// 结论完全相反，因此统一收口到本文件的 accessor。
package repository

// CharacterGrowTypeStatKey 是成长方向的规范 stat 键，映射 `dnf_characters.grow_type`
// （TINYINT UNSIGNED）mirror 列。
//
// 打包语义（与客户端、PVF 一致，全仓库唯一口径）：
//
//	低四位 = 转职分支（PVF `[growtype N]` 的 N-1，0 = 未转职）
//	高四位 = 觉醒段（0 = 未觉醒 / 1 = 一觉 / 2 = 二觉）
const CharacterGrowTypeStatKey = "grow_type"

// CharacterGrowType 返回角色的打包 grow_type。
//
// 第二个返回值表示"权威值是否存在"：
//   - true  —— 记录携带该键且值非负（0 也是**有效值**，表示未转职）；
//   - false —— 键缺失，或值为负（仓储未水合 / 部分记录 / 脏数据）。
//
// 旧调用点 `numericCharacterStatValue(record, "grow_type")` 把两者都当 0，
// 于是"键缺失"会静默变成"未转职"。
func CharacterGrowType(record CharacterRecord) (int, bool) {
	if record.Stats == nil {
		return 0, false
	}
	value, ok := record.Stats[CharacterGrowTypeStatKey]
	if !ok || value < 0 {
		return 0, false
	}
	return int(value), true
}

// CharacterGrowTypeInDomain 在 CharacterGrowType 之上再要求值落在持久化域内
// （TINYINT UNSIGNED 的 0..255）。诊断事件用它区分"脏值"与"合法值"。
func CharacterGrowTypeInDomain(record CharacterRecord) (int, bool) {
	packed, ok := CharacterGrowType(record)
	if !ok || packed > 0xff {
		return 0, false
	}
	return packed, true
}

// DecodeCharacterGrowType 拆出打包 grow_type 的两个分量。
// 负值按 0 处理，超域值按位截断（与客户端读取该字节的方式一致）。
func DecodeCharacterGrowType(packed int) (branch int, awakening int) {
	if packed < 0 {
		packed = 0
	}
	return packed & 0x0f, (packed >> 4) & 0x0f
}

// CharacterGrowTypeBranch 返回转职分支（低四位）：0 = 未转职。
func CharacterGrowTypeBranch(packed int) int {
	branch, _ := DecodeCharacterGrowType(packed)
	return branch
}

// CharacterGrowTypeAwakeningStage 返回觉醒段（高四位）：0/1/2。
func CharacterGrowTypeAwakeningStage(packed int) int {
	_, awakening := DecodeCharacterGrowType(packed)
	return awakening
}
