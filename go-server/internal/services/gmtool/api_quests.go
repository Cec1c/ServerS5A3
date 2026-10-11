package gmtool

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"

	dnfquest "longheng.io/server/internal/modules/dnf/quest"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// 任务只读查询面（对应上游 0.1.8 dnfbridge/gm.go 的
// GMQuestOverview / GMSearchQuests / GMQuestDetail）。
//
// 纯读：不写库、不需要开关。所有文案都直接来自运行时 PVF 的
// [name] / [depend message] / [condition message] / [solve message]；
// 段不存在就是空串，界面显示「PVF 未提供」，这里**不合成任何替代文案**。
// 特别是：上游 gmQuestSummary 的降级链末位会写死 `任务 <id>`，本仓**不移植
// 那一档**（见交付报告「未做/未确证项」）。

// questReadContext 装载读取任务面所需的三样东西：角色任务账、PVF 任务目录、
// 以及该角色的资格投影（等级/职业/成长段）。缺失的任务账按空账处理（上游同）。
func (s *Server) questReadContext(
	ctx context.Context,
	group dnfrepo.Group,
	character dnfrepo.CharacterRecord,
) (dnfrepo.QuestRecord, *dnfquest.Catalog, dnfquest.CharacterEligibility, error) {
	record, found, err := group.Quest.Load(ctx, character.CharacterID)
	if err != nil {
		return dnfrepo.QuestRecord{}, nil, dnfquest.CharacterEligibility{}, fmt.Errorf("读取任务数据失败: %w", err)
	}
	if !found {
		record = dnfrepo.QuestRecord{CharacterID: character.CharacterID}
	}
	catalog, err := s.pvf.questCatalog(s)
	if err != nil {
		return dnfrepo.QuestRecord{}, nil, dnfquest.CharacterEligibility{}, err
	}
	// 上游 GMQuestOverview/GMSearchQuests/GMQuestDetail 都以角色存储的 job
	// 为资格输入，且 job 不可解析时直接报错——不能用 0 顶上，那会把鬼剑士
	// 的任务错误地标成可接。投影只有一处实现（P6 起 api_quest_mutations.go 的
	// questEligibilityFromCharacter，链路重置用同一函数）。
	eligibility, err := questEligibilityFromCharacter(character)
	if err != nil {
		return dnfrepo.QuestRecord{}, nil, dnfquest.CharacterEligibility{}, err
	}
	if record.CharacterID == character.CharacterID {
		eligibility, err = catalog.WithEpicRoadEvent350Eligibility(eligibility, s.deps.Activities != nil && s.deps.Activities.Enabled(350))
	}
	if err != nil {
		return dnfrepo.QuestRecord{}, nil, dnfquest.CharacterEligibility{}, err
	}
	return record, catalog, eligibility, nil
}

// acceptableQuestIDs 把 catalog.Acceptable 的结果转成集合，供逐条打标。
func acceptableQuestIDs(catalog *dnfquest.Catalog, character dnfquest.CharacterEligibility, record dnfrepo.QuestRecord) map[int64]struct{} {
	available := catalog.Acceptable(character, record)
	ids := make(map[int64]struct{}, len(available.IDs))
	for _, questID := range available.IDs {
		ids[int64(questID)] = struct{}{}
	}
	return ids
}

func (s *Server) handleQuestOverview(w http.ResponseWriter, r *http.Request) {
	ctx, group, panelID, character, ok := s.characterRequest(w, r)
	if !ok {
		return
	}
	record, catalog, _, err := s.questReadContext(ctx, group, character)
	if err != nil {
		writeFailure(w, err.Error())
		return
	}

	// 上游 GMQuestOverview 不看资格，逐条 acceptable 恒为 false：它列出的是
	// 「这个角色账上已经有什么」，不是「还能接什么」。
	seen := make(map[int64]struct{}, len(record.States)+len(record.Progress))
	active := make([]map[string]any, 0, len(record.States))
	completed := make([]map[string]any, 0, len(record.States))
	appendState := func(questID int64, state dnfrepo.QuestState) {
		if _, exists := seen[questID]; exists {
			return
		}
		seen[questID] = struct{}{}
		// PVF 里找不到的任务按「只有 ID 的空定义」呈现（上游同口径：静默降级，
		// 不报错、也不编造名称/路径）。
		definition := dnfquest.Definition{ID: questID}
		if known, found := catalog.Find(questID); found {
			definition = known
		}
		if isActiveQuestStatus(state.Status) {
			active = append(active, questSummaryPayload(definition, state, true, false))
		}
		if isCompletedQuestStatus(state.Status) {
			completed = append(completed, questSummaryPayload(definition, state, true, false))
		}
	}
	for questID, state := range record.States {
		appendState(questID, state)
	}
	for questID, state := range record.Progress {
		appendState(questID, state)
	}
	sortQuestSummaries(active)
	sortQuestSummaries(completed)

	payload := map[string]any{
		"characterId":    panelID,
		"activeCount":    len(active),
		"completedCount": len(completed),
		"active":         active,
		"completed":      completed,
	}
	// 批次 3 项 ⑥：把「已接任务批量完成」这条新路由的广告位挂在这里（只在
	// DNFBRIDGE_GM_QUEST_MODE 启用时出现；关闭档下这个键**不存在**，前端据此
	// 不渲染任何新控件 —— 面板与加这条路由之前逐字节相同）。既有字段一个不动。
	// 见 api_quest_active_batch.go 的文件头。
	if advertisement := questCompletionBatchAdvertisement(); advertisement != nil {
		payload["completionBatch"] = advertisement
	}
	writeOK(w, payload)
}

