package gmtool

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	dnfcharstat "longheng.io/server/internal/modules/dnf/charstat"
	dnfprofession "longheng.io/server/internal/modules/dnf/profession"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

const (
	// maxCharacterLevel 是 90CN 客户端的等级上限（与 dnfbridge 的冒险团上限一致）。
	maxCharacterLevel = 90
	// A21 前台把 9,990,000 显示成 999 负重。
	inventoryLimitDisplayUnit = 10000
	inventoryLimitMaxValue    = 9990000
	// C#/Go 两侧共用的固定 HP/MP 兼容加成。
	csharpPremiumHPBonus int64 = 9800
	csharpPremiumMPBonus int64 = 10500
)

// inventory list type：与当前客户端一致（dnfrepo 只导出了 Main=0）。
const (
	listTypeMain          = 0
	listTypeAvatar        = 1
	listTypePersonalCargo = 2
	listTypeEquipment     = 3
	listTypePet           = 7
	listTypeAccountCargo  = 12
	listTypeGuildMedal    = 38
)

type statRow struct {
	Key   string
	Label string
	Stat  string
	Zero  bool
}

var characterStatRows = buildCharacterStatRows()

func buildCharacterStatRows() []statRow {
	rows := []statRow{
		{Key: "hpMax", Label: "HP最大值", Stat: "stat_hp_max"},
		{Key: "mpMax", Label: "MP最大值", Stat: "stat_mp_max"},
		{Key: "physAtk", Label: "物理攻击力", Stat: "stat_physical_attack"},
		{Key: "physDef", Label: "物理防御力", Stat: "stat_physical_defense"},
		{Key: "magAtk", Label: "魔法攻击力", Stat: "stat_magical_attack"},
		{Key: "magDef", Label: "魔法防御力", Stat: "stat_magical_defense"},
		{Key: "fireRes", Label: "火属性抗性", Stat: "stat_fire_resistance"},
		{Key: "iceRes", Label: "冰属性抗性", Stat: "stat_water_resistance"},
		{Key: "darkRes", Label: "暗属性抗性", Stat: "stat_dark_resistance"},
		{Key: "lightRes", Label: "光属性抗性", Stat: "stat_light_resistance"},
	}
	statusLabels := []string{
		"减速抗性", "冰冻抗性", "中毒抗性", "眩晕抗性", "诅咒抗性", "失明抗性", "感电抗性",
		"石化抗性", "睡眠抗性", "灼伤抗性", "即死抗性", "出血抗性", "穿刺抗性",
		"被攻击时回避率", "混乱抗性", "束缚抗性", "所有异常状态抗性",
	}
	for idx, label := range statusLabels {
		rows = append(rows, statRow{
			Key:   fmt.Sprintf("statusRes%d", idx),
			Label: label,
			Stat:  fmt.Sprintf("active_status_resistance_%02d", idx),
			Zero:  true,
		})
	}
	rows = append(rows,
		statRow{Key: "inventoryLimit", Label: "最大负重", Stat: "stat_inventory_limit"},
		statRow{Key: "hpRegen", Label: "HP恢复率", Stat: "stat_hp_regen_speed"},
		statRow{Key: "mpRegen", Label: "MP恢复率", Stat: "stat_mp_regen_speed"},
		statRow{Key: "moveSpeed", Label: "移动速度", Stat: "stat_move_speed"},
		statRow{Key: "attackSpeed", Label: "攻击速度", Stat: "stat_attack_speed"},
		statRow{Key: "castSpeed", Label: "施放速度", Stat: "stat_cast_speed"},
		statRow{Key: "hitRecovery", Label: "硬直", Stat: "stat_hit_recovery"},
		statRow{Key: "jumpPower", Label: "跳跃力", Stat: "stat_jump_power"},
		statRow{Key: "weight", Label: "重量", Stat: "stat_weight"},
	)
	return rows
}

// ---------------------------------------------------------------------------
// 角色列表 / 详情
// ---------------------------------------------------------------------------

func (s *Server) handleCharacterList(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	group, err := s.repositories()
	if err != nil {
		writeFailure(w, err.Error())
		return
	}
	accountFilter := queryInt(r, "accountId", -1)

	characters := make([]dnfrepo.CharacterRecord, 0, 16)
	if accountFilter >= 0 {
		account, err := s.webAccountByPanelID(ctx, int64(accountFilter))
		if err != nil {
			writeFailure(w, fmt.Sprintf("账号不存在: %d", accountFilter))
			return
		}
		list, listErr := s.charactersForAccount(ctx, group, account.AccountID)
		if listErr != nil {
			writeFailure(w, listErr.Error())
			return
		}
		characters = append(characters, list...)
	} else {
		accounts, accErr := s.webAccounts(ctx)
		if accErr != nil {
			writeFailure(w, accErr.Error())
			return
		}
		for _, account := range accounts {
			list, listErr := s.charactersForAccount(ctx, group, account.AccountID)
			if listErr != nil {
				continue
			}
			characters = append(characters, list...)
		}
	}

	payload := make([]map[string]any, 0, len(characters))
	for _, character := range characters {
		job := jobNumber(character)
		grow := growTypeValue(character)
		payload = append(payload, map[string]any{
			"characterId": panelIDOfCharacter(character),
			"name":        character.Name,
			"level":       character.Level,
			"exp":         statInt(character, "exp"),
			"job":         job,
			"growType":    grow,
			"jobName":     s.jobDisplayName(byte(job), byte(grow)),
			"bonusSp":     statInt(character, "bonus_sp"),
			"bonusTp":     statInt(character, "bonus_tp"),
			"accountId":   panelIDFromAccountID(character.AccountID),
			"accountName": character.AccountID,
		})
	}
	writeJSON(w, map[string]any{"characters": payload})
}

