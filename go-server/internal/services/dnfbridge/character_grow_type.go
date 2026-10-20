package dnfbridge

import (
	"strconv"
	"strings"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// 本文件是 dnfbridge 里读取角色成长方向（grow_type）的唯一入口。
//
// 为什么要有它：`dnf_characters.grow_type` mirror 列是唯一权威真源，载入时由
// mysql mergeCharacterMirrorStats 投影进 `CharacterRecord.Stats["grow_type"]`，
// 全桥有 30+ 处读取（选角名册、actor tail、userinfo、op9、组队槽位、冒险团、
// 任务资格、战力、技能门禁…）。这些读取原先各写各的 numericCharacterStatValue，
// 值虽然同源，但没有任何地方能回答"这个 0 是合法未转职，还是权威值没水合"。
// 现在全部收口到下面这几个 helper：一处定义、一处可改、一处带 presence 语义。
//
// 与旧读写逐字节兼容：characterGrowTypeByte/Stat/Int 的返回值与原
// numericCharacterStatValue(record, "grow_type")（及其外层 byte()/int() 转换）
// 完全相同，包括负值按 0、超域值按 byte 截断。

// characterGrowth 是一次权威读取的完整结果。
type characterGrowth struct {
	// Packed 是打包值（低四位=转职分支，高四位=觉醒段）；键缺失时为 0。
	Packed int
	// Present 为 false 表示记录没有携带权威键（仓储未水合 / 部分记录 / 脏值），
	// 与"合法值 0 = 未转职"必须区分。
	Present bool
	// InDomain 表示 Present 且值落在 TINYINT UNSIGNED 的 0..255。
	InDomain bool
	// Branch 是低四位（0 = 未转职），Awakening 是高四位（0/1/2）。
	Branch    int
	Awakening int
}

// characterGrowType 读取角色当前成长方向。所有分支/觉醒相关的判断都必须用它，
// 不要直接读 character.Stats 里的同名键。
func characterGrowType(character dnfrepo.CharacterRecord) characterGrowth {
	packed, present := dnfrepo.CharacterGrowType(character)
	_, inDomain := dnfrepo.CharacterGrowTypeInDomain(character)
	branch, awakening := dnfrepo.DecodeCharacterGrowType(packed)
	return characterGrowth{
		Packed:    packed,
		Present:   present,
		InDomain:  inDomain,
		Branch:    branch,
		Awakening: awakening,
	}
}

// characterGrowTypeStat 与原 numericCharacterStatValue(record, "grow_type") 等价
// （int64、键缺失或负值返回 0），供日志字段使用。
func characterGrowTypeStat(character dnfrepo.CharacterRecord) int64 {
	return int64(characterGrowType(character).Packed)
}

// characterGrowTypeInt 与原 int(numericCharacterStatValue(record, "grow_type")) 等价。
func characterGrowTypeInt(character dnfrepo.CharacterRecord) int {
	return characterGrowType(character).Packed
}

// characterGrowTypeByte 与原 byte(numericCharacterStatValue(record, "grow_type")) 等价
// （含超域值按 byte 截断）。
func characterGrowTypeByte(character dnfrepo.CharacterRecord) byte {
	return byte(characterGrowType(character).Packed)
}

// characterGrowTypeAuditState 把一次权威读取渲染成稳定字段对，供判读串使用。
func characterGrowTypeAuditState(character dnfrepo.CharacterRecord) []any {
	growth := characterGrowType(character)
	return []any{
		dnfrepo.CharacterGrowTypeStatKey, growth.Packed,
		"grow_type_present", growth.Present,
		"grow_type_in_domain", growth.InDomain,
		"grow_branch", growth.Branch,
		"grow_awakening", growth.Awakening,
		"grow_type_source", characterGrowTypeSource,
	}
}

// characterGrowTypeSource 是判读串里"这个值从哪来"的固定答案：权威 mirror 列，
// 不是 dnf_character_stats（该表按设计只存非 mirror 键，线上 0 行 grow_type）。
const characterGrowTypeSource = "dnf_characters.grow_type"

// skillGrowTypeAuditEnv：DNFBRIDGE_SKILL_GROW_TYPE_AUDIT（默认 on）。
// 控制登录技能同步边界的 game-skill-grow-type-state 判读事件；=off 只关日志，
// 不改任何游戏行为（fail-open）。
const skillGrowTypeAuditEnv = "DNFBRIDGE_SKILL_GROW_TYPE_AUDIT"

func skillGrowTypeAuditEnabled() bool {
	return envBool(skillGrowTypeAuditEnv, true)
}

// formatSkillIDs 把技能号列表渲染成 [27 33 110]，与既有事件的写法一致。
func formatSkillIDs(ids []uint16) string {
	if len(ids) == 0 {
		return "[]"
	}
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		parts = append(parts, strconv.FormatUint(uint64(id), 10))
	}
	return "[" + strings.Join(parts, " ") + "]"
}
