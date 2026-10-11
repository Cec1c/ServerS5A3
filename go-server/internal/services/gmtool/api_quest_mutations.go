package gmtool

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	dnfprofession "longheng.io/server/internal/modules/dnf/profession"
	dnfquest "longheng.io/server/internal/modules/dnf/quest"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfskill "longheng.io/server/internal/modules/dnf/skill"
)

// P5：任务写面（安全子集）—— 接取 / 放弃 / 交付(ready) / 完成 / 反完成 / 批量完成。
//
// 上游参考（0.1.8 `dnfbridge/gm.go` 与 `gm_extended.go`）：
//
//	gm.go:1074-1105  GMAcceptQuest        -> POST /api/characters/{id}/quests/{questId}/accept
//	gm.go:1107-1132  GMGiveUpQuest        -> POST .../give-up
//	gm.go:1134-1184  GMMarkQuestReady     -> POST .../ready
//	gm.go:1186-1318  GMForceCompleteQuest -> POST .../complete
//	gm.go:1320-1386  GMUncompleteQuest    -> POST .../uncomplete
//	gm_extended.go:832-907 GMCompleteQuestBatch -> POST .../quests/complete-batch
//
// 三条硬约束（派工单）决定了本文件与上游的差异，逐条都写在这里，改代码前先读：
//
//  1. **不接受上游的静默假成功**（计划书 §5.2 C11）。上游在若干分支上「什么都没做却
//     返回成功」：`GMUncompleteQuest` 在任务账缺失（gm.go:1355-1357）或任务行不存在时
//     直接 return nil；`GMClearInventory` 在账号记录缺失时静默成功（gm_extended.go:643）。
//     本仓一律**明确失败**并说明原因，绝不把"没找到"当成"做完了"。
//  2. **拒绝即零写入**。所有校验（请求形态、PVF 存在性、职业任务门、链路门、在线守卫）
//     都排在克隆/保存之前；事务里校验失败直接返回错误，由 CharacterSettlement 回滚。
//  3. **批量如实回报每一条**。上游 `GMCompleteQuestBatch` 是"全有或全无 + 只回一个
//     总览"；本仓保留全有或全无（单事务、单写入点），但逐条给出 outcome/reason。
//
// 开关（都用 os.Getenv 直读，不改 dnfbridge/options.go —— 与 P2 的 online_guard.go 同惯例）：
//
//	DNFBRIDGE_GM_QUEST_MUTATE           空 = on  ：新路由默认可用；显式 off/0/no/false 才关闭
//	DNFBRIDGE_GM_QUEST_PROFESSION_WRITE 空 = off ：职业任务写入（complete 撞到职业任务）单独把关，
//	                                               **只有显式 on 才启用**（与 RequireOfflineFromEnv 同风格）
//	DNFBRIDGE_GM_QUEST_CHAIN_RESET      空 = off ：主线链路重置（uncomplete 撞到 [main quest] > 0 的任务）
//	                                               **只有显式 on 才启用**（P6 新增，见下）
//
// P6（J12：职业任务分支 + 主线链路重置）在 P5 的两个缺口上接线，**不新增任何路由**：
//
//   - `complete` 撞到职业任务、且 DNFBRIDGE_GM_QUEST_PROFESSION_WRITE=on 时，除既有的
//     职业跃迁（grow_type + 技能账）外，再用 quest.PlanBaseProfessionQuestReset 把
//     职业任务账退回基准态（清掉 JobChangeQuest ∈ 1..3 的旧行，专家职业 20 不动）。
//   - `uncomplete` 撞到 [main quest] > 0 的任务、且 DNFBRIDGE_GM_QUEST_CHAIN_RESET=on 时，
//     走 quest.PlanLinkedQuestChainReset：删掉整条 quest clear 家族的所有已持久化世代，
//     再按 PVF 重新激活一代。**只有"被管理的链接子任务"才允许**；其它情况一律具名拒绝。
//
// P6b（同一开关，不新增开关、不新增路由）补上 P6 报告 §8-② 留下的缺口：请求的是
// **quest clear 父任务自己**（MainQuestID == 0，且 [int data] 里至少有一个受管子任务）时，
// 走 quest.PlanLinkedQuestChainResetFromParent —— 同一段整族重置，只是发起点是父任务；
// 响应与子任务路径同形（chainReset / parentQuestId / removedQuestIds / reactivatedQuestIds）。
// childless 的 quest clear 父任务与普通任务继续走 P5 的单条路径（没有家族可重置）。
//
// 作用域红线：本文件的 6 条路由全部是**新增**路由；既有 35+ 条路由的注册行与 handler
// 一行未动，默认档（三个开关都未设）下既有请求的响应逐字节不变。P6 的两个开关默认档
// 同样是"逐字节保持 P5 的拒绝响应与 reason"（见下方的开关分支注释）。

const (
	// questMutateEnv 是任务写面的总开关（空 = on）。
	questMutateEnv = "DNFBRIDGE_GM_QUEST_MUTATE"
	// questProfessionWriteEnv 是职业任务写入的独立开关（空 = off）。
	questProfessionWriteEnv = "DNFBRIDGE_GM_QUEST_PROFESSION_WRITE"
	// questChainResetEnv 是主线链路重置的独立开关（空 = off，P6 新增）。
	questChainResetEnv = "DNFBRIDGE_GM_QUEST_CHAIN_RESET"
	// questBatchMaxIDs 与上游 gm_extended.go:834-836 的上限一致（1..200）。
	questBatchMaxIDs = 200
	// questCompletedLiteral 是写进仓储的完成态字面量（上游 gm.go:1300 用 "completed"）。
	questCompletedLiteral = "completed"
	// questProfessionWriteSwitchUsage 在拒绝文案里复用，保证操作者一眼看到开关名。
	questProfessionWriteSwitchUsage = questProfessionWriteEnv + "=on"
)

// questCompletionExtraKeys 是上游在两处（gm.go:1172-1174 ready、gm.go:1306-1308 complete）
// 都要清掉的四个"结算回执"键。留着它们会让客户端/后续结算流程以为还有一个待领的奖励。
var questCompletionExtraKeys = []string{
	"completion_key",
	"completion_kind",
	"reward_state",
	"settlement_receipt_v1",
}

// 写进任务行 Extra 的 GM 标记（键名与上游逐字相同）。
const (
	questMarkedReadyKey    = "gm_marked_ready"
	questForceCompletedKey = "gm_force_completed"
	questBatchCompletedKey = "gm_batch_completed"
	questRewardsGrantedKey = "gm_rewards_granted"
	questPVFPathKey        = "pvf_path"
)

// ---------------------------------------------------------------------------
// 开关解析
// ---------------------------------------------------------------------------

// QuestMutateFromEnv 解析 DNFBRIDGE_GM_QUEST_MUTATE。**空 = on**（新路由默认可用）：
// 未设置 / 空 / on / true / 1 / yes / enable 视为启用；显式 off / 0 / no / false /
// disable / disabled 才关闭。关闭时 6 条新路由恒返失败（HTTP 200 + success:false），
// 不做任何校验、不碰仓储。
func QuestMutateFromEnv(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "on", "true", "1", "yes", "enable", "enabled":
		return true
	default:
		return false
	}
}

// QuestProfessionWriteFromEnv 解析 DNFBRIDGE_GM_QUEST_PROFESSION_WRITE。
// **空 = off**，且只有显式 `on`（两端裁剪、大小写不敏感）才启用 —— 与
// RequireOfflineFromEnv（online_guard.go:61-63）同一约定：安全开关只认显式 on，
// `true`/`1`/`yes` 一律不算。
func QuestProfessionWriteFromEnv(value string) bool {
	return strings.EqualFold(strings.TrimSpace(value), "on")
}

func (s *Server) questMutateEnabled() bool {
	return QuestMutateFromEnv(os.Getenv(questMutateEnv))
}

func (s *Server) questProfessionWriteEnabled() bool {
	return QuestProfessionWriteFromEnv(os.Getenv(questProfessionWriteEnv))
}

// QuestChainResetFromEnv 解析 DNFBRIDGE_GM_QUEST_CHAIN_RESET。**空 = off**，且只有显式
// `on`（两端裁剪、大小写不敏感）才启用 —— 与 QuestProfessionWriteFromEnv /
// RequireOfflineFromEnv（online_guard.go:61-63）同一约定：安全开关只认显式 on，
// `true`/`1`/`yes` 一律不算。
func QuestChainResetFromEnv(value string) bool {
	return strings.EqualFold(strings.TrimSpace(value), "on")
}

func (s *Server) questChainResetEnabled() bool {
	return QuestChainResetFromEnv(os.Getenv(questChainResetEnv))
}

