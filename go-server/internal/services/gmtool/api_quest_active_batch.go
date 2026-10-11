package gmtool

// 批次 3 项 ⑥：任务完成页补齐 —— 「批量完成当前已接任务」+「已接任务逐条单个完成」。
//
// # 缺口（先查证，再移植；对照上游 A21 GM 工具 Temp\gm-upstream-v260910\）
//
// 查证结论（本仓 vs 上游，逐条给上游文件行号）：
//
//	① 当前已接任务列表：**本仓已有**（GET /api/characters/{id}/quests 的 active 数组，
//	   api_quests.go:77-105）↔ 上游 ListQuests（Services/GmService.Quests.cs:16-49，
//	   SELECT … FROM character_active_quests）。不缺。
//	② 已接任务逐条完成：**本仓已有服务端**（POST …/quests/{questId}/complete，
//	   api_quest_mutations.go:598-833，对应上游 ForceCompleteQuest :1213-1273），
//	   但面板上只有「详情」页里才有一个按钮 —— 进行中列表的每一行**没有任何操作按钮**
//	   （wwwroot/js/quests.js:31-40 的 questRowHtml 只渲染「详情」）↔ 上游进行中表
//	   每行都有「标记可交 / 强制完成」两个按钮（wwwroot/js/quests.js:22-31）。**缺 UI**。
//	③ 批量完成：本仓只有 POST …/quests/complete-batch，**必须由操作者手工敲 ID**
//	   （api_quest_mutations.go:1206、wwwroot/index.html:485），既不能按「当前已接」
//	   一键完成，也不能在列表里勾选 ↔ 上游有三条：GMCompleteQuestBatch
//	   （Services/GmService.Quests.cs:1277-1295）、CompleteVisibleQuestBatch
//	   （:1297-1344，按前端传的可见任务集批量、跳过每日任务并逐条回报 skipped）、
//	   以及按分类的服务端派生批量 CompleteCurrentLevelQuestGroup（:1853-1917）。
//	   **缺「以角色已接任务为输入」的批量子集**，也缺勾选式 UI。
//
// 因此本文件新增一条路由（服务端派生输入，前端可勾选或一键全部）：
//
//	POST /api/characters/{id}/quests/active/complete-batch
//	body   {"questIds":[3972,4002]}   // 可选；缺省/空 = 用角色当前已接（active）的任务
//	响应   恒 HTTP 200；成功 {"success":true,…}，失败 {"success":false,"error":"…"}
//
// 上游命名法：`<范围>/complete-batch`（上游是 `/quests/all-visible/complete-batch`，
// Program.cs:350）。本仓把范围定为「角色已接」= active。
//
// # 语义（逐条对齐上游，差异都写在下面）
//
//	合法状态   只有**该角色真的已经接下（active）**、且**不在完成态**的任务会被写。
//	           这与上游 MarkQuestReady 的「该角色没有进行中的任务」（:79-80）是同一条
//	           规则；本路由把它推广成批量口径：没接过的任务若被显式点名，
//	           逐条回报 outcome=rejected / reason=not_accepted，并**零写入**。
//	幂等       已经是完成态的行一个字节都不改，逐条回报 already_completed；
//	           派生档重复调用时「已接集合」已经空了，changed=0 且**不开事务**。
//	奖励       上游 ForceCompleteQuest:1212 的注释就是「不发奖励」——它只删进行中行、
//	           写已完成标记；转职/觉醒由 ApplyGrowTypeFromQuest 单独同步，称号由
//	           DeliverTitleIfBookShell 走成就链。**本仓不发奖励**，也不发明第二套发放：
//	           奖励的正路是本仓 dnfquest.Owner 的结算路径（api_quest_mutations.go 的文件头
//	           第 2 条已声明 GM 强制完成从不发奖励），本文件沿用同一条既有实现，逐字写
//	           gm_rewards_granted=false（与 complete/complete-batch 同一处常量）。
//	           职业任务（[reward type] 为 grow/awakening）因此**排除**：那不是「发奖励」
//	           的问题，而是会改角色职业账（本仓由 DNFBRIDGE_GM_QUEST_PROFESSION_WRITE=on
//	           单独把关，见 handleQuestForceComplete:610-627），批量子集一律不碰。
//	每日任务   逐字移植上游 IsDailyQuestGrade（Services/GmService.Quests.cs:365-370）：
//	           daily / daily random / special daily 三种 grade 被跳过并点名原因
//	           （上游 CompleteVisibleQuestBatch:1320-1325 的 skippedDaily）。
//	           `normaly repeat` **不算**每日（上游 repeatable 是另一个判定 :357-363）。
//	逐条回报   上游 CompleteVisibleQuestBatch 是「能做的做、做不了的进 skipped[] 且
//	           success:true」；本文件照抄这个口径（results[] 逐条给 outcome/reason）。
//	           本仓既有的 complete-batch 是**全有或全无**（api_quest_mutations.go:1286-1310，
//	           那是有意的收紧），两条路由分工保持不变：要「一条不合格就整批不动」用
//	           complete-batch，要「把已接任务一次做完、跳过并如实报告」用本路由。
//	上限       questBatchMaxIDs（与既有 complete-batch 同一处常量，200 条）。上游可见集
//	           批量放到 2000（:1301-1302），本仓收敛到 200 是因为整批在**一个事务**里提交
//	           （同 complete-batch）；派生档超过 200 时明确拒绝、零写入，并提示先分批。
//	事务       单事务、单写入点（Group.CharacterSettlement.WithinCharacterSettlement），
//	           与 complete-batch:1328-1382 同形；事务内不做发奖励、不做职业跃迁。
//
// # 开关（默认启用，可一键回退）
//
//	`DNFBRIDGE_GM_QUEST_MODE`
//	  未设置 / 空 / 任何未知值 = **启用**（默认；本条路由注册，行为如上）
//	  `off` / `0` / `false` / `no` / `disable` / `disabled` / `none` / `legacy`
//	                           = **关闭**：routes() 里这一行**不执行**，请求继续落
//	                             `/api/` 兜底「该功能未移植到 90CN 面板…」；同时
//	                             GET /api/characters/{id}/quests **不再回 completionBatch**
//	                             字段，前端据此不渲染本批的任何新控件 —— 面板与加这条
//	                             路由之前**逐字节相同**（这就是文档里那条回退档）。
//
// 只管**本批新增**的这一条路由与新 UI：既有 6 条任务写路由仍由它们自己的
// DNFBRIDGE_GM_QUEST_MUTATE 把关（本文件一行都不改它们）。
// 解析函数 QuestModeFromEnv 沿用 CharacterRenameFromEnv（api_character_rename.go:181-188）
// 与 QuestMutateFromEnv（api_quest_mutations.go:110-117）同一条「默认启用 + 显式 off 字面量
// 回退」约定。开关自读环境（os.Getenv），不动 dnfbridge/options.go。