func (s *Server) handleQuestSearch(w http.ResponseWriter, r *http.Request) {
	ctx, group, panelID, character, ok := s.characterRequest(w, r)
	if !ok {
		return
	}
	record, catalog, eligibility, err := s.questReadContext(ctx, group, character)
	if err != nil {
		writeFailure(w, err.Error())
		return
	}
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	// limit 的钳制在 dnfquest.Catalog.Search 内（<=0 → 30，>200 → 200），
	// 与上游同一处口径；这里传 0 表示「用默认」。
	limit := queryInt(r, "limit", 0)
	definitions := catalog.Search(query, limit)
	acceptable := acceptableQuestIDs(catalog, eligibility, record)

	results := make([]map[string]any, 0, len(definitions))
	for _, definition := range definitions {
		state, hasState := questStateLookup(record, definition.ID)
		// 上游在搜索面里也允许「已经在做/已经做完」的任务出现，但只把真正
		// 还能接的标成 acceptable（questSummaryPayload 内部会收敛这一点）。
		_, canAccept := acceptable[definition.ID]
		results = append(results, questSummaryPayload(definition, state, hasState, canAccept))
	}
	writeOK(w, map[string]any{
		"characterId": panelID,
		"query":       query,
		"count":       len(results),
		"results":     results,
	})
}

func (s *Server) handleQuestDetail(w http.ResponseWriter, r *http.Request) {
	questID, err := strconv.ParseInt(strings.TrimSpace(r.PathValue("questId")), 10, 64)
	if err != nil || questID <= 0 || questID > math.MaxUint16 {
		writeFailure(w, fmt.Sprintf("任务编号无效: %s (允许范围 1..%d)", r.PathValue("questId"), uint64(math.MaxUint16)))
		return
	}
	ctx, group, panelID, character, ok := s.characterRequest(w, r)
	if !ok {
		return
	}
	record, catalog, eligibility, err := s.questReadContext(ctx, group, character)
	if err != nil {
		writeFailure(w, err.Error())
		return
	}
	definition, found := catalog.Find(questID)
	if !found {
		writeFailure(w, fmt.Sprintf("任务 %d 不在当前运行时 Script.pvf 的任务表里", questID))
		return
	}

	state, hasState := questStateLookup(record, questID)
	acceptable := false
	if !isActiveQuestStatus(state.Status) && !isCompletedQuestStatus(state.Status) {
		_, acceptable = acceptableQuestIDs(catalog, eligibility, record)[questID]
	}
	summary := questSummaryPayload(definition, state, hasState, acceptable)

	payload := map[string]any{
		"characterId": panelID,
		"quest":       summary,
		// 三段展示文本直接取 PVF 原文；不存在的段是空串，前端显示「PVF 未提供」。
		"introduction": strings.TrimSpace(definition.DependMessage),
		"objective":    strings.TrimSpace(definition.ConditionMessage),
		"completion":   strings.TrimSpace(definition.SolveMessage),
		"difficulty":   strings.TrimSpace(definition.Difficulty),
		"job":          strings.TrimSpace(definition.Job),
		"noExperience": definition.NoExperience,
		"goldMultiple": definition.GoldMultiple,
	}
	rewardItems := s.questRewardItems(definition.RewardItems, false)
	selectionItems := s.questRewardItems(definition.RewardSelectionItems, true)
	// 上游的 title 奖励回退：奖励类型归一化后是 title、且 [reward int data] 至少
	// 两项时，把前两项当成 (itemID, count) 展示（gm.go:1034-1036）。
	if len(rewardItems) == 0 && normalizeQuestRewardTag(definition.RewardType) == "title" && len(definition.RewardIntData) >= 2 {
		if item := s.questRewardItem(definition.RewardIntData[0], definition.RewardIntData[1], false); item != nil {
			rewardItems = []map[string]any{item}
		}
	}
	if len(rewardItems) > 0 {
		payload["rewardItems"] = rewardItems
	}
	if len(selectionItems) > 0 {
		payload["rewardSelectionItems"] = selectionItems
	}
	writeOK(w, payload)
}

// questStateLookup 复刻上游 gmQuestState：先看 States，再看 Progress。
func questStateLookup(record dnfrepo.QuestRecord, questID int64) (dnfrepo.QuestState, bool) {
	if state, ok := record.States[questID]; ok {
		return state, true
	}
	if state, ok := record.Progress[questID]; ok {
		return state, true
	}
	return dnfrepo.QuestState{}, false
}