// rejectQuestMutateDisabled 是总开关关闭时的统一拒绝（零校验、零写入）。
func (s *Server) rejectQuestMutateDisabled(w http.ResponseWriter, action string, panelID int, questID int64) {
	raw := strings.TrimSpace(os.Getenv(questMutateEnv))
	s.logEvent("gm-quest-mutate-disabled", "action", action, "panel_id", panelID,
		"quest_id", questID, "env", questMutateEnv, "value", raw)
	writeFailure(w, fmt.Sprintf(
		"任务写操作已关闭（%s=%q）。把它设为空或 on 才允许改任务；本次未做任何写入。",
		questMutateEnv, raw))
}

// ---------------------------------------------------------------------------
// 拒绝原因（结构化，供 handler 如实回报给面板）
// ---------------------------------------------------------------------------

// questMutationRejection 是"拒绝、零写入"的结构化原因。message 直接回给前端，
// reason 供日志与自动化断言使用（与 P2 的 mailboxDeleteRejection 同一形态）。
type questMutationRejection struct {
	reason  string
	message string
}

func (e *questMutationRejection) Error() string {
	return fmt.Sprintf("quest mutation rejected (%s): %s", e.reason, e.message)
}

func questReject(reason, format string, args ...any) *questMutationRejection {
	return &questMutationRejection{reason: reason, message: fmt.Sprintf(format, args...)}
}

// 事务内用于中止的哨兵（真正给操作者看的文案由 questReject 携带）。
var (
	errQuestRecordMissing         = errors.New("quest record is missing")
	errCharacterMissingForQuest   = errors.New("character record is missing")
	errQuestSettlementUnavailable = errors.New("quest settlement transaction is unavailable")
)

// ---------------------------------------------------------------------------
// 请求上下文
// ---------------------------------------------------------------------------

// questMutationTarget 是一次任务写需要的全部只读输入：角色、任务账、PVF 任务目录与
// 资格投影。构造过程**不写任何字节**；任何一步失败都会写出失败响应并返回 false。
type questMutationTarget struct {
	ctx         context.Context
	group       dnfrepo.Group
	panelID     int
	characterID string
	questID     int64
	character   dnfrepo.CharacterRecord
	record      dnfrepo.QuestRecord
	catalog     *dnfquest.Catalog
	definition  dnfquest.Definition
	eligibility dnfquest.CharacterEligibility
}

// questMutationTarget 解析 id / questId，装载任务账、PVF 目录与资格投影，并要求该任务
// **确实存在于当前运行时 Script.pvf**（派工单硬性要求 5：PVF 里没有的任务一律报错，
// 不写死、不猜）。上游只有 complete/uncomplete 做这一步；本仓对 6 条路由统一口径。
func (s *Server) questMutationTarget(w http.ResponseWriter, r *http.Request) (questMutationTarget, bool) {
	ctx, group, panelID, character, ok := s.characterRequest(w, r)
	if !ok {
		return questMutationTarget{}, false
	}
	questID, err := parseQuestMutationID(r.PathValue("questId"))
	if err != nil {
		writeFailure(w, err.Error())
		return questMutationTarget{}, false
	}
	// 任务 owner 的主键是 uint16（上游 gmQuestMutationIDs 的 ParseUint(...,16)，
	// gm.go:2028-2037）。面板主键超出这个范围时必须**明确拒绝**，绝不能让
	// uint16(panelID) 截断后去改另一个角色。
	if panelID <= 0 || panelID > math.MaxUint16 {
		writeFailure(w, fmt.Sprintf(
			"面板角色号 %d 超出任务 owner 的 uint16 范围（1..%d），已拒绝；本次未做任何写入。",
			panelID, uint64(math.MaxUint16)))
		return questMutationTarget{}, false
	}
	record, catalog, eligibility, err := s.questReadContext(ctx, group, character)
	if err != nil {
		writeFailure(w, err.Error())
		return questMutationTarget{}, false
	}
	definition, found := catalog.Find(questID)
	if !found {
		writeFailure(w, fmt.Sprintf(
			"任务 %d 不在当前运行时 Script.pvf 的任务表里（本仓不合成任务定义、不猜字段）；本次未做任何写入。",
			questID))
		return questMutationTarget{}, false
	}
	return questMutationTarget{
		ctx:         ctx,
		group:       group,
		panelID:     panelID,
		characterID: strings.TrimSpace(character.CharacterID),
		questID:     questID,
		character:   character,
		record:      record,
		catalog:     catalog,
		definition:  definition,
		eligibility: eligibility,
	}, true
}

// parseQuestMutationID 与 P1 的 questId 解析同一处口径：1..65535。
func parseQuestMutationID(raw string) (int64, error) {
	value, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || value <= 0 || value > math.MaxUint16 {
		return 0, fmt.Errorf("任务编号无效: %s (允许范围 1..%d)",
			strings.TrimSpace(raw), uint64(math.MaxUint16))
	}
	return value, nil
}

// questPostSummary 重读任务账并给出变更后的任务摘要（上游 gmQuestMutationResult 的
// 等价物：gm.go:2010-2026）。只读，失败时退化为"没有该任务行"的空摘要。
func (s *Server) questPostSummary(target questMutationTarget) map[string]any {
	record, found, err := target.group.Quest.Load(target.ctx, target.characterID)
	if err != nil || !found {
		record = dnfrepo.QuestRecord{CharacterID: target.characterID}
	}
	state, hasState := questStateLookup(record, target.questID)
	acceptable := false
	if !isActiveQuestStatus(state.Status) && !isCompletedQuestStatus(state.Status) {
		_, acceptable = acceptableQuestIDs(target.catalog, target.eligibility, record)[target.questID]
	}
	return questSummaryPayload(target.definition, state, hasState, acceptable)
}

// ---------------------------------------------------------------------------
// 事务内的小工具（上游 gmMutableQuestState / gmPutQuestState 的等价物）
// ---------------------------------------------------------------------------

// questMutationState 复刻上游 gmMutableQuestState（gm.go:2049-2057）：先看 States，
// 再看 Progress，并回传"该行实际在哪张表"。
func questMutationState(record dnfrepo.QuestRecord, questID int64) (dnfrepo.QuestState, dnfrepo.QuestField, bool) {
	if state, ok := record.States[questID]; ok {
		return state, dnfrepo.QuestFieldStates, true
	}
	if state, ok := record.Progress[questID]; ok {
		return state, dnfrepo.QuestFieldProgress, true
	}
	return dnfrepo.QuestState{}, "", false
}

// questMutationPutState 复刻上游 gmPutQuestState（gm.go:2059-2075）。
func questMutationPutState(record *dnfrepo.QuestRecord, questID int64, state dnfrepo.QuestState, field dnfrepo.QuestField) {
	if record == nil {
		return
	}
	if field == dnfrepo.QuestFieldProgress {
		if record.Progress == nil {
			record.Progress = make(map[int64]dnfrepo.QuestState)
		}
		record.Progress[questID] = state
		return
	}
	if record.States == nil {
		record.States = make(map[int64]dnfrepo.QuestState)
	}
	record.States[questID] = state
}

// copyQuestExtra 深拷贝 Extra：避免在"没有实际变化、因此不写库"的分支上改动原行。
func copyQuestExtra(extra map[string]string) map[string]string {
	out := make(map[string]string, len(extra)+2)
	for key, value := range extra {
		out[key] = value
	}
	return out
}

// ---------------------------------------------------------------------------
// 失败回报
// ---------------------------------------------------------------------------

// writeQuestMutationFailure 把事务/owner 的失败翻译成前端能读懂的响应 + 一条可 grep 的
// 具名事件。失败形态恒 HTTP 200 + success:false（计划书 §5.2 C10：绝不引入 409）。
func (s *Server) writeQuestMutationFailure(
	w http.ResponseWriter,
	action string,
	target questMutationTarget,
	err error,
) {
	payload := map[string]any{
		"success":     false,
		"characterId": target.panelID,
		"questId":     target.questID,
	}
	var rejection *questMutationRejection
	switch {
	case errors.As(err, &rejection):
		payload["reason"] = rejection.reason
		payload["error"] = rejection.message
		s.logEvent("gm-quest-mutation-rejected", "action", action, "panel_id", target.panelID,
			"character_id", target.characterID, "quest_id", target.questID, "reason", rejection.reason)
	case errors.Is(err, errCharacterMissingForQuest):
		payload["reason"] = "character_missing"
		payload["error"] = fmt.Sprintf("角色不存在: %d；本次未做任何写入。", target.panelID)
		s.logEvent("gm-quest-mutation-rejected", "action", action, "panel_id", target.panelID,
			"character_id", target.characterID, "quest_id", target.questID, "reason", "character_missing")
	case errors.Is(err, errQuestSettlementUnavailable):
		payload["reason"] = "settlement_unavailable"
		payload["error"] = "任务结算事务能力未就绪（CharacterSettlement）；本次未做任何写入。"
		s.logEvent("gm-quest-mutation-rejected", "action", action, "panel_id", target.panelID,
			"character_id", target.characterID, "quest_id", target.questID, "reason", "settlement_unavailable")
	default:
		payload["reason"] = "write_failed"
		payload["error"] = "任务写入失败: " + err.Error()
		s.logEvent("gm-quest-mutation-failed", "action", action, "panel_id", target.panelID,
			"character_id", target.characterID, "quest_id", target.questID,
			"reason", "write_failed", "error", err.Error())
	}
	writeJSON(w, payload)
}