func (s *Server) handleCharacterDetail(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	group, err := s.repositories()
	if err != nil {
		writeFailure(w, err.Error())
		return
	}
	panelID, err := strconv.Atoi(strings.TrimSpace(r.PathValue("id")))
	if err != nil {
		writeFailure(w, "角色编号无效")
		return
	}
	character, err := s.loadCharacter(ctx, group, panelID)
	if err != nil {
		writeFailure(w, fmt.Sprintf("角色不存在: %d", panelID))
		return
	}
	writeJSON(w, s.characterPayload(
		character,
		s.accountCera(ctx, group, character.AccountID),
		s.jobDisplayName(byte(jobNumber(character)), byte(growTypeValue(character))),
	))
}

// characterPayload 组装 A21 契约里的角色详情。点券来自账号级 account_cera
// （角色表的 cera 列是历史遗留只读副本，客户端不认）。
//
// jobName 由调用方传入（需要 PVF 职业表），这样本函数保持纯计算、可单测。
func (s *Server) characterPayload(record dnfrepo.CharacterRecord, accountCera int64, jobName string) map[string]any {
	job := jobNumber(record)
	grow := growTypeValue(record)
	exEquip := statInt(record, "ex_equip_slot_stat")
	return map[string]any{
		"characterId":                 panelIDOfCharacter(record),
		"accountId":                   panelIDFromAccountID(record.AccountID),
		"name":                        record.Name,
		"level":                       record.Level,
		"exp":                         statInt(record, "exp"),
		"job":                         job,
		"jobName":                     jobName,
		"growType":                    grow,
		"bonusSp":                     statInt(record, "bonus_sp"),
		"bonusTp":                     statInt(record, "bonus_tp"),
		"exEquipSlotStat":             exEquip,
		"extraEquipmentSlotsUnlocked": byte(exEquip)&ExtraEquipmentSlotsAll == ExtraEquipmentSlotsAll,
		"extraEquipmentState":         extraEquipmentStatePayload(byte(exEquip)),
		"maxLevel":                    maxCharacterLevel,
		"wallet": map[string]any{
			"gold":      statInt(record, "gold"),
			"cera":      accountCera,
			"tokenCera": 0,
			"luckyStar": 0,
		},
	}
}

// ---------------------------------------------------------------------------
// 属性
// ---------------------------------------------------------------------------

func (s *Server) handleCharacterStats(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	group, err := s.repositories()
	if err != nil {
		writeFailure(w, err.Error())
		return
	}
	panelID, err := strconv.Atoi(strings.TrimSpace(r.PathValue("id")))
	if err != nil {
		writeFailure(w, "角色编号无效")
		return
	}
	character, err := s.loadCharacter(ctx, group, panelID)
	if err != nil {
		writeFailure(w, fmt.Sprintf("角色不存在: %d", panelID))
		return
	}

	values := make(map[string]int64, len(characterStatRows))
	for _, row := range characterStatRows {
		values[row.Stat] = statInt(character, row.Stat)
	}
	// 老角色可能从未写入过属性镜像；这时用 PVF 成长表现算一份。
	if values["stat_hp_max"] <= 0 {
		if computed, computeErr := s.computedCharacterStats(character); computeErr == nil {
			for key, value := range computed {
				values[key] = value
			}
		}
	}

	stats := make([]map[string]any, 0, len(characterStatRows))
	for _, row := range characterStatRows {
		stats = append(stats, map[string]any{
			"key":       row.Key,
			"label":     row.Label,
			"value":     values[row.Stat],
			"zeroBlock": row.Zero,
		})
	}

	inventoryLimit := values["stat_inventory_limit"]
	writeJSON(w, map[string]any{
		"characterId":              panelIDOfCharacter(character),
		"job":                      jobNumber(character),
		"level":                    character.Level,
		"growType":                 growTypeValue(character),
		"inventoryLimit":           inventoryLimit,
		"inventoryLimitOverridden": inventoryLimit == inventoryLimitMaxValue,
		"stats":                    stats,
	})
}