import (
	"fmt"
	"math"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

const (
	// questModeEnv 是任务完成页（本批新增路由 + 新 UI）的开关名。**默认启用**；
	// 只有 QuestModeFromEnv 列出的显式字面量才关闭。
	questModeEnv = "DNFBRIDGE_GM_QUEST_MODE"

	// questActiveBatchCompletedKey 是写进任务行 Extra 的「本次由已接任务批量完成」标记
	// （键名与既有 gm_batch_completed / gm_force_completed 同一命名法，值同为 "true"）。
	questActiveBatchCompletedKey = "gm_active_batch_completed"

	// questActiveBatchRoute 是前端要在列表里点的那条路径（只用于响应/日志/静态检查）。
	questActiveBatchRoute = "POST /api/characters/{id}/quests/active/complete-batch"
)

// QuestModeFromEnv 解析 DNFBRIDGE_GM_QUEST_MODE。
//
// 与 CharacterRenameFromEnv（api_character_rename.go:181）同一条默认档约定：
// **空 = on**（新路由默认可用）；只有显式 off/0/false/no/disable/disabled/none/legacy
// 才关闭。未知值（笔误）取默认档 on —— 与「回退必须写成显式字面量」一致。
func QuestModeFromEnv(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "off", "0", "false", "no", "disable", "disabled", "none", "legacy":
		return false
	default:
		return true
	}
}