// questOwnerRejection 把 dnfquest.Owner 的哨兵错误翻成中文拒绝原因。
// 上游把这些错误统一包成 ErrGMInvalidRequest + 英文原文（gm.go:1102/1129）；
// 本仓保留"是哪一个前置不满足"的信息量，便于操作者判断。
func questOwnerRejection(panelID int, questID int64, err error) *questMutationRejection {
	switch {
	case errors.Is(err, dnfquest.ErrOwnerUnavailable):
		return questReject("owner_unavailable", "任务 owner 不可用：缺少角色或任务仓储；本次未做任何写入。")
	case errors.Is(err, dnfquest.ErrCharacterNotFound):
		return questReject("character_missing", "角色不存在: %d；本次未做任何写入。", panelID)
	case errors.Is(err, dnfquest.ErrQuestDefinitionMissing):
		return questReject("definition_missing",
			"任务 %d 不在当前运行时 Script.pvf 的任务表里（本仓不合成任务定义）；本次未做任何写入。", questID)
	case errors.Is(err, dnfquest.ErrQuestNotAcceptable):
		return questReject("not_acceptable",
			"角色当前状态不满足任务 %d 的接取条件（等级 / 职业 / 成长段 / 前置任务 / 冲突任务）：%v；本次未做任何写入。",
			questID, err)
	case errors.Is(err, dnfquest.ErrQuestAcceptEventItemsRequired):
		return questReject("accept_needs_event_items",
			"任务 %d 的接取需要活动道具事务，owner 明确不支持；本次未做任何写入。", questID)
	case errors.Is(err, dnfquest.ErrQuestInitialTriggerUnsupported):
		return questReject("init_trigger_unsupported",
			"任务 %d 的初始进度规则尚未实现（owner 拒绝写入半截状态）；本次未做任何写入。", questID)
	case errors.Is(err, dnfquest.ErrQuestCannotGiveUp):
		return questReject("cant_give_up",
			"任务 %d 在 PVF 里标记为不可放弃（[cant give up]），owner 已拒绝；本次未做任何写入。", questID)
	case errors.Is(err, dnfquest.ErrGiveUpNeedsAssets):
		return questReject("give_up_needs_assets",
			"任务 %d 的放弃需要背包回收事务（PVF 定义带任务道具或奖励物品），owner 拒绝只删任务行；本次未做任何写入。",
			questID)
	case errors.Is(err, dnfquest.ErrQuestNotActive):
		return questReject("not_active", "任务 %d 当前不是进行中的任务；本次未做任何写入。", questID)
	case errors.Is(err, dnfquest.ErrQuestPersistVerify):
		return questReject("persist_verify_failed",
			"任务 %d 写入后回读校验失败（仓储返回值与预期不一致），请检查数据库；", questID)
	default:
		return questReject("owner_failed", "任务 %d 写入被 owner 拒绝：%v；本次未做任何写入。", questID, err)
	}
}

// ---------------------------------------------------------------------------
// J9-a：接取任务（上游 GMAcceptQuest gm.go:1074-1105）
// ---------------------------------------------------------------------------

func (s *Server) handleQuestAccept(w http.ResponseWriter, r *http.Request) {
	if !s.questMutateEnabled() {
		s.rejectQuestMutateDisabled(w, "quest-accept", 0, 0)
		return
	}
	target, ok := s.questMutationTarget(w, r)
	if !ok {
		return
	}
	if !s.requireOffline(w, "quest-accept", target.panelID) {
		return
	}
	// 复用本仓既有的任务 owner（quest/owner.go:125）：接取规则、合法性与持久化都只有
	// 这一套实现，GM 面板不再写第二遍。上游 gm.go:1094-1100 也是这么做的。
	owner, err := dnfquest.NewOwner(target.group)
	if err != nil {
		s.writeQuestMutationFailure(w, "quest-accept", target, questOwnerRejection(target.panelID, target.questID, err))
		return
	}
	result, err := owner.ApplyAccept(target.ctx, target.catalog, target.eligibility, dnfquest.Command{
		Operation:           "accept_quest",
		AccountID:           target.character.AccountID,
		SelectedCharacterID: uint16(target.panelID),
		QuestID:             uint16(target.questID),
	})
	if err != nil {
		s.writeQuestMutationFailure(w, "quest-accept", target, questOwnerRejection(target.panelID, target.questID, err))
		return
	}
	s.logEvent("gm-quest-accept", "panel_id", target.panelID, "character_id", target.characterID,
		"quest_id", target.questID, "idempotent", result.Idempotent, "init_trigger", result.InitTrigger,
		"pvf_path", result.PVFPath)
	writeOK(w, map[string]any{
		"characterId":       target.panelID,
		"operation":         "accept",
		"questId":           target.questID,
		"idempotent":        result.Idempotent,
		"initTrigger":       result.InitTrigger,
		"rewardGranted":     false,
		"professionChanged": false,
		"quest":             s.questPostSummary(target),
	})
}

// ---------------------------------------------------------------------------
// J9-b：放弃任务（上游 GMGiveUpQuest gm.go:1107-1132）
// ---------------------------------------------------------------------------

func (s *Server) handleQuestGiveUp(w http.ResponseWriter, r *http.Request) {
	if !s.questMutateEnabled() {
		s.rejectQuestMutateDisabled(w, "quest-give-up", 0, 0)
		return
	}
	target, ok := s.questMutationTarget(w, r)
	if !ok {
		return
	}
	if !s.requireOffline(w, "quest-give-up", target.panelID) {
		return
	}
	owner, err := dnfquest.NewOwner(target.group)
	if err != nil {
		s.writeQuestMutationFailure(w, "quest-give-up", target, questOwnerRejection(target.panelID, target.questID, err))
		return
	}
	// ApplyGiveUp 自己也做 PVF 门与"带资产任务"门（quest/owner.go:271-280）：
	// `[cant give up]`、任务道具/奖励物品/`seeking` 类任务一律拒绝，绝不只删任务行。
	if _, err := owner.ApplyGiveUp(target.ctx, target.catalog, dnfquest.Command{
		Operation:           "give_up_quest",
		AccountID:           target.character.AccountID,
		SelectedCharacterID: uint16(target.panelID),
		QuestID:             uint16(target.questID),
	}); err != nil {
		s.writeQuestMutationFailure(w, "quest-give-up", target, questOwnerRejection(target.panelID, target.questID, err))
		return
	}
	s.logEvent("gm-quest-give-up", "panel_id", target.panelID, "character_id", target.characterID,
		"quest_id", target.questID, "pvf_path", target.definition.Path)
	writeOK(w, map[string]any{
		"characterId":       target.panelID,
		"operation":         "give-up",
		"questId":           target.questID,
		"removed":           true,
		"rewardGranted":     false,
		"professionChanged": false,
		"quest":             s.questPostSummary(target),
	})
}

// ---------------------------------------------------------------------------
// J10-a：标记可交付 ready（上游 GMMarkQuestReady gm.go:1134-1184）
// ---------------------------------------------------------------------------