func sortQuestSummaries(rows []map[string]any) {
	sort.SliceStable(rows, func(i, j int) bool {
		left, _ := rows[i]["questId"].(int64)
		right, _ := rows[j]["questId"].(int64)
		return left < right
	})
}

// questSummaryPayload 是上游 gmQuestSummary 的等价投影。
//
// 与上游的唯一差异：`name` 只回 PVF [name] 的原文（缺段即空串），**不**走上游
// 那三级降级链的末位（写死 `任务 <id>`）——那属于「PVF 未提供时编数据」。
// 缺名时 `path` 仍然可用，前端据此显示「PVF 未提供任务名」。
func questSummaryPayload(
	definition dnfquest.Definition,
	state dnfrepo.QuestState,
	hasState bool,
	acceptable bool,
) map[string]any {
	active := isActiveQuestStatus(state.Status)
	completed := isCompletedQuestStatus(state.Status)
	status := strings.TrimSpace(state.Status)
	if status == "" {
		if acceptable {
			status = "available"
		} else {
			status = "not_started"
		}
	}
	prerequisites := definition.PreRequiredGroups
	if prerequisites == nil {
		prerequisites = [][]int64{}
	}
	collisions := definition.CollisionQuests
	if collisions == nil {
		collisions = []int64{}
	}
	progress := int64(0)
	if hasState {
		progress = state.ProgressValue
	}
	return map[string]any{
		"questId":         definition.ID,
		"name":            strings.TrimSpace(definition.Name),
		"path":            definition.Path,
		"grade":           definition.Grade,
		"type":            definition.Type,
		"levelMin":        definition.LevelMin,
		"levelMax":        definition.LevelMax,
		"status":          status,
		"progressValue":   progress,
		"active":          active,
		"completed":       completed,
		"acceptable":      acceptable && !active && !completed,
		"jobChangeQuest":  definition.JobChangeQuest,
		"rewardType":      definition.RewardType,
		"professionQuest": isProfessionRewardType(definition.RewardType),
		"mainQuestId":     definition.MainQuestID,
		"prerequisites":   prerequisites,
		"collisions":      collisions,
		"cantGiveUp":      definition.CantGiveUp,
	}
}

func (s *Server) questRewardItems(rules []dnfquest.RewardItemRule, selectable bool) []map[string]any {
	if len(rules) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(rules))
	for _, rule := range rules {
		item := s.questRewardItem(rule.ItemID, rule.Count, selectable)
		if item == nil {
			continue
		}
		item["hasJobFilter"] = rule.HasJobFilter
		item["requiredJob"] = rule.Job
		item["requiredGrowType"] = rule.GrowType
		out = append(out, item)
	}
	return out
}

// questRewardItem 解析一条奖励物品。名称来自本仓 PVF 物品索引；索引里没有
// 就留空（前端不显示名字），不造名字、也不因此丢掉这一行。
func (s *Server) questRewardItem(itemID, count int64, selectable bool) map[string]any {
	if itemID <= 0 || itemID > math.MaxUint32 || count <= 0 {
		return nil
	}
	return map[string]any{
		"itemId":     itemID,
		"name":       s.items.name(uint32(itemID)),
		"count":      count,
		"selectable": selectable,
	}
}

// normalizeQuestRewardTag 与 modules/dnf/quest 的 normalizeQuestTag 同口径：
// 去掉反引号与方括号、折叠空白、转小写。
func normalizeQuestRewardTag(value string) string {
	value = strings.ToLower(strings.TrimSpace(strings.ReplaceAll(value, "`", "")))
	return strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(value, "["), "]"))
}

// isProfessionRewardType 等价上游 gmProfessionRewardType（gm.go:2095）：
// 只认 [grow type] / [awakening type] 两个字面，绝不用 job id 猜。
// 实现已收敛到 profession_quests.go 的 IsProfessionQuestReward（P5 起导出，
// 读面与写面共用同一处判定）。
func isProfessionRewardType(value string) bool {
	return IsProfessionQuestReward(value)
}

// isActiveQuestStatus / isCompletedQuestStatus 与 modules/dnf/quest 的
// normalizeStatus + isActiveQuestStatus/isCompletedQuestStatus 同口径，并补齐
// 上游 gmQuestStatusActive/gmQuestStatusCompleted 多出来的同义词，取两者并集，
// 保证「服务端认为在做/已完成的任务」在面板里不会被显示成别的状态。
func isActiveQuestStatus(status string) bool {
	switch normalizeQuestStatus(status) {
	case "active", "accepted", "inprogress", "progress":
		return true
	default:
		return false
	}
}

func isCompletedQuestStatus(status string) bool {
	switch normalizeQuestStatus(status) {
	case "complete", "completed", "cleared", "finished", "done", "rewarded":
		return true
	default:
		return false
	}
}

func normalizeQuestStatus(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, "_", "")
	value = strings.ReplaceAll(value, "-", "")
	return strings.ReplaceAll(value, " ", "")
}