// QuestModeFromEnvironment 读取进程环境里的同一个开关。
func QuestModeFromEnvironment() bool {
	return QuestModeFromEnv(os.Getenv(questModeEnv))
}

// questModeEnabled 是路由注册、overview 广告位与 handler 共用的同一个判断（不重写第二套）。
func (s *Server) questModeEnabled() bool {
	return QuestModeFromEnvironment()
}

// rejectQuestModeDisabled 是关闭档的统一拒绝（零校验、零写入）。
//
// 关闭档下路由**根本不注册**，这里只对「handler 被直接调用」（单测、将来别的调用点）
// 负责：口径与 rejectQuestMutateDisabled（api_quest_mutations.go:148）逐字同形。
func (s *Server) rejectQuestModeDisabled(w http.ResponseWriter, action string, panelID int) {
	raw := strings.TrimSpace(os.Getenv(questModeEnv))
	s.logEvent("gm-quest-mode-disabled", "action", action, "panel_id", panelID,
		"env", questModeEnv, "value", raw)
	writeFailure(w, fmt.Sprintf(
		"任务完成页的批量完成已关闭（%s=%q）。把它设为空或 on 才允许；本次未做任何写入。",
		questModeEnv, raw))
}

// questCompletionBatchAdvertisement 是本批新 UI 的**唯一开关广告位**：
// 只在开关启用时返回非 nil，由 GET /api/characters/{id}/quests 挂到响应上。
//
// 前端据此决定是否渲染「逐条强制完成」「勾选批量完成」这些控件（wwwroot/js/quests.js）：
// 关闭档下这个字段不存在 → 前端不渲染 → 面板与今天逐字节相同。字段是**新增**的，
// 既有 activeCount/completedCount/active/completed 一个字节都不动。
func questCompletionBatchAdvertisement() map[string]any {
	if !QuestModeFromEnvironment() {
		return nil
	}
	return map[string]any{
		"mode":    "enabled",
		"enabled": true,
		"route":   questActiveBatchRoute,
		"path":    "/api/characters/{id}/quests/active/complete-batch",
		"maxIds":  questBatchMaxIDs,
		// 与后端一致的排除项（逐条回报 reason，不是静默丢掉）。
		"excludes": []string{"daily", "profession"},
		"note": "只完成角色当前已接（active）并且未完成的任务；不发奖励（gm_rewards_granted=false）；" +
			"空 questIds = 全部已接任务；每日任务与职业任务会被跳过并逐条回报原因。",
	}
}

// isDailyQuestGrade 逐字移植上游 IsDailyQuestGrade（Services/GmService.Quests.cs:365-370）。
//
// 只认 daily / daily random / special daily 三个字面量：`normaly repeat` 在上游是
// repeatable（:357-363），**不在**每日批量排除范围内，因此这里也不能顺手扩进去。
// grade 的取值在 PVF 里带反引号与方括号，归一化复用只读面已有的 normalizeQuestRewardTag
// （api_quests.go:316，与 modules/dnf/quest 的 normalizeQuestTag 同口径）。
func isDailyQuestGrade(grade string) bool {
	switch normalizeQuestRewardTag(grade) {
	case "daily", "daily random", "special daily":
		return true
	default:
		return false
	}
}