func (s *Server) handleQuestMarkReady(w http.ResponseWriter, r *http.Request) {
	if !s.questMutateEnabled() {
		s.rejectQuestMutateDisabled(w, "quest-ready", 0, 0)
		return
	}
	target, ok := s.questMutationTarget(w, r)
	if !ok {
		return
	}
	if !s.requireOffline(w, "quest-ready", target.panelID) {
		return
	}
	if target.group.CharacterSettlement == nil {
		s.writeQuestMutationFailure(w, "quest-ready", target, errQuestSettlementUnavailable)
		return
	}
	var (
		changed       bool
		clearedKeys   []string
		progressAfter int64
		statusAfter   string
	)
	err := target.group.CharacterSettlement.WithinCharacterSettlement(target.ctx, target.characterID, func(tx dnfrepo.Group) error {
		if tx.Character == nil || tx.Quest == nil {
			return errQuestSettlementUnavailable
		}
		// 上游 gm.go:1150-1154：事务内重载角色，角色不存在直接失败。
		if _, found, loadErr := tx.Character.Load(target.ctx, target.characterID); loadErr != nil {
			return loadErr
		} else if !found {
			return errCharacterMissingForQuest
		}
		record, found, loadErr := tx.Quest.Load(target.ctx, target.characterID)
		if loadErr != nil {
			return loadErr
		}
		if !found {
			return errQuestRecordMissing
		}
		record = dnfrepo.CloneQuest(record)
		state, field, known := questMutationState(record, target.questID)
		if !known || !isActiveQuestStatus(state.Status) {
			// 上游 gm.go:1160/1164-1166 的 "quest is not active"。
			return questReject("not_active",
				"任务 %d 当前不是进行中的任务（当前状态 %q）；只能把进行中的任务标记为可交付；本次未做任何写入。",
				target.questID, strings.TrimSpace(state.Status))
		}
		// 上游 gm.go:1167-1175：进度归零、清掉四个结算回执键、打上 gm_marked_ready。
		next := state
		nextExtra := copyQuestExtra(state.Extra)
		for _, key := range questCompletionExtraKeys {
			if _, exists := nextExtra[key]; exists {
				delete(nextExtra, key)
				clearedKeys = append(clearedKeys, key)
			}
		}
		alreadyReady := nextExtra[questMarkedReadyKey] == "true"
		changed = state.ProgressValue != 0 || len(clearedKeys) > 0 || !alreadyReady
		next.ProgressValue = 0
		next.Extra = nextExtra
		next.Extra[questMarkedReadyKey] = "true"
		progressAfter = next.ProgressValue
		statusAfter = next.Status
		if !changed {
			// 已经是"可交付"且进度本来就是 0：重复点击 = 零写入（本仓的幂等口径，
			// 与 profession_quests.go 的"重复点击 = 无写入"一致）。
			return nil
		}
		next.UpdatedAt = time.Now().UTC()
		questMutationPutState(&record, target.questID, next, field)
		record.CharacterID = target.characterID
		record.UpdatedAt = next.UpdatedAt
		return dnfrepo.SaveQuestFields(target.ctx, tx.Quest, record, field)
	})
	if err != nil {
		s.writeQuestMutationFailure(w, "quest-ready", target, err)
		return
	}
	s.logEvent("gm-quest-ready", "panel_id", target.panelID, "character_id", target.characterID,
		"quest_id", target.questID, "changed", changed, "cleared_keys", strings.Join(clearedKeys, ","))
	writeOK(w, map[string]any{
		"characterId":       target.panelID,
		"operation":         "ready",
		"questId":           target.questID,
		"markedReady":       true,
		"changed":           changed,
		"unchanged":         !changed,
		"clearedKeys":       append([]string{}, clearedKeys...),
		"progressValue":     progressAfter,
		"status":            statusAfter,
		"rewardGranted":     false,
		"professionChanged": false,
		"quest":             s.questPostSummary(target),
	})
}

// ---------------------------------------------------------------------------
// J10-b：强制完成（上游 GMForceCompleteQuest gm.go:1186-1318）
// ---------------------------------------------------------------------------

