package dnfbridge

import (
	"context"
	"sort"
	"strconv"
	"strings"

	"longheng.io/server/internal/modules/dnf/jobmap"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// 本文件是"穿戴装备在 PVF 里点名要求的掌握技能"在**技能页下发点**的审计。
//
// 为什么要有它（2026-09-28「光剑重登被卸下」）：
//
//   - 操作者的决定性 A/B：穿**钝器**（`101020668` 无惧者钝器）下线再上线，武器在身上；
//     穿**光剑**（`101040493` 圣耀救赎光剑）下线再上线，武器被脱下。
//   - 两件物品在 PVF 上**唯一**的装备要求差别就是：光剑多一段
//     `[required job skill] `[swordman]` 33 `[demonic swordman]` 33 `[at swordman]` 95`，
//     钝器整段不存在。33 = `skill/Swordman/EquipLightSword.skl` = **光剑掌握**。
//   - 角色 6（test78）在 `dnf_skill_states` 里**有** 33 / level 10，且这次技能页确实把它
//     发给了客户端（`learned_skill_ids=[… 33 …]`）——所以"库里/页里没有这个技能"已经被否掉。
//
// 剩下的、只能用日志判读的问题是**顺序与内容**：技能页（`msg_id=19` class 0）是在穿戴行
// （`game-upper-selected-userinfo-mode1-equipment-create-rows` / `msg_id=2`）之后约 0.56s 才
// 下发的。客户端完全可能在**只拿到穿戴行**的那一刻按"没有这个技能"处置这件装备，随后才
// 收到技能页。要证明或否掉这一条，下一次实测必须能在一行日志里同时看到：
//
//	① 本次技能页里，每个"被穿戴行点名要求"的技能**在不在**、**等级多少**；
//	② 这一页是哪个边界发的（`source`）、体长、已学数量；
//	③ 任何一条要求没被满足的穿戴行单独成行，便于直接 grep。
//
// 它**不改任何下行字节**：默认档只多写日志；把它关掉后日志与今天逐字节相同。
const skillEquipRequirementAuditEnv = "DNFBRIDGE_SKILL_EQUIP_REQUIREMENT_AUDIT"

// skillEquipRequirementAuditReferenceDrop 是**回退档字面量**：写上它（或 0/false/no）
// 之后 `skillEquipRequirementAuditEnabled()` 恒为 false，本文件的任何事件都不再写，
// 登录期日志与"本改动之前"逐字节相同。下行包体在两个档位下本来就一个字节都不差。
const skillEquipRequirementAuditReferenceDrop = "off"

const (
	// skillEquipRequirementAuditEvent：每次技能页下发点的汇总判读行。
	skillEquipRequirementAuditEvent = "game-login-equip-requirement-audit"
	// skillEquipRequirementMissingEvent：有穿戴行的 PVF 要求在本页里**没有被满足**。
	// 这一行就是"客户端有正当理由把这件装备请下身体"的服务端可判读信号。
	skillEquipRequirementMissingEvent = "game-login-equip-requirement-missing"
	// skillEquipRequirementSkippedEvent：审计自己失败了（PVF/仓储读不到等）。
	// 它只说明"这次没判读成功"，绝不改变任何下发内容。
	skillEquipRequirementSkippedEvent = "game-login-equip-requirement-skipped"
)

// skillEquipRequirementSection 是 PVF 装备定义里那段点名掌握技能的段落名。
const skillEquipRequirementSection = "required job skill"

func skillEquipRequirementAuditEnabled() bool {
	return envBool(skillEquipRequirementAuditEnv, true)
}

// equipRequirementTextSource 是本审计需要的最小 PVF 能力（`*platformpvf.Archive` 满足它）。
type equipRequirementTextSource interface {
	ReadText(relativePath string) (string, error)
}

// loginEquipRequiredSkillRow 是一件"PVF 里点名了掌握技能"的穿戴行。
type loginEquipRequiredSkillRow struct {
	Slot        int16
	ItemID      int64
	PVFPath     string
	RequiredIDs []int64
}

// loginEquipRequirementStatus 是一行穿戴行的判读结果：要求里哪些在本次技能页里，
// 分别在多少级；哪些不在。`ServedOK` 是**判据**（要求表里任意一个被下发即满足），
// 要求已按本次技能页的职业筛选；其他职业的同编号技能不能充当掌握技能。
type loginEquipRequirementStatus struct {
	Row      loginEquipRequiredSkillRow
	Served   []string // "33:10"（技能 id : 本页下发的等级）
	Missing  []int64  // 本页里没有（或等级 <= 0）的要求技能
	ServedOK bool
}

// wornEquipmentRequiredSkillRows 从穿戴行里挑出"PVF 点名要求掌握技能"的那些。
//
// 纯函数：只读 `entries` 与 `source`，输出按槽位升序，便于日志与用例逐字节比对。
// 读不到/解析不了的行走跳过（不影响判读其它行），调用方据返回值决定是否记 skipped。
func wornEquipmentRequiredSkillRows(
	source equipRequirementTextSource,
	entries map[string]dnfrepo.EquipmentEntry,
	job byte,
) ([]loginEquipRequiredSkillRow, int) {
	if source == nil || len(entries) == 0 {
		return nil, 0
	}
	slots := make([]int, 0, len(entries))
	slotOf := make(map[int]dnfrepo.EquipmentEntry, len(entries))
	for key, entry := range entries {
		slot, err := strconv.Atoi(strings.TrimSpace(key))
		if err != nil {
			continue
		}
		if _, seen := slotOf[slot]; seen {
			continue
		}
		slotOf[slot] = entry
		slots = append(slots, slot)
	}
	sort.Ints(slots)

	rows := make([]loginEquipRequiredSkillRow, 0, 4)
	unreadable := 0
	for _, slot := range slots {
		entry := slotOf[slot]
		rawPath := ""
		if entry.Extra != nil {
			rawPath = strings.TrimSpace(entry.Extra["pvf_path"])
		}
		if rawPath == "" || !strings.HasPrefix(strings.ToLower(rawPath), "equipment/") {
			continue
		}
		text, actualPath, err := readInitialPVFText(source, initialPVFPath("equipment", rawPath), rawPath)
		if err != nil {
			unreadable++
			continue
		}
		document, err := dnfpvf.Parse(actualPath, text)
		if err != nil {
			unreadable++
			continue
		}
		ids, valid := equipRequiredSkillIDsForJob(document, job)
		if !valid {
			unreadable++
			continue
		}
		if len(ids) == 0 {
			continue
		}
		rows = append(rows, loginEquipRequiredSkillRow{
			Slot:        int16(slot),
			ItemID:      entry.ItemID,
			PVFPath:     actualPath,
			RequiredIDs: ids,
		})
	}
	return rows, unreadable
}

// Keep the PVF job/skill pairs together: numeric IDs are reused between jobs.
// An absent section or a different job's pair contributes no requirement here.
// An unparseable pair is unknown, never evidence that a requirement was served.
func equipRequiredSkillIDsForJob(document *dnfpvf.Document, job byte) ([]int64, bool) {
	tags := jobmap.QuestTags(int(job))
	if len(tags) == 0 {
		return nil, false
	}
	tokens, exists := document.Section(skillEquipRequirementSection)
	if !exists {
		return nil, true
	}
	if len(tokens)%2 != 0 {
		return nil, false
	}
	var ids []int64
	for index := 0; index < len(tokens); index += 2 {
		tag, skill := tokens[index], tokens[index+1]
		if (tag.Kind != dnfpvf.TokenString && tag.Kind != dnfpvf.TokenIdent) ||
			skill.Kind != dnfpvf.TokenInt || skill.Int <= 0 || skill.Int > 0xffff {
			return nil, false
		}
		for _, expected := range tags {
			if strings.EqualFold(strings.TrimSpace(tag.Value), expected) {
				ids = append(ids, skill.Int)
				break
			}
		}
	}
	return normalizeRequiredSkillIDs(ids), true
}

func normalizeRequiredSkillIDs(values []int64) []int64 {
	out := make([]int64, 0, len(values))
	seen := make(map[int64]struct{}, len(values))
	for _, value := range values {
		if value <= 0 || value > 0xffff {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Slice(out, func(left, right int) bool { return out[left] < out[right] })
	return out
}

// evaluateEquipRequirements 把穿戴行的要求与**本次技能页真正下发的内容**对照。
//
// rows 已按职业筛选：光剑的 [swordman] 要求33，[at swordman] 要求95。
// 比较该职业候选技能的正等级存在性；其他职业的同编号不参与判断。
func evaluateEquipRequirements(
	rows []loginEquipRequiredSkillRow,
	servedLevels map[int64]int,
) []loginEquipRequirementStatus {
	statuses := make([]loginEquipRequirementStatus, 0, len(rows))
	for _, row := range rows {
		status := loginEquipRequirementStatus{Row: row}
		for _, required := range row.RequiredIDs {
			level, ok := servedLevels[required]
			if ok && level > 0 {
				status.Served = append(status.Served, strconv.FormatInt(required, 10)+":"+strconv.Itoa(level))
				continue
			}
			status.Missing = append(status.Missing, required)
		}
		status.ServedOK = len(status.Served) > 0
		statuses = append(statuses, status)
	}
	return statuses
}

// loginEquipRequirementRowsText 把判读结果渲染成确定性的单串，便于 grep 与断言。
func loginEquipRequirementRowsText(statuses []loginEquipRequirementStatus) string {
	if len(statuses) == 0 {
		return "none"
	}
	parts := make([]string, 0, len(statuses))
	for _, status := range statuses {
		verdict := "served"
		if !status.ServedOK {
			verdict = "missing"
		}
		parts = append(parts, strings.Join([]string{
			strconv.Itoa(int(status.Row.Slot)),
			strconv.FormatInt(status.Row.ItemID, 10),
			"req[" + joinInt64(status.Row.RequiredIDs) + "]",
			"served[" + joinStringsOrDash(status.Served) + "]",
			"missing[" + joinInt64(status.Missing) + "]",
			verdict,
		}, ":"))
	}
	return strings.Join(parts, ";")
}

func joinInt64(values []int64) string {
	if len(values) == 0 {
		return "-"
	}
	parts := make([]string, 0, len(values))
	for _, value := range values {
		parts = append(parts, strconv.FormatInt(value, 10))
	}
	return strings.Join(parts, ",")
}

// joinStringsOrDash 让"这一侧为空"也留下一个可 grep 的占位符（`-`），
// 免得 `served[]` 与"字段缺失"在下一次实测里分不清。
func joinStringsOrDash(values []string) string {
	if len(values) == 0 {
		return "-"
	}
	return strings.Join(values, ",")
}

// auditLoginEquipRequirements 是技能页下发点上的判读：读取当前**穿戴行**，解析 PVF 要求，
// 与本次技能页的已学技能/等级对照，把结果写进 packet_log。
//
// 任何失败都只记 skipped，绝不返回错误、绝不改变下发内容 —— 它只观察、不参与决策。
func (s *Service) auditLoginEquipRequirements(
	session *gameSession,
	ctx context.Context,
	character dnfrepo.CharacterRecord,
	projection currentSceneSkillInfoProjection,
	source string,
) {
	if s == nil || !skillEquipRequirementAuditEnabled() {
		return
	}
	if !jobmap.Valid(int(projection.job)) {
		s.logGameEvent(session, skillEquipRequirementSkippedEvent,
			"source", source, "character_id", character.CharacterID,
			"job", projection.job, "reason", "unsupported_job")
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	repos, ok := s.repositoryGroup()
	if !ok || repos.Equipment == nil {
		s.logGameEvent(session, skillEquipRequirementSkippedEvent,
			"source", source,
			"character_id", character.CharacterID,
			"reason", "equipment_repository_missing")
		return
	}
	record, found, err := repos.Equipment.Load(ctx, character.CharacterID)
	if err != nil {
		s.logGameEvent(session, skillEquipRequirementSkippedEvent,
			"source", source,
			"character_id", character.CharacterID,
			"reason", "equipment_load_failed",
			"error", err.Error())
		return
	}
	if !found || len(record.Entries) == 0 {
		s.logGameEvent(session, skillEquipRequirementAuditEvent,
			"source", source,
			"character_id", character.CharacterID,
			"worn_rows", 0,
			"requirement_rows", 0,
			"rows", "none",
			"verdict", "no_worn_rows")
		return
	}
	s.initialEquipmentMu.Lock()
	archive, archiveErr := s.initialEquipmentArchiveLocked()
	s.initialEquipmentMu.Unlock()
	if archiveErr != nil {
		s.logGameEvent(session, skillEquipRequirementSkippedEvent,
			"source", source,
			"character_id", character.CharacterID,
			"reason", "pvf_archive_unavailable",
			"error", archiveErr.Error())
		return
	}
	rows, unreadable := wornEquipmentRequiredSkillRows(archive, record.Entries, projection.job)
	statuses := evaluateEquipRequirements(rows, projection.learnedLevels)
	verdict := "all_requirements_served"
	if unreadable > 0 {
		verdict = "requirement_audit_incomplete"
	}
	for _, status := range statuses {
		if !status.ServedOK {
			verdict = "requirement_missing_in_served_skill_page"
			break
		}
	}
	fields := []any{
		"source", source,
		"character_id", character.CharacterID,
		"job", projection.job,
		"grow_type", characterGrowTypeByte(character),
		"msg_id", currentSkillInfoMsgID,
		"skill_page_body_len", len(projection.body),
		"skill_page_learned_count", projection.learnedCount,
		"served_skill_levels", servedSkillLevelsText(projection.learnedLevels),
		"worn_rows", len(record.Entries),
		"requirement_rows", len(rows),
		"unreadable_rows", unreadable,
		"rows", loginEquipRequirementRowsText(statuses),
		"verdict", verdict,
	}
	s.logGameEvent(session, skillEquipRequirementAuditEvent, fields...)
	for _, status := range statuses {
		if status.ServedOK {
			continue
		}
		s.logGameEvent(session, skillEquipRequirementMissingEvent,
			"source", source,
			"character_id", character.CharacterID,
			"job", projection.job,
			"grow_type", characterGrowTypeByte(character),
			"slot", int(status.Row.Slot),
			"item_id", status.Row.ItemID,
			"pvf_path", status.Row.PVFPath,
			"pvf_section", skillEquipRequirementSection,
			"required_skill_ids", status.Row.RequiredIDs,
			"served_skill_levels", servedSkillLevelsText(projection.learnedLevels),
			"verdict", "worn_item_requirement_not_in_served_skill_page")
	}
}

// servedSkillLevelsText 渲染本次技能页真正下发的技能等级向量（按 id 升序）。
// 这是"客户端此刻能看到的全部技能状态"，也是与 PVF 要求对照的那一侧。
func servedSkillLevelsText(levels map[int64]int) string {
	if len(levels) == 0 {
		return "none"
	}
	ids := make([]int64, 0, len(levels))
	for id, level := range levels {
		if level > 0 {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(left, right int) bool { return ids[left] < ids[right] })
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		parts = append(parts, strconv.FormatInt(id, 10)+":"+strconv.Itoa(levels[id]))
	}
	return strings.Join(parts, ",")
}