func (s *Server) computedCharacterStats(character dnfrepo.CharacterRecord) (map[string]int64, error) {
	table, err := s.pvf.characterStatTable(s)
	if err != nil {
		return nil, err
	}
	vector, err := table.Compute(byte(jobNumber(character)), byte(growTypeValue(character)), character.Level)
	if err != nil {
		return nil, err
	}
	vector = withCompatibilityBonus(vector)
	return statValuesFromVector(vector), nil
}

func withCompatibilityBonus(vector dnfcharstat.Vector) dnfcharstat.Vector {
	vector.HPMax += csharpPremiumHPBonus
	vector.MPMax += csharpPremiumMPBonus
	return vector
}

func statValuesFromVector(vector dnfcharstat.Vector) map[string]int64 {
	return map[string]int64{
		"stat_hp_max":             vector.HPMax,
		"stat_mp_max":             vector.MPMax,
		"stat_strength":           vector.Strength,
		"stat_intelligence":       vector.Intelligence,
		"stat_vitality":           vector.Vitality,
		"stat_spirit":             vector.Spirit,
		"stat_physical_attack":    vector.PhysicalAttack,
		"stat_physical_defense":   vector.PhysicalDefense,
		"stat_magical_attack":     vector.MagicalAttack,
		"stat_magical_defense":    vector.MagicalDefense,
		"stat_independent_attack": vector.IndependentAttack,
		"stat_fire_resistance":    vector.FireResistance,
		"stat_water_resistance":   vector.WaterResistance,
		"stat_dark_resistance":    vector.DarkResistance,
		"stat_light_resistance":   vector.LightResistance,
		"stat_inventory_limit":    vector.InventoryLimit,
		"stat_hp_regen_speed":     vector.HPRegenSpeed,
		"stat_mp_regen_speed":     vector.MPRegenSpeed,
		"stat_move_speed":         vector.MoveSpeed,
		"stat_attack_speed":       vector.AttackSpeed,
		"stat_cast_speed":         vector.CastSpeed,
		"stat_hit_recovery":       vector.HitRecovery,
		"stat_jump_power":         vector.JumpPower,
		"stat_weight":             vector.Weight,
	}
}

// ---------------------------------------------------------------------------
// SP / TP
// ---------------------------------------------------------------------------

func (s *Server) handleSpTp(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	group, err := s.repositories()
	if err != nil {
		writeFailure(w, err.Error())
		return
	}
	panelID, err := strconv.Atoi(strings.TrimSpace(r.PathValue("id")))
	if err != nil {
		writeFailure(w, "角色编号无效")
		return
	}
	character, err := s.loadCharacter(ctx, group, panelID)
	if err != nil {
		writeFailure(w, fmt.Sprintf("角色不存在: %d", panelID))
		return
	}
	skill, err := s.loadCharacterSkill(ctx, group, character.CharacterID)
	if err != nil {
		writeFailure(w, "技能点状态加载失败")
		return
	}
	writeOK(w, s.spTpPayload(character, skill))
}

func (s *Server) spTpPayload(character dnfrepo.CharacterRecord, skill dnfrepo.SkillRecord) map[string]any {
	_, awakening := dnfprofession.Decode(byte(growTypeValue(character)))
	treeUnlocked := awakening > 0
	skillTreeIndex := 255
	if treeUnlocked {
		skillTreeIndex = 0
		if statInt(character, "skill_tree_index") == 1 {
			skillTreeIndex = 1
		}
	}
	currentPage := 0
	if skillTreeIndex == 1 {
		currentPage = 1
	}
	points := skill.Points
	return map[string]any{
		"success":            true,
		"characterId":        panelIDOfCharacter(character),
		"skillTreeIndex":     skillTreeIndex,
		"skillTreeUnlocked":  treeUnlocked,
		"currentSkillPage":   currentPage,
		"totalSp":            points.TotalSP,
		"remainingSp":        points.RemainingSP,
		"remainingSpPage0":   points.RemainingSP,
		"remainingSpPage1":   points.RemainingSP,
		"currentRemainingSp": points.RemainingSP,
		"totalTp":            points.TotalTP,
		"remainingTp":        points.RemainingTP,
		"remainingTpPage0":   points.RemainingTP,
		"remainingTpPage1":   points.RemainingTP,
		"currentRemainingTp": points.RemainingTP,
		"bonusSp":            statInt(character, "bonus_sp"),
		"bonusTp":            statInt(character, "bonus_tp"),
	}
}

type spTpRequest struct {
	Sp int `json:"sp"`
	Tp int `json:"tp"`
}