func (s *Server) handleQuestForceComplete(w http.ResponseWriter, r *http.Request) {
	if !s.questMutateEnabled() {
		s.rejectQuestMutateDisabled(w, "quest-complete", 0, 0)
		return
	}
	target, ok := s.questMutationTarget(w, r)
	if !ok {
		return
	}
	professionQuest := IsProfessionQuestReward(target.definition.RewardType)
	// 职业任务写入默认关闭（计划书 §4.5 的开关表：默认走「拒绝，与批量完成同口径」）。
	// 拒绝发生在任何写之前 —— 零写入。
	if professionQuest && !s.questProfessionWriteEnabled() {
		raw := strings.TrimSpace(os.Getenv(questProfessionWriteEnv))
		s.logEvent("gm-quest-mutation-rejected", "action", "quest-complete", "panel_id", target.panelID,
			"character_id", target.characterID, "quest_id", target.questID, "reason", "profession_write_disabled",
			"env", questProfessionWriteEnv, "value", raw)
		writeJSON(w, map[string]any{
			"success":     false,
			"characterId": target.panelID,
			"questId":     target.questID,
			"reason":      "profession_write_disabled",
			"rewardType":  target.definition.RewardType,
			"error": fmt.Sprintf(
				"任务 %d 是职业任务（[reward type] = %s）：职业任务写入默认关闭，需显式 %s 才允许；"+
					"本次未做任何写入。批量完成始终排除职业任务。",
				target.questID, strings.TrimSpace(target.definition.RewardType), questProfessionWriteSwitchUsage),
		})
		return
	}
	if !s.requireOffline(w, "quest-complete", target.panelID) {
		return
	}
	if target.group.CharacterSettlement == nil {
		s.writeQuestMutationFailure(w, "quest-complete", target, errQuestSettlementUnavailable)
		return
	}
	// 职业跃迁要用的 PVF 资源在事务**之前**取（PVF 解析可能很慢，且只读）。
	var (
		profiles     *dnfprofession.Profiles
		skillCatalog *dnfskill.Table
	)
	if professionQuest {
		var err error
		profiles, skillCatalog, err = s.pvf.professionResources(s)
		if err != nil {
			writeFailure(w, "职业/技能 PVF 资源不可用: "+err.Error()+"；本次未做任何写入。")
			return
		}
		if profiles == nil || skillCatalog == nil {
			writeFailure(w, "职业/技能 PVF 资源不可用（PVF 未提供职业成长数据）；本次未做任何写入。")
			return
		}
	}
	var (
		alreadyCompleted  bool
		professionChanged bool
		growTypeAfter     int64
		professionReset   []int64
	)
	err := target.group.CharacterSettlement.WithinCharacterSettlement(target.ctx, target.characterID, func(tx dnfrepo.Group) error {
		if tx.Character == nil || tx.Quest == nil {
			return errQuestSettlementUnavailable
		}
		character, found, loadErr := tx.Character.Load(target.ctx, target.characterID)
		if loadErr != nil {
			return loadErr
		}
		if !found {
			return errCharacterMissingForQuest
		}
		record, found, loadErr := tx.Quest.Load(target.ctx, target.characterID)
		if loadErr != nil {
			return loadErr
		}
		if !found {
			record = dnfrepo.QuestRecord{CharacterID: target.characterID}
		} else {
			record = dnfrepo.CloneQuest(record)
		}
		// 上游 gm.go:1236-1238：已经是完成态的，直接无操作返回（幂等，且**不做**
		// 职业跃迁 —— 重复点击不能把 grow_type 再推一格）。
		if state, known := questStateLookup(record, target.questID); known && isCompletedQuestStatus(state.Status) {
			alreadyCompleted = true
			return nil
		}
		character = dnfrepo.CloneCharacter(character)
		if professionQuest {
			if tx.Skill == nil {
				return errQuestSettlementUnavailable
			}
			// 上游 gm.go:1244-1256：解析存储的职业/成长值，再把 PVF 奖励解成一次跃迁。
			job, parseErr := strconv.ParseUint(strings.TrimSpace(character.Job), 10, 8)
			grow := growTypeValue(character)
			if parseErr != nil || grow < 0 || grow > math.MaxUint8 {
				return questReject("profession_state_invalid",
					"角色存储的职业/成长值无效（job=%q grow_type=%d），无法执行职业任务跃迁；本次未做任何写入。",
					character.Job, grow)
			}
			request, transitionErr := dnfprofession.ParseReward(
				target.definition.JobChangeQuest, target.definition.RewardType, target.definition.RewardIntData)
			if transitionErr != nil {
				return questReject("profession_reward_invalid",
					"任务 %d 的职业奖励数据无法解析：%v；本次未做任何写入。", target.questID, transitionErr)
			}
			transition, transitionErr := profiles.PlanTransition(byte(job), byte(grow), request)
			if transitionErr != nil {
				return questReject("profession_transition_invalid",
					"任务 %d 的职业跃迁不合法（job=%d grow_type=%d）：%v；本次未做任何写入。",
					target.questID, job, grow, transitionErr)
			}
			skill, skillFound, skillErr := tx.Skill.Load(target.ctx, target.characterID)
			if skillErr != nil {
				return skillErr
			}
			if !skillFound {
				// 上游 gm.go:1261-1263 的 "skill ledger is missing"：绝不合成空技能账。
				return questReject("skill_ledger_missing",
					"角色 %s 的技能账缺失，职业任务跃迁需要它；本次未做任何写入。", target.characterID)
			}
			skill, skillErr = profiles.ApplySkillTransition(
				skillCatalog, byte(job), character.Level, transition, skill, skill.Points)
			if skillErr != nil {
				return questReject("profession_skill_transition_failed",
					"职业跃迁的技能重算失败：%v；本次未做任何写入。", skillErr)
			}
			if character.Stats == nil {
				character.Stats = make(map[string]int64)
			}
			character.Stats["grow_type"] = int64(transition.NewGrowType)
			// 上游 gm.go:1272-1278 用 characterPVFStatValuesForLevel 重算等级属性；
			// 本仓等价物是既有 applyLevelStats（同时保留 GM 设置的负重上限覆写）。
			if statErr := s.applyLevelStats(&character); statErr != nil {
				return questReject("profession_stat_recompute_failed",
					"职业跃迁的属性重算失败：%v；本次未做任何写入。", statErr)
			}
			now := time.Now().UTC()
			character.UpdatedAt = now
			skill.CharacterID = target.characterID
			skill.UpdatedAt = now
			if saveErr := dnfrepo.SaveCharacterFields(target.ctx, tx.Character, character,
				dnfrepo.CharacterFieldStats); saveErr != nil {
				return saveErr
			}
			if saveErr := dnfrepo.SaveSkillFields(target.ctx, tx.Skill, skill,
				dnfrepo.SkillFieldSkills, dnfrepo.SkillFieldPoints,
				dnfrepo.SkillFieldLayouts, dnfrepo.SkillFieldCooldowns); saveErr != nil {
				return saveErr
			}
			professionChanged = true
			growTypeAfter = int64(transition.NewGrowType)
			// P6（J12-a）：职业跃迁之后，职业任务账必须一起退回基准态 ——
			// 上游 quest/profession_reset.go:23 的 PlanBaseProfessionQuestReset。
			//
			// 为什么必须做：任务账独立于 grow_type 存储。只推 grow_type 会把上一个
			// 职业/成长段的"第一职业 / 一次觉醒 / 二次觉醒"任务行留在账里，客户端
			// 0x164 的已完成任务位图与 op21 可接任务列表随即与角色当前成长段矛盾
			// （本仓同款现象记录在 gmtool/profession_quests.go:13-24）。
			//
			// 顺序：**先重置、后写本次这一行**（本函数末尾那段完成态写入）。反过来的话
			// 刚写好的完成行会被重置删掉。plan 与 apply 分离：本函数只返回投影，写入
			// 由本事务末尾的 SaveQuestFields 一次完成；无变化时 plan.Record 逐字节等于
			// 原记录（含 UpdatedAt）。
			//
			// 已经是完成态的任务走上面的幂等早退（alreadyCompleted），既不做跃迁也不
			// 做重置 —— 重复点击不会反复清账。
			//
			// 与上游调用点的关系：上游在 GMSetCareer（改职业）里调它；本仓的"设置成长/
			// 觉醒"走 gmtool/api_characters.go 的 growoptions + profession_quests.go 的
			// planProfessionQuestStates（那条路径**不删行**，只把 PVF 授权任务翻成完成态）。
			// 派工单把本 plan 接到 GM 面板的强制完成上：操作者勾了
			// DNFBRIDGE_GM_QUEST_PROFESSION_WRITE=on 就是明确要求"完成这个职业任务并让
			// 职业状态自洽"，因此这里做的是**清掉除本次完成之外的全部职业任务旧行**。
			resetPlan, resetErr := target.catalog.PlanBaseProfessionQuestReset(record, now)
			if resetErr != nil {
				return questReject("profession_ledger_reset_failed",
					"职业任务账重置计划失败: %v；本次未做任何写入。", resetErr)
			}
			record = resetPlan.Record
			professionReset = resetPlan.ResetQuestIDs
		}
		// 上游 gm.go:1292-1312：写 completed + 三个 GM 标记，删四个结算回执键，
		// 并把 Progress 里的同 id 行删掉（只留 States 一份权威行）。
		now := time.Now().UTC()
		if record.States == nil {
			record.States = make(map[int64]dnfrepo.QuestState)
		}
		state, _ := questStateLookup(record, target.questID)
		state.Extra = copyQuestExtra(state.Extra)
		state.Status = questCompletedLiteral
		state.ProgressValue = 0
		state.UpdatedAt = now
		state.Extra[questPVFPathKey] = target.definition.Path
		state.Extra[questForceCompletedKey] = "true"
		// **不发奖励**：上游 gm.go:1305 就把这件事写进任务行，本仓逐字保留。
		state.Extra[questRewardsGrantedKey] = "false"
		for _, key := range questCompletionExtraKeys {
			delete(state.Extra, key)
		}
		record.States[target.questID] = state
		delete(record.Progress, target.questID)
		record.CharacterID = target.characterID
		record.UpdatedAt = now
		return dnfrepo.SaveQuestFields(target.ctx, tx.Quest, record,
			dnfrepo.QuestFieldStates, dnfrepo.QuestFieldProgress)
	})
	if err != nil {
		s.writeQuestMutationFailure(w, "quest-complete", target, err)
		return
	}
	s.logEvent("gm-quest-complete", "panel_id", target.panelID, "character_id", target.characterID,
		"quest_id", target.questID, "already_completed", alreadyCompleted,
		"profession_quest", professionQuest, "profession_changed", professionChanged,
		"profession_reset_ids", int64ListText(professionReset))
	payload := map[string]any{
		"characterId":       target.panelID,
		"operation":         "complete",
		"questId":           target.questID,
		"alreadyCompleted":  alreadyCompleted,
		"changed":           !alreadyCompleted,
		"unchanged":         alreadyCompleted,
		"professionQuest":   professionQuest,
		"professionChanged": professionChanged,
		// P6：本次被清掉的职业任务旧行（JobChangeQuest ∈ 1..3；专家职业 20 不动）。
		// 空切片 = 一个旧行都没有（plan 无变化），而不是"没查"。
		"professionResetQuestIds": append([]int64{}, professionReset...),
		// 与上游 gm.go:2010 的 gmQuestMutationResult(rewardGranted=false) 同口径：
		// GM 强制完成**从不发放奖励**。
		"rewardGranted": false,
		"quest":         s.questPostSummary(target),
	}
	if professionChanged {
		payload["growType"] = growTypeAfter
	}
	writeOK(w, payload)
}

// ---------------------------------------------------------------------------
// J10-c：反完成（上游 GMUncompleteQuest gm.go:1320-1386）
// ---------------------------------------------------------------------------