// activeQuestIDs 取「角色当前已接（active）」的任务号，升序去重。
//
// 状态判定复用只读面同一处 isActiveQuestStatus（api_quests.go:333），不写第二套；
// 表的优先级与 questStateLookup（api_quests.go:208）一致：States 先、Progress 后，
// 且同一个 id 出现两次时以 States 那份为准（不会因为 Progress 里的残留行把它算成已接）。
func activeQuestIDs(record dnfrepo.QuestRecord) []int64 {
	seen := make(map[int64]struct{}, len(record.States)+len(record.Progress))
	ids := make([]int64, 0, len(record.States))
	for questID, state := range record.States {
		seen[questID] = struct{}{}
		if isActiveQuestStatus(state.Status) {
			ids = append(ids, questID)
		}
	}
	for questID, state := range record.Progress {
		if _, dup := seen[questID]; dup {
			continue
		}
		seen[questID] = struct{}{}
		if isActiveQuestStatus(state.Status) {
			ids = append(ids, questID)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

// questActiveBatchRequest 是请求体。camelCase 为主，另接受上游风格的下划线别名
// （与 questBatchRequest，api_quest_mutations.go:1179-1182 同一手法）。
//
// QuestIDs 缺省/空 = 「以角色当前已接任务为准」；给了就只处理这些编号（每条仍必须是
// 该角色**已经接下**的任务，否则逐条 not_accepted 拒绝）。
type questActiveBatchRequest struct {
	QuestIDs    []int64 `json:"questIds"`
	QuestIDsAlt []int64 `json:"quest_ids"`
}

// handleQuestActiveCompleteBatch 实现 POST /api/characters/{id}/quests/active/complete-batch。
func (s *Server) handleQuestActiveCompleteBatch(w http.ResponseWriter, r *http.Request) {
	if !s.questModeEnabled() {
		s.rejectQuestModeDisabled(w, "quest-active-complete-batch", 0)
		return
	}
	ctx, group, panelID, character, ok := s.characterRequest(w, r)
	if !ok {
		return
	}
	body, ok := decodeBody[questActiveBatchRequest](w, r)
	if !ok {
		return
	}
	if !s.requireOffline(w, "quest-active-complete-batch", panelID) {
		return
	}
	// 任务 owner 的主键是 uint16（与 questMutationTarget:215-223 同一处口径）。
	if panelID <= 0 || panelID > math.MaxUint16 {
		writeFailure(w, fmt.Sprintf(
			"面板角色号 %d 超出任务 owner 的 uint16 范围（1..%d），已拒绝；本次未做任何写入。",
			panelID, uint64(math.MaxUint16)))
		return
	}
	record, catalog, _, err := s.questReadContext(ctx, group, character)
	if err != nil {
		writeFailure(w, err.Error())
		return
	}

	requested := body.QuestIDs
	if len(requested) == 0 {
		requested = body.QuestIDsAlt
	}
	// 派生档：没有给编号 = 以角色当前已接任务为输入（本批要补的正就是这个入口）。
	derived := len(requested) == 0
	activeIDs := activeQuestIDs(record)
	if derived {
		requested = activeIDs
	}
	if len(requested) > questBatchMaxIDs {
		s.logEvent("gm-quest-active-batch-rejected", "panel_id", panelID, "character_id", character.CharacterID,
			"reason", "batch_size", "requested", len(requested), "max", questBatchMaxIDs, "derived", derived)
		writeJSON(w, map[string]any{
			"success":     false,
			"characterId": panelID,
			"reason":      "batch_size",
			"requested":   len(requested),
			"max":         questBatchMaxIDs,
			"error": fmt.Sprintf(
				"一次最多批量完成 %d 个已接任务，当前 %d 个（本批未做任何写入）。请分批操作，或改用逐条完成按钮。",
				questBatchMaxIDs, len(requested)),
		})
		return
	}

	// ---- 事务前逐条预检（零写入）----
	// 去重保序（与 complete-batch:1247-1256 同手法：上游用 map 且随机序，本仓按请求顺序）。
	unique := make([]int64, 0, len(requested))
	entries := make([]questBatchEntry, 0, len(requested))
	planned := make([]int64, 0, len(requested))
	seen := make(map[int64]struct{}, len(requested))
	for _, questID := range requested {
		if _, duplicate := seen[questID]; duplicate {
			continue
		}
		seen[questID] = struct{}{}
		unique = append(unique, questID)
		entry := questBatchEntry{questID: questID}
		switch {
		case questID <= 0 || questID > math.MaxUint16:
			entry.outcome = "rejected"
			entry.reason = "out_of_range"
		default:
			definition, found := catalog.Find(questID)
			switch {
			case !found:
				// 与 questMutationTarget:229-235 同一口径：PVF 里没有的任务一律报错，不合成定义。
				entry.outcome = "rejected"
				entry.reason = "not_in_script_pvf"
			default:
				entry.name = strings.TrimSpace(definition.Name)
				entry.path = definition.Path
				state, known := questStateLookup(record, questID)
				entry.status = strings.TrimSpace(state.Status)
				switch {
				case !known || (!isActiveQuestStatus(state.Status) && !isCompletedQuestStatus(state.Status)):
					// 本路由的**核心合法性门**：只有角色真的已经接下的任务才允许从这里完成
					// （上游 MarkQuestReady:79-80「该角色没有进行中的任务」是同一条规则）。
					entry.outcome = "rejected"
					entry.reason = "not_accepted"
				case isCompletedQuestStatus(state.Status):
					// 幂等：已完成的行一个字节都不改（与 complete-batch:1353-1359 同口径）。
					entry.outcome = "already_completed"
					entry.reason = "already_completed"
				case IsProfessionQuestReward(definition.RewardType):
					entry.outcome = "rejected"
					entry.reason = "profession_excluded"
				case isDailyQuestGrade(definition.Grade):
					entry.outcome = "rejected"
					entry.reason = "daily_excluded"
				default:
					entry.outcome = "pending"
					planned = append(planned, questID)
				}
			}
		}
		entries = append(entries, entry)
	}

	if len(planned) > 0 && group.CharacterSettlement == nil {
		s.writeQuestActiveBatchFailure(w, panelID, character.CharacterID, requested, unique, entries,
			"settlement_unavailable", "任务结算事务能力未就绪（CharacterSettlement）；本批未做任何写入。")
		return
	}

	// ---- 单事务、单写入点（同 complete-batch:1328-1382）----
	changed := 0
	if len(planned) > 0 {
		index := make(map[int64]int, len(entries))
		for position := range entries {
			index[entries[position].questID] = position
		}
		err = group.CharacterSettlement.WithinCharacterSettlement(ctx, character.CharacterID, func(tx dnfrepo.Group) error {
			if tx.Character == nil || tx.Quest == nil {
				return errQuestSettlementUnavailable
			}
			if _, found, loadErr := tx.Character.Load(ctx, character.CharacterID); loadErr != nil {
				return loadErr
			} else if !found {
				return errCharacterMissingForQuest
			}
			current, found, loadErr := tx.Quest.Load(ctx, character.CharacterID)
			if loadErr != nil {
				return loadErr
			}
			if !found {
				current = dnfrepo.QuestRecord{CharacterID: character.CharacterID}
			} else {
				current = dnfrepo.CloneQuest(current)
			}
			if current.States == nil {
				current.States = make(map[int64]dnfrepo.QuestState)
			}
			now := time.Now().UTC()
			for _, questID := range planned {
				definition, _ := catalog.Find(questID)
				state, known := questStateLookup(current, questID)
				position := index[questID]
				if !known || !isActiveQuestStatus(state.Status) {
					// 预检之后、提交之前状态被别的路径改过（并发）：这一条不写，如实回报。
					entries[position].outcome = "rejected"
					entries[position].reason = "state_changed"
					entries[position].status = strings.TrimSpace(state.Status)
					continue
				}
				next := state
				next.Extra = copyQuestExtra(state.Extra)
				next.Status = questCompletedLiteral
				next.ProgressValue = 0
				next.UpdatedAt = now
				next.Extra[questPVFPathKey] = definition.Path
				next.Extra[questActiveBatchCompletedKey] = "true"
				// 与 complete:793 / complete-batch:1367 同一句话：GM 批量完成**不发奖励**。
				next.Extra[questRewardsGrantedKey] = "false"
				// 清掉四个「还有一个待领奖励」的结算回执键（同 complete:794-796）。
				for _, key := range questCompletionExtraKeys {
					delete(next.Extra, key)
				}
				current.States[questID] = next
				delete(current.Progress, questID)
				entries[position].outcome = "completed"
				entries[position].status = questCompletedLiteral
				changed++
			}
			if changed == 0 {
				// 全部都在提交前失效/已完成：零写入（本仓幂等口径，与 complete-batch:1374-1377 同）。
				return nil
			}
			current.CharacterID = character.CharacterID
			current.UpdatedAt = now
			return dnfrepo.SaveQuestFields(ctx, tx.Quest, current,
				dnfrepo.QuestFieldStates, dnfrepo.QuestFieldProgress)
		})
		if err != nil {
			s.writeQuestActiveBatchFailure(w, panelID, character.CharacterID, requested, unique, entries,
				"write_failed", "批量完成写入失败（整批回滚）: "+err.Error())
			return
		}
	}

	changedIDs := make([]int64, 0, changed)
	unchangedIDs := make([]int64, 0, len(unique)-changed)
	notAccepted := make([]int64, 0, 4)
	alreadyCompleted := make([]int64, 0, 4)
	skippedDaily := make([]int64, 0, 2)
	skippedProfession := make([]int64, 0, 2)
	for _, entry := range entries {
		if entry.outcome == "completed" {
			changedIDs = append(changedIDs, entry.questID)
			continue
		}
		unchangedIDs = append(unchangedIDs, entry.questID)
		switch entry.reason {
		case "not_accepted":
			notAccepted = append(notAccepted, entry.questID)
		case "already_completed":
			alreadyCompleted = append(alreadyCompleted, entry.questID)
		case "daily_excluded":
			skippedDaily = append(skippedDaily, entry.questID)
		case "profession_excluded":
			skippedProfession = append(skippedProfession, entry.questID)
		}
	}
	s.logEvent("gm-quest-active-complete-batch", "panel_id", panelID, "character_id", character.CharacterID,
		"derived", derived, "requested", len(requested), "unique", len(unique), "changed", changed,
		"unchanged", len(unchangedIDs), "changed_ids", int64ListText(changedIDs),
		"skipped_daily", int64ListText(skippedDaily), "not_accepted", int64ListText(notAccepted))
	writeOK(w, map[string]any{
		"characterId":       panelID,
		"operation":         "active-complete-batch",
		"mode":              "enabled",
		"route":             questActiveBatchRoute,
		"derived":           derived,
		"requested":         len(requested),
		"unique":            len(unique),
		"changed":           changed,
		"unchanged":         len(unchangedIDs),
		"changedIds":        changedIDs,
		"unchangedIds":      unchangedIDs,
		"notAccepted":       notAccepted,
		"alreadyCompleted":  alreadyCompleted,
		"skippedDaily":      skippedDaily,
		"skippedProfession": skippedProfession,
		// 派生档的输入（= 角色当前已接任务，升序）；显式档也回，便于操作者对账。
		"activeQuestIds": activeIDs,
		// 与 complete:826 / complete-batch:1418 同一句话：GM 强制完成从不发放奖励。
		"rewardGranted":     false,
		"professionChanged": false,
		"results":           questBatchEntryList(entries),
		"hint": "只完成角色当前已接（进行中）的任务：不发奖励（gm_rewards_granted=false），" +
			"职业任务与每日任务被跳过并逐条回报，未接过的任务一律拒绝且不写任何字节。",
	})
}

// writeQuestActiveBatchFailure 是失败路径的统一收尾：HTTP 200 + success:false + 逐条结果。
//
// 整批回滚 ⇒ **没有任何一行落库**，因此逐条结果必须如实改写：事务里已经算成
// "completed" 或还停在 "pending" 的条目一律改写成 rejected/write_failed
// （预检阶段就具名拒绝的那些保持自己的 reason，它们本来就没被写）。
// 绝不把"事务失败了"报成"做完了"（计划书 §5.2 C11）。
//
// 与 writeQuestMutationFailure（api_quest_mutations.go:324）同一条契约：本仓业务失败恒
// HTTP 200，绝不引入 4xx/409。
func (s *Server) writeQuestActiveBatchFailure(
	w http.ResponseWriter,
	panelID int,
	characterID string,
	requested []int64,
	unique []int64,
	entries []questBatchEntry,
	reason string,
	message string,
) {
	for position := range entries {
		switch entries[position].outcome {
		case "pending", "completed":
			entries[position].outcome = "rejected"
			entries[position].reason = "write_failed"
		}
	}
	s.logEvent("gm-quest-active-batch-failed", "panel_id", panelID, "character_id", characterID,
		"reason", reason, "requested", len(requested), "unique", len(unique), "error", message)
	writeJSON(w, map[string]any{
		"success":     false,
		"characterId": panelID,
		"operation":   "active-complete-batch",
		"reason":      reason,
		"requested":   len(requested),
		"unique":      len(unique),
		"results":     questBatchEntryList(entries),
		"error":       message,
	})
}