func (s *Server) handleAdjustSpTp(w http.ResponseWriter, r *http.Request) {
	body, ok := decodeBody[spTpRequest](w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	group, err := s.repositories()
	if err != nil {
		writeFailure(w, err.Error())
		return
	}
	panelID, err := strconv.Atoi(strings.TrimSpace(r.PathValue("id")))
	if err != nil {
		writeFailure(w, "角色编号无效")
		return
	}
	character, err := s.loadCharacter(ctx, group, panelID)
	if err != nil {
		writeFailure(w, fmt.Sprintf("角色不存在: %d", panelID))
		return
	}
	skill, err := s.loadCharacterSkill(ctx, group, character.CharacterID)
	if err != nil {
		writeFailure(w, "技能点状态加载失败")
		return
	}

	character = dnfrepo.CloneCharacter(character)
	if character.Stats == nil {
		character.Stats = make(map[string]int64)
	}
	character.Stats["bonus_sp"] = statInt(character, "bonus_sp") + int64(body.Sp)
	character.Stats["bonus_tp"] = statInt(character, "bonus_tp") + int64(body.Tp)

	points, err := s.recomputeSkillPoints(character, skill.Points)
	if err != nil {
		writeFailure(w, err.Error())
		return
	}
	skill = dnfrepo.CloneSkill(skill)
	skill.Points = points

	if err := s.saveCharacterStats(ctx, group, character); err != nil {
		writeFailure(w, err.Error())
		return
	}
	if err := s.saveSkillPoints(ctx, group, skill); err != nil {
		writeFailure(w, err.Error())
		return
	}
	writeOK(w, s.spTpPayload(character, skill))
}

func (s *Server) handleZeroRemainingSpTp(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	group, err := s.repositories()
	if err != nil {
		writeFailure(w, err.Error())
		return
	}
	panelID, err := strconv.Atoi(strings.TrimSpace(r.PathValue("id")))
	if err != nil {
		writeFailure(w, "角色编号无效")
		return
	}
	character, err := s.loadCharacter(ctx, group, panelID)
	if err != nil {
		writeFailure(w, fmt.Sprintf("角色不存在: %d", panelID))
		return
	}
	skill, err := s.loadCharacterSkill(ctx, group, character.CharacterID)
	if err != nil {
		writeFailure(w, "技能点状态加载失败")
		return
	}
	if skill.Points.RemainingSP == 0 && skill.Points.RemainingTP == 0 {
		writeFailure(w, "当前没有可归零的剩余 SP/TP")
		return
	}
	skill = dnfrepo.CloneSkill(skill)
	skill.Points.RemainingSP = 0
	skill.Points.RemainingTP = 0
	if err := s.saveSkillPoints(ctx, group, skill); err != nil {
		writeFailure(w, err.Error())
		return
	}
	writeOK(w, s.spTpPayload(character, skill))
}

// recomputeSkillPoints 用 PVF 累计值 + 附加点重算总量，并保留已经花掉的点数。
// 90CN 只有一套技能页账本，因此这里不做分页处理。
func (s *Server) recomputeSkillPoints(character dnfrepo.CharacterRecord, previous dnfrepo.SkillPointState) (dnfrepo.SkillPointState, error) {
	tables, err := s.pvf.progressionTables(s)
	if err != nil {
		return dnfrepo.SkillPointState{}, err
	}
	totalSP, err := tables.TotalSkillPoints(character.Level)
	if err != nil {
		return dnfrepo.SkillPointState{}, fmt.Errorf("SP/TP 计算失败: %w", err)
	}
	totalTP, err := tables.TotalTechniquePoints(character.Level)
	if err != nil {
		return dnfrepo.SkillPointState{}, fmt.Errorf("SP/TP 计算失败: %w", err)
	}
	bonusSP := int(statInt(character, "bonus_sp"))
	bonusTP := int(statInt(character, "bonus_tp"))
	newTotalSP := totalSP + bonusSP
	newTotalTP := totalTP + bonusTP

	spentSP := previous.TotalSP - previous.RemainingSP
	spentTP := previous.TotalTP - previous.RemainingTP
	if spentSP < 0 {
		spentSP = 0
	}
	if spentTP < 0 {
		spentTP = 0
	}
	remainingSP := newTotalSP - spentSP
	remainingTP := newTotalTP - spentTP
	if remainingSP < 0 {
		return dnfrepo.SkillPointState{}, fmt.Errorf("减少 SP 会使技能方案剩余点数为负数（第一页 %d）", remainingSP)
	}
	if remainingTP < 0 {
		return dnfrepo.SkillPointState{}, fmt.Errorf("减少 TP 会使技能方案剩余点数为负数（第一页 %d）", remainingTP)
	}
	return dnfrepo.SkillPointState{
		TotalSP:     newTotalSP,
		RemainingSP: remainingSP,
		TotalTP:     newTotalTP,
		RemainingTP: remainingTP,
		SyncedLevel: character.Level,
	}, nil
}

// ---------------------------------------------------------------------------
// 等级
// ---------------------------------------------------------------------------

type levelRequest struct {
	Level int `json:"level"`
}

func (s *Server) handleSetLevel(w http.ResponseWriter, r *http.Request) {
	body, ok := decodeBody[levelRequest](w, r)
	if !ok {
		return
	}
	if body.Level < 1 || body.Level > maxCharacterLevel {
		writeFailure(w, fmt.Sprintf("等级范围 1-%d", maxCharacterLevel))
		return
	}
	ctx := r.Context()
	group, err := s.repositories()
	if err != nil {
		writeFailure(w, err.Error())
		return
	}
	panelID, err := strconv.Atoi(strings.TrimSpace(r.PathValue("id")))
	if err != nil {
		writeFailure(w, "角色编号无效")
		return
	}
	character, err := s.loadCharacter(ctx, group, panelID)
	if err != nil {
		writeFailure(w, fmt.Sprintf("角色不存在: %d", panelID))
		return
	}
	skill, err := s.loadCharacterSkill(ctx, group, character.CharacterID)
	if err != nil {
		writeFailure(w, "技能点状态加载失败")
		return
	}

	updated := dnfrepo.CloneCharacter(character)
	if updated.Stats == nil {
		updated.Stats = make(map[string]int64)
	}
	updated.Level = body.Level
	// 90CN 的 exp 是当前等级内的经验，升级后从 0 开始。
	updated.Stats["exp"] = 0

	points, err := s.recomputeSkillPoints(updated, skill.Points)
	if err != nil {
		writeFailure(w, err.Error())
		return
	}
	skill = dnfrepo.CloneSkill(skill)
	skill.Points = points

	if err := s.applyLevelStats(&updated); err != nil {
		writeFailure(w, err.Error())
		return
	}
	if err := s.saveCharacterStats(ctx, group, updated); err != nil {
		writeFailure(w, err.Error())
		return
	}
	if err := s.saveSkillPoints(ctx, group, skill); err != nil {
		writeFailure(w, err.Error())
		return
	}
	writeOK(w, map[string]any{
		"characterId": panelIDOfCharacter(updated),
		"level":       updated.Level,
		"exp":         updated.Stats["exp"],
	})
}

// applyLevelStats 按新等级重算属性镜像，同时保留 GM 设置的负重上限覆写。
func (s *Server) applyLevelStats(character *dnfrepo.CharacterRecord) error {
	if character == nil {
		return nil
	}
	computed, err := s.computedCharacterStats(*character)
	if err != nil {
		return fmt.Errorf("等级/经验/属性重算失败: %w", err)
	}
	override := statInt(*character, "stat_inventory_limit")
	if character.Stats == nil {
		character.Stats = make(map[string]int64)
	}
	for key, value := range computed {
		character.Stats[key] = value
	}
	if override == inventoryLimitMaxValue {
		character.Stats["stat_inventory_limit"] = override
	}
	return nil
}

// ---------------------------------------------------------------------------
// 转职 / 觉醒
// ---------------------------------------------------------------------------

func (s *Server) handleGrowOptions(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	group, err := s.repositories()
	if err != nil {
		writeFailure(w, err.Error())
		return
	}
	panelID, err := strconv.Atoi(strings.TrimSpace(r.PathValue("id")))
	if err != nil {
		writeFailure(w, "角色编号无效")
		return
	}
	character, err := s.loadCharacter(ctx, group, panelID)
	if err != nil {
		writeFailure(w, fmt.Sprintf("角色不存在: %d", panelID))
		return
	}
	profiles, _, err := s.pvf.professionResources(s)
	if err != nil {
		writeFailure(w, err.Error())
		return
	}

	currentJob := byte(jobNumber(character))
	requestedJob := currentJob
	if raw := strings.TrimSpace(r.URL.Query().Get("job")); raw != "" {
		value, convErr := strconv.Atoi(raw)
		if convErr != nil || value < 0 || value > 255 {
			writeFailure(w, "职业范围 0-255")
			return
		}
		requestedJob = byte(value)
	}
	first, second := 0, 0
	if requestedJob == currentJob {
		firstValue, secondValue := dnfprofession.Decode(byte(growTypeValue(character)))
		first, second = int(firstValue), int(secondValue)
	}

	options := s.growOptionsForJob(profiles, requestedJob)
	writeJSON(w, map[string]any{
		"characterId": panelIDOfCharacter(character),
		"currentJob":  int(currentJob),
		"job":         int(requestedJob),
		"first":       first,
		"second":      second,
		"jobs":        s.jobOptions(profiles),
		"options":     options,
	})
}

func (s *Server) jobOptions(profiles *dnfprofession.Profiles) []map[string]any {
	jobs := make([]map[string]any, 0, 16)
	if profiles == nil {
		return jobs
	}
	for job := 0; job <= 255; job++ {
		name, ok := profiles.DisplayName(byte(job), 0)
		if !ok {
			continue
		}
		jobs = append(jobs, map[string]any{
			"value": job,
			"label": name,
			"token": strconv.Itoa(job),
		})
	}
	return jobs
}

// growAwakeningStageLabel 把 PVF 的觉醒 stage（1/2/3）翻成面板上的中文标签。
// 与 PVF `[grow type]`/chr 里的 stage 序号一一对应，不做任何推断。
func growAwakeningStageLabel(stage int) string {
	switch stage {
	case 1:
		return "一次觉醒"
	case 2:
		return "二次觉醒"
	case 3:
		return "三次觉醒"
	default:
		return "觉醒 " + strconv.Itoa(stage)
	}
}

func (s *Server) growOptionsForJob(profiles *dnfprofession.Profiles, job byte) map[string]any {
	options := map[string]any{"baseName": nil, "growTypes": []any{}}
	if profiles == nil {
		return options
	}
	baseName, ok := profiles.DisplayName(job, 0)
	if !ok {
		return options
	}
	growTypes := make([]map[string]any, 0, 6)
	for first := 0; first <= 5; first++ {
		encoded, err := dnfprofession.Encode(byte(first), 0)
		if err != nil {
			continue
		}
		name, ok := profiles.DisplayName(job, encoded)
		if !ok {
			continue
		}
		awakenings := make([]string, 0, 3)
		// awakeningOptions 与 awakenings 同序同长，但带上"第几次觉醒"的显式标签：
		// 面板原来的下拉只显示名字（例如「大暗黑天」），操作者会把它当成二次觉醒，
		// 实际它是阿修罗的**一次**觉醒（PVF chr 的 stage 1）。这里把 stage 一起给出，
		// 前端渲染成「一次觉醒 · 大暗黑天」，避免再选错。
		awakeningOptions := make([]map[string]any, 0, 3)
		for stage := 1; stage <= 3; stage++ {
			stageValue, err := dnfprofession.Encode(byte(first), byte(stage))
			if err != nil {
				continue
			}
			stageName, ok := profiles.DisplayName(job, stageValue)
			if !ok || strings.TrimSpace(stageName) == "" {
				continue
			}
			awakenings = append(awakenings, stageName)
			awakeningOptions = append(awakeningOptions, map[string]any{
				"stage":      stage,
				"stageLabel": growAwakeningStageLabel(stage),
				"name":       stageName,
				"label":      growAwakeningStageLabel(stage) + " · " + stageName,
			})
		}
		growTypes = append(growTypes, map[string]any{
			"value":            first,
			"label":            name,
			"awakenings":       awakenings,
			"awakeningOptions": awakeningOptions,
		})
	}
	options["baseName"] = baseName
	options["growTypes"] = growTypes
	return options
}

type growTypeRequest struct {
	Job    *int `json:"job"`
	First  int  `json:"first"`
	Second int  `json:"second"`
}

func (s *Server) handleSetGrowType(w http.ResponseWriter, r *http.Request) {
	body, ok := decodeBody[growTypeRequest](w, r)
	if !ok {
		return
	}
	if body.Job != nil && (*body.Job < 0 || *body.Job > 255) {
		writeFailure(w, "职业范围 0-255")
		return
	}
	if body.First < 0 || body.First > 15 || body.Second < 0 || body.Second > 15 {
		writeFailure(w, "转职/觉醒范围必须为 0-15")
		return
	}
	ctx := r.Context()
	group, err := s.repositories()
	if err != nil {
		writeFailure(w, err.Error())
		return
	}
	panelID, err := strconv.Atoi(strings.TrimSpace(r.PathValue("id")))
	if err != nil {
		writeFailure(w, "角色编号无效")
		return
	}
	character, err := s.loadCharacter(ctx, group, panelID)
	if err != nil {
		writeFailure(w, fmt.Sprintf("角色不存在或写入失败: %d", panelID))
		return
	}

	if body.First > 0 && character.Level < 15 {
		writeFailure(w, "角色达到 15 级后才能转职")
		return
	}
	if body.Second >= 1 && character.Level < 50 {
		writeFailure(w, "角色达到 50 级后才能觉醒")
		return
	}
	if body.Second >= 2 && character.Level < 75 {
		writeFailure(w, "角色达到 75 级后才能二次觉醒")
		return
	}

	profiles, catalog, err := s.pvf.professionResources(s)
	if err != nil {
		writeFailure(w, err.Error())
		return
	}
	// 任务目录与任务仓储必须在写任何数据之前就绪：本接口承诺「覆写 grow 的同时
	// 把转职/觉醒任务状态改到一致」，缺少任务侧就写一半的状态没有意义。
	questCatalog, err := s.pvf.questCatalog(s)
	if err != nil {
		writeFailure(w, err.Error())
		return
	}
	if group.Quest == nil {
		writeFailure(w, "任务仓储不可用，无法同步转职/觉醒任务状态")
		return
	}
	currentJob := byte(jobNumber(character))
	targetJob := currentJob
	if body.Job != nil {
		targetJob = byte(*body.Job)
	}
	if _, ok := profiles.DisplayName(targetJob, 0); !ok {
		writeFailure(w, fmt.Sprintf("PVF 中找不到职业: %d", int(targetJob)))
		return
	}
	encoded, err := dnfprofession.Encode(byte(body.First), byte(body.Second))
	if err != nil {
		writeFailure(w, "转职/觉醒范围必须为 0-15")
		return
	}
	if _, ok := profiles.DisplayName(targetJob, encoded); !ok {
		if body.First == 0 {
			writeFailure(w, "未转职不能设置觉醒")
			return
		}
		writeFailure(w, fmt.Sprintf("PVF 中找不到该转职: job=%d, first=%d", int(targetJob), body.First))
		return
	}

	skill, err := s.loadCharacterSkill(ctx, group, character.CharacterID)
	if err != nil {
		writeFailure(w, "技能点状态加载失败")
		return
	}
	previousFirst, previousSecond := dnfprofession.Decode(byte(growTypeValue(character)))
	kind := dnfprofession.KindAwakening
	if byte(body.First) != previousFirst || targetJob != currentJob {
		kind = dnfprofession.KindClassChange
	}
	transition := dnfprofession.Transition{
		Kind:             kind,
		GrowNumber:       byte(body.First),
		PreviousGrowType: byte(growTypeValue(character)),
		NewGrowType:      encoded,
		FirstGrowType:    byte(body.First),
		AwakeningStage:   byte(body.Second),
	}
	_ = previousSecond

	updatedSkill, err := profiles.ApplySkillTransition(catalog, targetJob, character.Level, transition, skill, skill.Points)
	if err != nil {
		writeFailure(w, "等级/经验/属性重算失败: "+err.Error())
		return
	}

	updated := dnfrepo.CloneCharacter(character)
	if updated.Stats == nil {
		updated.Stats = make(map[string]int64)
	}
	updated.Job = strconv.Itoa(int(targetJob))
	updated.Stats["grow_type"] = int64(encoded)
	if err := s.applyLevelStats(&updated); err != nil {
		writeFailure(w, err.Error())
		return
	}
	if err := s.saveCharacterStats(ctx, group, updated); err != nil {
		writeFailure(w, err.Error())
		return
	}
	if err := s.saveSkillPoints(ctx, group, updatedSkill); err != nil {
		writeFailure(w, err.Error())
		return
	}
	// 最后同步转职/觉醒任务状态：目标集合与「旧成长段位已经完成的行」都来自
	// 运行时 PVF（quest.Catalog.ProfessionProgressionQuestIDs）。
	questPlan, err := s.syncProfessionQuestStates(
		ctx,
		group,
		updated.CharacterID,
		questCatalog.ProfessionProgressionQuestIDs(int(targetJob), int(encoded)),
		questCatalog.ProfessionProgressionQuestIDs(int(currentJob), growTypeValue(character)),
	)
	if err != nil {
		writeFailure(w, "角色数值已写入，但转职/觉醒任务状态同步失败: "+err.Error())
		return
	}
	writeOK(w, map[string]any{
		"characterId":       panelIDOfCharacter(updated),
		"job":               body.Job,
		"first":             body.First,
		"second":            body.Second,
		"skillsInitialized": true,
		"quests":            questPlan.payload(),
	})
}

// ---------------------------------------------------------------------------
// 金币上限 / 负重 / 个人仓库 / 特殊装备槽
// ---------------------------------------------------------------------------

// handleGoldLimit 90CN 服务端不维护「按等级的金币上限表」——这个限制由客户端
// 自己显示，服务端只做金币数值本身，因此这里如实返回失败状态。
func (s *Server) handleGoldLimit(w http.ResponseWriter, r *http.Request) {
	writeFailure(w, "90CN 服务端没有按等级的金币上限表（金币上限由客户端显示）。")
}

// handleGoldLimitMax 同样没有可写入的服务端上限。
func (s *Server) handleGoldLimitMax(w http.ResponseWriter, r *http.Request) {
	writeFailure(w, "90CN 服务端没有可升级的金币上限。")
}

func (s *Server) handleInventoryLimitMax(w http.ResponseWriter, r *http.Request) {
	ctx, group, panelID, character, ok := s.characterRequest(w, r)
	if !ok {
		return
	}
	updated := dnfrepo.CloneCharacter(character)
	if updated.Stats == nil {
		updated.Stats = make(map[string]int64)
	}
	updated.Stats["stat_inventory_limit"] = inventoryLimitMaxValue
	if err := s.saveCharacterStats(ctx, group, updated); err != nil {
		writeFailure(w, err.Error())
		return
	}
	writeOK(w, map[string]any{
		"characterId":    panelID,
		"inventoryLimit": inventoryLimitMaxValue,
		"displayValue":   inventoryLimitMaxValue / inventoryLimitDisplayUnit,
	})
}

func (s *Server) handleInventoryLimitRestore(w http.ResponseWriter, r *http.Request) {
	ctx, group, panelID, character, ok := s.characterRequest(w, r)
	if !ok {
		return
	}
	computed, err := s.computedCharacterStats(character)
	if err != nil {
		writeFailure(w, "角色属性数据不存在，无法恢复负重")
		return
	}
	limit := computed["stat_inventory_limit"]
	updated := dnfrepo.CloneCharacter(character)
	if updated.Stats == nil {
		updated.Stats = make(map[string]int64)
	}
	updated.Stats["stat_inventory_limit"] = limit
	if err := s.saveCharacterStats(ctx, group, updated); err != nil {
		writeFailure(w, err.Error())
		return
	}
	writeOK(w, map[string]any{"characterId": panelID, "inventoryLimit": limit})
}

func (s *Server) handlePersonalCargoMax(w http.ResponseWriter, r *http.Request) {
	ctx, group, panelID, character, ok := s.characterRequest(w, r)
	if !ok {
		return
	}
	if group.Settings == nil {
		writeFailure(w, "服务端设置仓储不可用")
		return
	}
	scope := dnfrepo.CharacterContainerStateScope(character.CharacterID)
	record, found, err := group.Settings.Load(ctx, scope)
	if err != nil {
		writeFailure(w, "读取容器状态失败")
		return
	}
	if !found {
		writeFailure(w, "该角色还没有容器状态记录（请先在游戏里打开一次个人仓库）")
		return
	}
	record = dnfrepo.CloneSettings(record)
	if record.Values == nil {
		record.Values = make(map[string]string)
	}
	record.Values["personal_cargo_list_param16"] = strconv.Itoa(int(personalCargoMaxSlots))
	if err := dnfrepo.SaveSettingsFields(ctx, group.Settings, record, dnfrepo.SettingsFieldValues); err != nil {
		writeFailure(w, "写入容器状态失败")
		return
	}
	writeOK(w, map[string]any{
		"characterId": panelID,
		"listType":    listTypePersonalCargo,
		"listParam16": personalCargoMaxSlots,
	})
}

// personalCargoMaxSlots 是客户端 op13 头部允许的最大个人仓库格数（8..200，步长 16）。
const personalCargoMaxSlots = 200

// avatarInventoryMaxExpansion 是客户端 op13 头部允许的最大装扮栏扩展值：
// 商城共 8 个阶段的装扮栏扩展券、每阶段 +7 格，合法值 0,7..56
// （见 dnfrepo 的 currentAvatarInventoryExpansion）。
const avatarInventoryMaxExpansion = 56

// handleAvatarInventoryMax 把角色的装扮栏扩展直接置到客户端允许的最大档位。
// 与主背包/个人仓库上限接口同构：只写容器状态，不消耗任何道具。
func (s *Server) handleAvatarInventoryMax(w http.ResponseWriter, r *http.Request) {
	ctx, group, panelID, character, ok := s.characterRequest(w, r)
	if !ok {
		return
	}
	if group.Settings == nil {
		writeFailure(w, "服务端设置仓储不可用")
		return
	}
	scope := dnfrepo.CharacterContainerStateScope(character.CharacterID)
	record, found, err := group.Settings.Load(ctx, scope)
	if err != nil {
		writeFailure(w, "读取容器状态失败")
		return
	}
	if !found {
		writeFailure(w, "该角色还没有容器状态记录（请先在游戏里打开一次装扮栏）")
		return
	}
	record = dnfrepo.CloneSettings(record)
	if record.Values == nil {
		record.Values = make(map[string]string)
	}
	record.Values["avatar_list_param16"] = strconv.Itoa(avatarInventoryMaxExpansion)
	if err := dnfrepo.SaveSettingsFields(ctx, group.Settings, record, dnfrepo.SettingsFieldValues); err != nil {
		writeFailure(w, "写入容器状态失败")
		return
	}
	writeOK(w, map[string]any{
		"characterId": panelID,
		"listType":    listTypeAvatar,
		"listParam16": avatarInventoryMaxExpansion,
	})
}

// handleUnlockEquipmentSlots 见 equipment_slots.go —— 槽位分组、位掩码与授权任务
// 的解析全部在那一个文件里，避免第二套「开槽」实现。

// handleUnlockDungeonPermissions 见 dungeon_permissions.go。

// ---------------------------------------------------------------------------
// 请求辅助
// ---------------------------------------------------------------------------

func (s *Server) characterRequest(w http.ResponseWriter, r *http.Request) (
	context.Context, dnfrepo.Group, int, dnfrepo.CharacterRecord, bool,
) {
	ctx := r.Context()
	group, err := s.repositories()
	if err != nil {
		writeFailure(w, err.Error())
		return ctx, group, 0, dnfrepo.CharacterRecord{}, false
	}
	panelID, err := strconv.Atoi(strings.TrimSpace(r.PathValue("id")))
	if err != nil {
		writeFailure(w, "角色编号无效")
		return ctx, group, 0, dnfrepo.CharacterRecord{}, false
	}
	character, err := s.loadCharacter(ctx, group, panelID)
	if err != nil {
		writeFailure(w, fmt.Sprintf("角色不存在: %d", panelID))
		return ctx, group, panelID, dnfrepo.CharacterRecord{}, false
	}
	return ctx, group, panelID, character, true
}