func (s *Server) handleQuestUncomplete(w http.ResponseWriter, r *http.Request) {
	if !s.questMutateEnabled() {
		s.rejectQuestMutateDisabled(w, "quest-uncomplete", 0, 0)
		return
	}
	target, ok := s.questMutationTarget(w, r)
	if !ok {
		return
	}
	// 上游对 MainQuestID > 0 的任务走 catalog.PlanLinkedQuestChainReset（gm.go:1359-1372）。
	// P5 时代本仓 quest 包**没有**这个函数（整文件不存在），只删本任务会把同一主线的
	// 子任务留在自相矛盾的状态里，所以**拒绝**而不是悄悄只做一半。
	//
	// P6 已移植该 plan（quest/linked_chain_reset.go）。默认档（DNFBRIDGE_GM_QUEST_CHAIN_RESET
	// 未设置）**逐字节保持 P5 的拒绝响应与 reason**（`linked_chain_reset_not_ported`）——
	// 这是本批的硬性契约，由 api_quest_mutations_test.go 的
	// TestQuestUncompleteChainResetDefaultOffKeepsP5Rejection 与既有
	// TestQuestUncompleteRejectsWithZeroWrites 共同钉死。
	if target.definition.MainQuestID > 0 {
		if !s.questChainResetEnabled() {
			s.logEvent("gm-quest-mutation-rejected", "action", "quest-uncomplete", "panel_id", target.panelID,
				"character_id", target.characterID, "quest_id", target.questID, "reason", "linked_chain_reset_not_ported",
				"main_quest_id", target.definition.MainQuestID)
			writeJSON(w, map[string]any{
				"success":     false,
				"characterId": target.panelID,
				"questId":     target.questID,
				"reason":      "linked_chain_reset_not_ported",
				"mainQuestId": target.definition.MainQuestID,
				"error": fmt.Sprintf(
					"任务 %d 属于主线链路（[main quest id] = %d）：上游会重置整条链路（PlanLinkedQuestChainReset），"+
						"本仓尚未移植（P6）。只删本任务会留下不一致的子任务，因此拒绝；本次未做任何写入。",
					target.questID, target.definition.MainQuestID),
			})
			return
		}
		s.handleQuestChainUncomplete(w, target)
		return
	}
	// P6b：quest clear **父任务自己**（MainQuestID == 0）也能发起整族重置。
	//
	// 为什么需要：操作者在面板上要重置一条主线时，点的自然是**父任务**那一行，而
	// quest clear 家族父任务自己的 [main quest] 是 0（真档 104 个 quest clear 根全是
	// 这样）。P6 只在 MainQuestID > 0 时接管，于是"重置父任务"退化成 P5 的单条删除：
	// 父行没了、同一家族里已持久化的子任务世代还留在库里，之后再接取会撞
	// CollisionQuests / 家族判定。P6 交付报告 §8-② 自己点名过这个缺口。
	//
	// 判据仍然只有 quest 包那一份（Catalog.ManagedLinkedQuestChainFamily 只是
	// managedLinkedQuestChainIDs 的封装）：请求的任务**自己就是** quest clear 家族父任务、
	// 且 [int data] 里至少有一个被管理的链接子任务，才交给父任务入口。其余情况
	// （普通任务 / childless 的 quest clear 父任务 / [int data] 全是悬空引用的父任务）
	// 继续走下面 P5 的普通单条路径 —— 没有家族可重置时绝不编造。
	//
	// 默认档（DNFBRIDGE_GM_QUEST_CHAIN_RESET 未设置 / 非 on）**逐字节不变**：这一段整体
	// 在 questChainResetEnabled() 之后，开关关闭时一行都不会执行。
	if s.questChainResetEnabled() {
		if _, isFamilyRoot := target.catalog.ManagedLinkedQuestChainFamily(target.questID); isFamilyRoot {
			s.handleQuestChainParentUncomplete(w, target)
			return
		}
	}
	if !s.requireOffline(w, "quest-uncomplete", target.panelID) {
		return
	}
	if target.group.CharacterSettlement == nil {
		s.writeQuestMutationFailure(w, "quest-uncomplete", target, errQuestSettlementUnavailable)
		return
	}
	var (
		removedStates   bool
		removedProgress bool
	)
	err := target.group.CharacterSettlement.WithinCharacterSettlement(target.ctx, target.characterID, func(tx dnfrepo.Group) error {
		if tx.Character == nil || tx.Quest == nil {
			return errQuestSettlementUnavailable
		}
		if _, found, loadErr := tx.Character.Load(target.ctx, target.characterID); loadErr != nil {
			return loadErr
		} else if !found {
			return errCharacterMissingForQuest
		}
		record, found, loadErr := tx.Quest.Load(target.ctx, target.characterID)
		if loadErr != nil {
			return loadErr
		}
		// **有意偏离上游**（计划书 §5.2 C11）：上游在任务账缺失时 `return nil`
		// （gm.go:1355-1357），即"什么都没做但报成功"。本仓明确失败。
		if !found {
			return questReject("quest_record_missing",
				"角色 %s 没有任务账记录，没有可反完成的任务；本次未做任何写入。", target.characterID)
		}
		record = dnfrepo.CloneQuest(record)
		state, known := questStateLookup(record, target.questID)
		// 同样是有意偏离：上游在任务行不存在时直接删两个 map（都是空操作）并报成功。
		if !known {
			return questReject("quest_row_missing",
				"任务 %d 不在该角色的任务账里（States / Progress 都没有这一行），无法反完成；本次未做任何写入。",
				target.questID)
		}
		if !isCompletedQuestStatus(state.Status) {
			return questReject("not_completed",
				"任务 %d 当前不是已完成状态（当前 %q），反完成只对已完成的任务有意义；本次未做任何写入。",
				target.questID, strings.TrimSpace(state.Status))
		}
		// 上游 gm.go:1377-1378：States 与 Progress **两张表都删**。
		if _, exists := record.States[target.questID]; exists {
			delete(record.States, target.questID)
			removedStates = true
		}
		if _, exists := record.Progress[target.questID]; exists {
			delete(record.Progress, target.questID)
			removedProgress = true
		}
		record.CharacterID = target.characterID
		record.UpdatedAt = time.Now().UTC()
		return dnfrepo.SaveQuestFields(target.ctx, tx.Quest, record,
			dnfrepo.QuestFieldStates, dnfrepo.QuestFieldProgress)
	})
	if err != nil {
		s.writeQuestMutationFailure(w, "quest-uncomplete", target, err)
		return
	}
	s.logEvent("gm-quest-uncomplete", "panel_id", target.panelID, "character_id", target.characterID,
		"quest_id", target.questID, "removed_states", removedStates, "removed_progress", removedProgress)
	writeOK(w, map[string]any{
		"characterId":       target.panelID,
		"operation":         "uncomplete",
		"questId":           target.questID,
		"removedStates":     removedStates,
		"removedProgress":   removedProgress,
		"changed":           true,
		"rewardGranted":     false,
		"professionChanged": false,
		"quest":             s.questPostSummary(target),
	})
}

// ---------------------------------------------------------------------------
// P6 · J12-b：主线链路重置（DNFBRIDGE_GM_QUEST_CHAIN_RESET=on）
// ---------------------------------------------------------------------------

// questEligibilityFromCharacter 是任务资格投影（等级 / 职业 / 成长段）的唯一实现。
// 角色存储的 job 不可解析或越界时**明确失败**，绝不用 0 顶替 —— 用 0 会把鬼剑士的
// 任务错标成可接（api_quests.go:43-50 的原始注释）。questReadContext 与本文件的
// 链路重置共用它，避免第二套口径。
func questEligibilityFromCharacter(character dnfrepo.CharacterRecord) (dnfquest.CharacterEligibility, error) {
	job, err := strconv.Atoi(strings.TrimSpace(character.Job))
	if err != nil || job < 0 || job > math.MaxUint8 {
		return dnfquest.CharacterEligibility{}, fmt.Errorf("角色存储的职业值无效: %q", character.Job)
	}
	return dnfquest.CharacterEligibility{
		Level:    character.Level,
		Job:      job,
		GrowType: int(growTypeValue(character)),
	}, nil
}

// questChainRejection 把 quest.PlanLinkedQuestChainReset 的**具名**拒绝翻成面板文案。
//
// 每一条都点名具体的 quest id（以及父任务 / 缺失的家族成员），对应派工单硬性要求 2
// 「任何'账不存在 / 任务行不存在 / PVF 没有这个任务 / 链条数据不完整'都必须明确失败
// 并点名原因，绝不允许只重置看到的那部分然后报成功」。
func questChainRejection(questID int64, err error) *questMutationRejection {
	var rejection *dnfquest.LinkedQuestChainResetRejection
	details := []int64(nil)
	parent := int64(0)
	if errors.As(err, &rejection) {
		details = rejection.Details
		parent = rejection.ParentQuestID
	}
	switch {
	case errors.Is(err, dnfquest.ErrQuestChainNotLinked):
		return questReject("chain_not_linked",
			"任务 %d 在 Script.pvf 里没有 [main quest]，不属于任何主线链路；链路重置只对主线链路的"+
				"子任务有意义；本次未做任何写入。", questID)
	case errors.Is(err, dnfquest.ErrQuestChainParentMissing):
		return questReject("chain_parent_missing",
			"任务 %d 的 [main quest] = %d 在 Script.pvf 的任务表里不存在，链条数据不完整；"+
				"本次未做任何写入。", questID, parent)
	case errors.Is(err, dnfquest.ErrQuestChainParentNotQuestClear):
		return questReject("chain_parent_not_quest_clear",
			"任务 %d 的父任务 %d 的 [type] 不是 quest clear / clear quest，不属于主线链路家族（本仓不猜家族成员）；"+
				"本次未做任何写入。", questID, parent)
	case errors.Is(err, dnfquest.ErrQuestChainRequestedNotManaged):
		return questReject("chain_requested_not_managed",
			"任务 %d 不是父任务 %d 的 [int data] 点名、且 [main quest] 指回父任务的无奖励 [sub] 子任务"+
				"（或它带 [depend give item]、不由父任务代管）：只删它一个会留下不一致的同族子任务，因此拒绝；"+
				"本次未做任何写入。", questID, parent)
	case errors.Is(err, dnfquest.ErrQuestChainRowMissing):
		return questReject("chain_row_missing",
			"任务 %d 不在该角色的任务账里（States / Progress 都没有这一行），没有可重置的链路世代；"+
				"本次未做任何写入。", questID)
	case errors.Is(err, dnfquest.ErrQuestChainChildUnresolved):
		return questReject("chain_child_unresolved",
			"任务 %d 的主线链路数据不完整：父任务 %d 的 [int data] 点名了 Script.pvf 里不存在的任务 %v；"+
				"拒绝「只重置看得见的那部分」；本次未做任何写入。", questID, parent, details)
	case errors.Is(err, dnfquest.ErrQuestChainIncompleteReactivation):
		return questReject("chain_incomplete_reactivation",
			"任务 %d 的主线链路无法整体重建：家族成员 %v 按当前角色的等级 / 职业 / 成长段接不回来；"+
				"拒绝留下「删了但没建回」的半截状态；本次未做任何写入。", questID, details)
	case errors.Is(err, dnfquest.ErrQuestChainNoManagedChildren):
		return questReject("chain_no_managed_children",
			"任务 %d 是 quest clear 父任务，但它的 [int data] 里没有任何被管理的链接子任务"+
				"（[main quest] 指回它、无 [depend give item] 的无奖励 [sub] 子任务），家族只有它自己，"+
				"没有可整族重置的世代；本次未做任何写入。", questID)
	case errors.Is(err, dnfquest.ErrQuestDefinitionMissing):
		return questReject("definition_missing",
			"任务 %d 不在当前运行时 Script.pvf 的任务表里（本仓不合成任务定义、不猜字段）；本次未做任何写入。", questID)
	case errors.Is(err, dnfquest.ErrCatalogEmpty):
		return questReject("catalog_unavailable",
			"任务目录为空（Script.pvf 未提供 n_quest/quest.lst），无法按 PVF 重建链路世代；本次未做任何写入。")
	case errors.Is(err, dnfquest.ErrQuestNotAcceptable):
		return questReject("chain_parent_not_acceptable",
			"任务的父任务当前不接受该角色（等级 / 职业 / 成长段 / 前置任务 / 冲突任务），无法按 PVF 重建这一代链路："+
				"%v；本次未做任何写入。", err)
	default:
		return questReject("chain_reset_failed", "主线链路重置失败: %v；本次未做任何写入。", err)
	}
}

// questChainResetEntry 区分链路重置的两个**发起点**（事务、校验、响应键、审计事件都
// 完全同形，只有调用哪个 plan 入口不同）：
//
//	questChainResetBySubQuest —— P6：请求的是 `[main quest] > 0` 的链接子任务；
//	questChainResetByParent   —— P6b：请求的是 quest clear 家族父任务自己（`== 0`）。
type questChainResetEntry int

const (
	questChainResetBySubQuest questChainResetEntry = iota
	questChainResetByParent
)

func (e questChainResetEntry) fromParent() bool { return e == questChainResetByParent }

// handleQuestChainUncomplete 是 uncomplete 撞到 [main quest] > 0 且链路重置开关 on 的
// 分支（上游 gm.go:1359-1372）。与 P5 的非链路分支同一形态：事务内重载角色与任务账、
// 事务内做全部校验、单写入点、失败由 CharacterSettlement 回滚（零写入）。
//
// 与上游的**有意偏离**（都在 quest/linked_chain_reset.go 的文件头注释里）：
// 任务账缺失不再静默成功；请求的任务行不存在、链条数据不完整、家族成员激活不回来
// 三种情况一律具名拒绝，绝不"只重置看到的那部分"。
func (s *Server) handleQuestChainUncomplete(w http.ResponseWriter, target questMutationTarget) {
	s.handleQuestChainReset(w, target, questChainResetBySubQuest)
}

// handleQuestChainParentUncomplete 是 P6b 的父任务入口：请求的是 quest clear 家族父任务
// 自己（MainQuestID == 0，且 [int data] 里至少有一个受管子任务）。除"发起点"之外与
// 子任务入口逐字同形 —— 同一段整族重置、同一组响应键、同一个审计事件。
func (s *Server) handleQuestChainParentUncomplete(w http.ResponseWriter, target questMutationTarget) {
	s.handleQuestChainReset(w, target, questChainResetByParent)
}

func (s *Server) handleQuestChainReset(w http.ResponseWriter, target questMutationTarget, entry questChainResetEntry) {
	if !s.requireOffline(w, "quest-uncomplete", target.panelID) {
		return
	}
	if target.group.CharacterSettlement == nil {
		s.writeQuestMutationFailure(w, "quest-uncomplete", target, errQuestSettlementUnavailable)
		return
	}
	var plan dnfquest.LinkedQuestChainResetPlan
	err := target.group.CharacterSettlement.WithinCharacterSettlement(target.ctx, target.characterID, func(tx dnfrepo.Group) error {
		if tx.Character == nil || tx.Quest == nil {
			return errQuestSettlementUnavailable
		}
		// 上游 gm.go:1344-1350：事务内重载角色，角色不存在直接失败。
		character, found, loadErr := tx.Character.Load(target.ctx, target.characterID)
		if loadErr != nil {
			return loadErr
		}
		if !found {
			return errCharacterMissingForQuest
		}
		record, found, loadErr := tx.Quest.Load(target.ctx, target.characterID)
		if loadErr != nil {
			return loadErr
		}
		// **有意偏离上游**：上游在任务账缺失时 `return nil`（gm.go:1355-1357），
		// 即"什么都没做但报成功"。本仓与 P5 的非链路分支同口径，明确失败。
		if !found {
			return questReject("quest_record_missing",
				"角色 %s 没有任务账记录，没有可重置的主线链路；本次未做任何写入。", target.characterID)
		}
		// 资格投影用**事务内**读到的角色（上游 gm.go:1360-1368 同），
		// 与 questReadContext 共用一处实现。
		eligibility, eligibilityErr := questEligibilityFromCharacter(character)
		if eligibilityErr != nil {
			return questReject("character_job_invalid",
				"角色存储的职业/成长值无效（job=%q grow_type=%d），无法按 PVF 重建链路世代；本次未做任何写入。",
				character.Job, growTypeValue(character))
		}
		// 两个入口的差别只有这一处：谁算"发起点的任务"。家族判据、删除范围、重建口径
		// 在 quest 包里是**同一份**实现（resetLinkedQuestChainFamily），这里不分叉。
		var (
			next    dnfquest.LinkedQuestChainResetPlan
			planErr error
		)
		if entry.fromParent() {
			next, planErr = target.catalog.PlanLinkedQuestChainResetFromParent(
				eligibility, record, target.questID, time.Now().UTC())
		} else {
			next, planErr = target.catalog.PlanLinkedQuestChainReset(
				eligibility, record, target.questID, time.Now().UTC())
		}
		if planErr != nil {
			return questChainRejection(target.questID, planErr)
		}
		plan = next
		return dnfrepo.SaveQuestFields(target.ctx, tx.Quest, plan.Record, plan.ChangedFields...)
	})
	if err != nil {
		s.writeQuestMutationFailure(w, "quest-uncomplete", target, err)
		return
	}
	s.logEvent("gm-quest-chain-reset", "panel_id", target.panelID, "character_id", target.characterID,
		"quest_id", target.questID, "parent_quest_id", plan.ParentQuestID,
		"from_parent", entry.fromParent(),
		"removed_ids", int64ListText(plan.RemovedQuestIDs),
		"reactivated_ids", int64ListText(plan.ReactivatedQuestIDs))
	writeOK(w, map[string]any{
		"characterId":   target.panelID,
		"operation":     "uncomplete",
		"questId":       target.questID,
		"chainReset":    true,
		"parentQuestId": plan.ParentQuestID,
		// 被删掉的已持久化世代（父+子，升序）与重新激活的一代（父 + 子，PVF 顺序）。
		"removedQuestIds":     plan.RemovedQuestIDs,
		"reactivatedQuestIds": plan.ReactivatedQuestIDs,
		"changed":             true,
		"rewardGranted":       false,
		"professionChanged":   false,
		"quest":               s.questPostSummary(target),
	})
}

// ---------------------------------------------------------------------------
// J11：批量完成（上游 GMCompleteQuestBatch gm_extended.go:832-907）
// ---------------------------------------------------------------------------

// questBatchRequest 是批量完成的请求体。camelCase 为主，另接受上游风格的下划线别名。
type questBatchRequest struct {
	QuestIDs    []int64 `json:"questIds"`
	QuestIDsAlt []int64 `json:"quest_ids"`
}

// questBatchEntry 是逐条结果（派工单要求：必须如实回报每一条的成功/跳过/失败与原因）。
type questBatchEntry struct {
	questID int64
	outcome string // completed / already_completed / rejected
	reason  string
	status  string
	name    string
	path    string
}

func (e questBatchEntry) payload() map[string]any {
	out := map[string]any{
		"questId": e.questID,
		"outcome": e.outcome,
		"status":  e.status,
		"reason":  e.reason,
		"name":    e.name,
		"path":    e.path,
	}
	return out
}

func (s *Server) handleQuestCompleteBatch(w http.ResponseWriter, r *http.Request) {
	if !s.questMutateEnabled() {
		s.rejectQuestMutateDisabled(w, "quest-complete-batch", 0, 0)
		return
	}
	ctx, group, panelID, character, ok := s.characterRequest(w, r)
	if !ok {
		return
	}
	body, ok := decodeBody[questBatchRequest](w, r)
	if !ok {
		return
	}
	requested := body.QuestIDs
	if len(requested) == 0 {
		requested = body.QuestIDsAlt
	}
	// 上游 gm_extended.go:834-836：空列表或超过 200 个直接拒绝。
	if len(requested) == 0 || len(requested) > questBatchMaxIDs {
		s.logEvent("gm-quest-batch-rejected", "panel_id", panelID, "reason", "batch_size",
			"requested", len(requested), "max", questBatchMaxIDs)
		writeJSON(w, map[string]any{
			"success":     false,
			"characterId": panelID,
			"reason":      "batch_size",
			"error": fmt.Sprintf("任务编号必须是 1..%d 个，当前 %d 个（本批未做任何写入）。",
				questBatchMaxIDs, len(requested)),
		})
		return
	}
	if !s.requireOffline(w, "quest-complete-batch", panelID) {
		return
	}
	catalog, err := s.pvf.questCatalog(s)
	if err != nil {
		writeFailure(w, err.Error())
		return
	}

	// ---- 事务前预校验（上游 gm_extended.go:844-860）----
	// 去重保序（上游用 map 且随机序；本仓按请求顺序，结果可复现），逐条给出结论。
	unique := make([]int64, 0, len(requested))
	entries := make([]questBatchEntry, 0, len(requested))
	seen := make(map[int64]struct{}, len(requested))
	problems := make([]questBatchEntry, 0, 4)
	for _, questID := range requested {
		if _, duplicate := seen[questID]; duplicate {
			continue // 上游 gm_extended.go:849-852：重复 ID 静默折叠。
		}
		seen[questID] = struct{}{}
		unique = append(unique, questID)
		entry := questBatchEntry{questID: questID}
		switch {
		case questID <= 0 || questID > math.MaxUint16:
			entry.outcome = "rejected"
			entry.reason = "out_of_range"
			problems = append(problems, entry)
			entries = append(entries, entry)
			continue
		}
		definition, found := catalog.Find(questID)
		if !found {
			entry.outcome = "rejected"
			entry.reason = "not_in_script_pvf"
			// 上游 gm_extended.go:855 的措辞：必须点名 Script.pvf（不合成定义）。
			problems = append(problems, entry)
			entries = append(entries, entry)
			continue
		}
		entry.name = strings.TrimSpace(definition.Name)
		entry.path = definition.Path
		if IsProfessionQuestReward(definition.RewardType) {
			entry.outcome = "rejected"
			entry.reason = "profession_excluded"
			problems = append(problems, entry)
			entries = append(entries, entry)
			continue
		}
		entries = append(entries, entry)
	}
	if len(problems) > 0 {
		// **全有或全无**：任何一条不合格 -> 整批拒绝、零写入（上游 gm_extended.go:846-859），
		// 但逐条列出问题与原因，不把部分失败吞成整体成功。
		reasons := make([]string, 0, len(problems))
		for _, problem := range problems {
			reasons = append(reasons, fmt.Sprintf("%d(%s)", problem.questID, problem.reason))
		}
		s.logEvent("gm-quest-batch-rejected", "panel_id", panelID, "character_id", character.CharacterID,
			"reason", "precheck_failed", "requested", len(requested), "unique", len(unique),
			"problems", strings.Join(reasons, ","))
		writeJSON(w, map[string]any{
			"success":     false,
			"characterId": panelID,
			"reason":      "precheck_failed",
			"requested":   len(requested),
			"unique":      len(unique),
			"results":     questBatchEntryList(entries),
			"problems":    questBatchEntryList(problems),
			"error": fmt.Sprintf(
				"批量完成被拒绝：%s。批量完成要求每一条都存在于当前运行时 Script.pvf、编号在 1..%d、且不是职业任务；"+
					"本批未做任何写入。",
				strings.Join(reasons, "、"), uint64(math.MaxUint16)),
		})
		return
	}

	if group.CharacterSettlement == nil {
		s.logEvent("gm-quest-batch-rejected", "panel_id", panelID, "character_id", character.CharacterID,
			"reason", "settlement_unavailable", "requested", len(requested))
		writeJSON(w, map[string]any{
			"success":     false,
			"characterId": panelID,
			"reason":      "settlement_unavailable",
			"error":       "任务结算事务能力未就绪（CharacterSettlement）；本批未做任何写入。",
		})
		return
	}

	// ---- 单事务、单写入点（上游 gm_extended.go:865-902）----
	// 事务内**不再有会失败的校验**：预校验已经把所有不合格的挡在外面，这里只会
	// 出现"已完成 -> 跳过"与"未完成 -> 标记完成"两种结论。
	changed := 0
	err = group.CharacterSettlement.WithinCharacterSettlement(ctx, character.CharacterID, func(tx dnfrepo.Group) error {
		if tx.Character == nil || tx.Quest == nil {
			return errQuestSettlementUnavailable
		}
		if _, found, loadErr := tx.Character.Load(ctx, character.CharacterID); loadErr != nil {
			return loadErr
		} else if !found {
			return errCharacterMissingForQuest
		}
		record, found, loadErr := tx.Quest.Load(ctx, character.CharacterID)
		if loadErr != nil {
			return loadErr
		}
		if !found {
			record = dnfrepo.QuestRecord{CharacterID: character.CharacterID}
		} else {
			record = dnfrepo.CloneQuest(record)
		}
		if record.States == nil {
			record.States = make(map[int64]dnfrepo.QuestState)
		}
		now := time.Now().UTC()
		for index, questID := range unique {
			definition, _ := catalog.Find(questID)
			state, known := questStateLookup(record, questID)
			if known && isCompletedQuestStatus(state.Status) {
				// 幂等：已经是完成态的行**一个字节都不改**（不覆盖 Extra、不重写
				// gm_batch_completed），如实回报 already_completed。
				entries[index].outcome = "already_completed"
				entries[index].reason = "already_completed"
				entries[index].status = strings.TrimSpace(state.Status)
				continue
			}
			state.Extra = copyQuestExtra(state.Extra)
			state.Status = questCompletedLiteral
			state.ProgressValue = 0
			state.UpdatedAt = now
			state.Extra[questPVFPathKey] = definition.Path
			state.Extra[questBatchCompletedKey] = "true"
			state.Extra[questRewardsGrantedKey] = "false"
			record.States[questID] = state
			delete(record.Progress, questID)
			entries[index].outcome = "completed"
			entries[index].status = questCompletedLiteral
			changed++
		}
		if changed == 0 {
			// 全部都是已完成：零写入（上游会整份重写一遍；本仓按幂等口径跳过）。
			return nil
		}
		record.CharacterID = character.CharacterID
		record.UpdatedAt = now
		return dnfrepo.SaveQuestFields(ctx, tx.Quest, record,
			dnfrepo.QuestFieldStates, dnfrepo.QuestFieldProgress)
	})
	if err != nil {
		s.logEvent("gm-quest-batch-failed", "panel_id", panelID, "character_id", character.CharacterID,
			"reason", "write_failed", "error", err.Error(), "requested", len(requested))
		writeJSON(w, map[string]any{
			"success":     false,
			"characterId": panelID,
			"reason":      "write_failed",
			"requested":   len(requested),
			"unique":      len(unique),
			"results":     questBatchEntryList(entries),
			"error":       "批量完成写入失败（整批回滚）: " + err.Error(),
		})
		return
	}
	changedIDs := make([]int64, 0, changed)
	unchangedIDs := make([]int64, 0, len(unique)-changed)
	for _, entry := range entries {
		if entry.outcome == "completed" {
			changedIDs = append(changedIDs, entry.questID)
		} else {
			unchangedIDs = append(unchangedIDs, entry.questID)
		}
	}
	s.logEvent("gm-quest-complete-batch", "panel_id", panelID, "character_id", character.CharacterID,
		"requested", len(requested), "unique", len(unique), "changed", changed,
		"unchanged", len(unchangedIDs), "changed_ids", int64ListText(changedIDs))
	writeOK(w, map[string]any{
		"characterId":       panelID,
		"operation":         "complete-batch",
		"requested":         len(requested),
		"unique":            len(unique),
		"changed":           changed,
		"unchanged":         len(unchangedIDs),
		"changedIds":        changedIDs,
		"alreadyCompleted":  unchangedIDs,
		"rewardGranted":     false,
		"professionChanged": false,
		"results":           questBatchEntryList(entries),
	})
}

func questBatchEntryList(entries []questBatchEntry) []map[string]any {
	out := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		out = append(out, entry.payload())
	}
	return out
}

func int64ListText(values []int64) string {
	if len(values) == 0 {
		return ""
	}
	parts := make([]string, 0, len(values))
	for _, value := range values {
		parts = append(parts, strconv.FormatInt(value, 10))
	}
	return strings.Join(parts, ",")
}
