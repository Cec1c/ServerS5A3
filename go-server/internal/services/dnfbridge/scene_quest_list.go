package dnfbridge

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"google.golang.org/protobuf/encoding/protowire"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfquest "longheng.io/server/internal/modules/dnf/quest"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

const (
	currentAcceptableQuestListMsgID = uint16(0x15)
	currentAcceptableQuestListKind  = "current_acceptable_quest_list"
	currentAcceptableQuestListEnum  = uint64(21)
)

func (s *Service) preloadQuestCatalog(ctx context.Context) error {
	catalog, err := s.loadQuestCatalog(ctx)
	if err != nil {
		return fmt.Errorf("preload quest catalog: %w", err)
	}
	snapshot := catalog.Snapshot()
	s.logPacketEvent("dnf-quest-catalog-loaded",
		"definitions", snapshot.Definitions,
		"epic", snapshot.Epic,
		"normal", snapshot.Normal,
		"list", dnfquest.DefaultList)
	return nil
}

func (s *Service) loadQuestCatalog(ctx context.Context) (*dnfquest.Catalog, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.questCatalogMu.Lock()
	defer s.questCatalogMu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.questCatalog != nil {
		return s.questCatalog, nil
	}
	if s.questCatalogLoadErr != nil {
		if !errors.Is(s.questCatalogLoadErr, context.Canceled) && !errors.Is(s.questCatalogLoadErr, context.DeadlineExceeded) {
			return nil, s.questCatalogLoadErr
		}
		// A caller deadline is not a permanent resource failure. Older callers
		// may have cached it; a live caller must be able to load the catalog.
		s.questCatalogLoadErr = nil
	}

	s.initialEquipmentMu.Lock()
	archive, err := s.initialEquipmentArchiveLocked()
	s.initialEquipmentMu.Unlock()
	if err != nil {
		if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			s.questCatalogLoadErr = err
		}
		return nil, err
	}
	index, err := dnfpvf.Build(ctx, archive, dnfpvf.BuildOptions{Lists: []string{dnfquest.DefaultList}})
	if err == nil {
		s.questCatalog, err = dnfquest.Load(ctx, index)
	}
	if err != nil {
		if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			s.questCatalogLoadErr = err
		}
		return nil, err
	}
	return s.questCatalog, nil
}

func buildCurrentAcceptableQuestListBody(level int, questIDs []int32) []byte {
	protobuf := make([]byte, 0, 8+len(questIDs)*3)
	protobuf = appendProtoVarint(protobuf, 1, currentAcceptableQuestListEnum)
	protobuf = appendProtoVarint(protobuf, 2, uint64(uint32(level)))
	if len(questIDs) > 0 {
		packed := make([]byte, 0, len(questIDs)*3)
		for _, questID := range questIDs {
			if questID <= 0 {
				continue
			}
			packed = protowire.AppendVarint(packed, uint64(uint32(questID)))
		}
		if len(packed) > 0 {
			protobuf = appendProtoBytes(protobuf, 4, packed)
		}
	}
	var writer packetWriter
	writer.writeUint32(uint32(len(protobuf)))
	writer.writeBytes(protobuf)
	return writer.bytes()
}

func (s *Service) buildCurrentAcceptableQuestListBodyForSession(ctx context.Context, session *gameSession) ([]byte, bool) {
	if s == nil || session == nil || session.selectedCharacterID == 0 {
		return nil, false
	}
	catalog, err := s.loadQuestCatalog(ctx)
	if err != nil {
		s.logGameEvent(session, "game-upper-current-acceptable-quest-list-skipped",
			"char_id", session.selectedCharacterID,
			"reason", "quest_catalog_unavailable",
			"error", err)
		return nil, false
	}
	repos, ok := s.repositoryGroup()
	if !ok || repos.Character == nil || repos.Quest == nil {
		s.logGameEvent(session, "game-upper-current-acceptable-quest-list-skipped",
			"char_id", session.selectedCharacterID,
			"reason", "character_or_quest_repository_unavailable")
		return nil, false
	}
	_, _, character, found := s.selectedCharacterForEnter(ctx, session)
	if !found {
		s.logGameEvent(session, "game-upper-current-acceptable-quest-list-skipped",
			"char_id", session.selectedCharacterID,
			"reason", "selected_character_not_found")
		return nil, false
	}
	job, validJob := characterJobByte(character)
	if !validJob || character.Level <= 0 {
		s.logGameEvent(session, "game-upper-current-acceptable-quest-list-skipped",
			"char_id", session.selectedCharacterID,
			"job", character.Job,
			"level", character.Level,
			"reason", "selected_character_job_or_level_invalid")
		return nil, false
	}
	characterID := character.CharacterID
	if characterID == "" {
		characterID = strconv.Itoa(int(session.selectedCharacterID))
	}
	record, hasRecord, err := repos.Quest.Load(ctx, characterID)
	if err != nil {
		s.logGameEvent(session, "game-upper-current-acceptable-quest-list-skipped",
			"char_id", session.selectedCharacterID,
			"reason", "quest_record_load_failed",
			"error", err)
		return nil, false
	}
	if !hasRecord {
		record = dnfrepo.QuestRecord{CharacterID: characterID}
	}
	record, repaired, repairErr := s.reconcileLegacyAbandonedExpertJobQuests(ctx, session, repos, catalog, character, record, hasRecord)
	if repairErr != nil {
		s.logGameEvent(session, "game-upper-current-acceptable-quest-list-skipped",
			"char_id", session.selectedCharacterID,
			"reason", "legacy_abandoned_expert_job_quest_repair_failed",
			"error", repairErr)
		return nil, false
	}
	eligibility := dnfquest.CharacterEligibility{
		Level:    character.Level,
		Job:      int(job),
		GrowType: characterGrowTypeInt(character),
	}
	var policyErr error
	if character.CharacterID == strconv.Itoa(int(session.selectedCharacterID)) && strings.TrimSpace(character.AccountID) == s.accountIDForSession(session) && record.CharacterID == character.CharacterID {
		eligibility, policyErr = catalog.WithEpicRoadEvent350Eligibility(eligibility, s.currentEpicRoadActivityAvailable())
	}
	if policyErr != nil {
		s.logGameEvent(session, "game-upper-current-acceptable-quest-list-skipped", "reason", "epic350_prerequisite_resource_mismatch", "error", policyErr)
		return nil, false
	}
	// 每日任务的跨日投影必须和本次会话的玩法时钟一致，不能另取墙上时钟。
	result := catalog.QuestListAt(eligibility, record, s.gameplayNow())
	body := buildCurrentAcceptableQuestListBody(character.Level, result.IDs)
	// 2026-10-02 (V20): `quest_ids` 是**有界**日志（前 32 条），而这一次排查里它被当成了全量：
	// 限定任务 10019/2901 排在第 32 条之后，于是"不在 quest_ids 里"被误读成"服务端没下发"，
	// 直到直接解 op21 包体才推翻。这里把"截断"显式写出来，并补两个针对性字段：
	//   active_ids_in_list  = 进列表的进行中任务（客户端"进行中"页直接依赖这些）
	//   active_ids_missing  = **不在**列表的进行中任务（真正的服务端缺陷信号，正常应为空）
	// 两者都是小集合（正常 ≤ 十几个），不会造成日志膨胀。
	activeInList, activeMissing := questActiveListMembership(result.IDs, record)
	s.logGameEvent(session, "game-upper-current-acceptable-quest-list-built",
		"char_id", session.selectedCharacterID,
		"character_id", characterID,
		"job", job,
		"level", character.Level,
		"grow_type", characterGrowTypeStat(character),
		"quest_record_found", hasRecord,
		"legacy_abandoned_expert_job_quests_repaired", repaired,
		"quest_list_count", len(result.IDs),
		"acceptable_count", len(result.IDs)-result.ActiveCount,
		"active_included_count", result.ActiveCount,
		"epic_count", result.EpicCount,
		"not_listed_accept_preflight_rejected_count", result.PreflightExcluded,
		"quest_ids", boundedQuestLogIDs(result.IDs, 32),
		"quest_ids_is_bounded_log", len(result.IDs) > 32,
		"quest_ids_total", len(result.IDs),
		"active_ids_in_list", int32ListText(activeInList),
		"active_ids_missing", int32ListText(activeMissing),
		"msg_id", currentAcceptableQuestListMsgID,
		"protobuf_enum", currentAcceptableQuestListEnum,
		"body_len", len(body),
		"body_source", "current_exe_sub_1D6FB50_pb_level_and_pvf_db_quest_ids_including_active_for_all_view")
	return body, true
}

// questActiveListMembership 把"进行中任务"（= op574 快照的行来源 currentSelectAckQuestRows）
// 和刚构建出来的 op21 可接受列表对上号。只读比对，不改任何状态；正常情况 missing 应为空。
func questActiveListMembership(listed []int32, record dnfrepo.QuestRecord) (inList, missing []int32) {
	rows := currentSelectAckQuestRows(record, true)
	if len(rows) == 0 {
		return nil, nil
	}
	present := make(map[int32]struct{}, len(listed))
	for _, id := range listed {
		present[id] = struct{}{}
	}
	for _, row := range rows { // currentSelectAckQuestRows 已按 questID 升序
		id := int32(row.questID)
		if id <= 0 {
			continue
		}
		if _, ok := present[id]; ok {
			inList = append(inList, id)
		} else {
			missing = append(missing, id)
		}
	}
	return inList, missing
}

// int32ListText 渲染一个 ID 列表给日志（空列表写成 "-"，免得空字段被当成缺字段）。
func int32ListText(values []int32) string {
	if len(values) == 0 {
		return "-"
	}
	parts := make([]string, 0, len(values))
	for _, value := range values {
		parts = append(parts, strconv.FormatInt(int64(value), 10))
	}
	return strings.Join(parts, " ")
}

func (s *Service) reconcileLegacyAbandonedExpertJobQuests(
	ctx context.Context,
	session *gameSession,
	repos dnfrepo.Group,
	catalog *dnfquest.Catalog,
	character dnfrepo.CharacterRecord,
	record dnfrepo.QuestRecord,
	hasRecord bool,
) (dnfrepo.QuestRecord, bool, error) {
	giveUpState := numericCharacterStatValue(character, currentExpertJobGiveUpStateStat)
	if !hasRecord || numericCharacterStatValue(character, "expert_job_type") != 0 || giveUpState <= 0 {
		return record, false, nil
	}
	transitionIDs := catalog.ExpertJobTransitionQuestIDs()
	terminalIDs := catalog.ExpertJobTransitionTerminalQuestIDs()
	if len(transitionIDs) == 0 || len(terminalIDs) == 0 {
		return record, false, nil
	}
	transitionSet := make(map[int64]struct{}, len(transitionIDs))
	for _, questID := range transitionIDs {
		transitionSet[questID] = struct{}{}
	}
	for questID, questState := range record.States {
		if _, ok := transitionSet[questID]; ok && strings.EqualFold(strings.TrimSpace(questState.Status), "active") {
			return record, false, nil
		}
	}
	for questID, questState := range record.Progress {
		if _, ok := transitionSet[questID]; ok && strings.EqualFold(strings.TrimSpace(questState.Status), "active") {
			return record, false, nil
		}
	}
	hasCompletedTerminal := false
	for _, questID := range terminalIDs {
		for _, questState := range []dnfrepo.QuestState{record.States[questID], record.Progress[questID]} {
			if strings.EqualFold(strings.TrimSpace(questState.Status), "completed") {
				hasCompletedTerminal = true
				break
			}
		}
		if hasCompletedTerminal {
			break
		}
	}
	if !hasCompletedTerminal {
		return record, false, nil
	}
	repaired := dnfrepo.CloneQuest(record)
	removed := 0
	for _, questID := range transitionIDs {
		if _, exists := repaired.States[questID]; exists {
			delete(repaired.States, questID)
			removed++
		}
		if _, exists := repaired.Progress[questID]; exists {
			delete(repaired.Progress, questID)
			removed++
		}
	}
	if removed == 0 {
		return record, false, nil
	}
	repaired.UpdatedAt = time.Now().UTC()
	if err := dnfrepo.SaveQuestFields(ctx, repos.Quest, repaired, dnfrepo.QuestFieldStates, dnfrepo.QuestFieldProgress); err != nil {
		return record, false, err
	}
	s.logGameEvent(session, "game-legacy-abandoned-expert-job-quests-repaired",
		"char_id", session.selectedCharacterID,
		"character_id", repaired.CharacterID,
		"give_up_state", giveUpState,
		"removed_rows", removed,
		"transition_quest_count", len(transitionIDs),
		"terminal_quest_count", len(terminalIDs))
	return repaired, true, nil
}

func (s *Service) sendCurrentAcceptableQuestListForSession(session *gameSession, source string) error {
	if err := s.sendCurrentAcceptableQuestListOnlyForSession(session, source); err != nil {
		return err
	}
	return s.sendCurrentActiveQuestSnapshotForSession(session, source+"_after_acceptable_op21")
}

// sendCurrentInitialTownQuestSnapshotsLocked publishes the task-manual
// definition list at the earliest selected-actor-safe login boundary. The
// caller holds session.townMu as part of the initial-town route; this helper
// marks only that route's one-shot snapshot so later quest accept/finish
// refreshes can still send op21/op574 normally.
func (s *Service) sendCurrentInitialTownQuestSnapshotsLocked(session *gameSession, source string) error {
	if s == nil || session == nil || session.selectedCharacterID == 0 {
		return nil
	}
	if session.initialTownQuestSnapshotsSent {
		s.logGameEvent(session, "game-initial-town-quest-snapshots-duplicate-skipped",
			"char_id", session.selectedCharacterID,
			"source", source,
			"reason", "initial_town_op21_op574_already_sent_before_typed_op24")
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	body, ok := s.buildCurrentAcceptableQuestListBodyForSession(ctx, session)
	cancel()
	if !ok {
		return nil
	}
	// 厂商顺序：op19/op2 → **op411 PVP 任务表** → op19/op205/op105/op21/op574。
	// 缺这张表时客户端进城会开出"决斗场任务书"占位窗（2026-09-26 现场弹窗）。
	if err := s.sendCurrentPVPMissionCombo(session, source+"_before_initial_town_op21"); err != nil {
		return err
	}
	s.logGameEvent(session, "game-initial-town-current-acceptable-quest-list-send",
		"char_id", session.selectedCharacterID,
		"msg_id", currentAcceptableQuestListMsgID,
		"classification", 0,
		"body_len", len(body),
		"source", source,
		"body_source", "current_exe_sub_1D6FB50_pb_level_and_pvf_db_quest_ids_including_active_for_all_view",
		"reason", "f1_all_task_manual_needs_definition_list_before_visible_scene_interaction")
	if err := s.sendGameUpperRawClass(session, currentAcceptableQuestListMsgID, body, 0); err != nil {
		return err
	}
	if err := s.sendCurrentActiveQuestSnapshotForSession(session, source+"_after_initial_town_acceptable_op21"); err != nil {
		return err
	}
	session.initialTownQuestSnapshotsSent = true
	return nil
}

func (s *Service) sendCurrentAcceptableQuestListOnlyForSession(session *gameSession, source string) error {
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	body, ok := s.buildCurrentAcceptableQuestListBodyForSession(ctx, session)
	cancel()
	if !ok {
		return nil
	}
	s.logGameEvent(session, "game-upper-current-acceptable-quest-list-send",
		"char_id", session.selectedCharacterID,
		"msg_id", currentAcceptableQuestListMsgID,
		"classification", 0,
		"body_len", len(body),
		"source", source,
		"body_source", "current_exe_sub_1D6FB50_pb_level_and_pvf_db_quest_ids_including_active_for_all_view")
	if err := s.sendGameUpperRawClass(session, currentAcceptableQuestListMsgID, body, 0); err != nil {
		return err
	}
	return nil
}

func boundedQuestLogIDs(ids []int32, limit int) []int32 {
	if limit <= 0 || len(ids) == 0 {
		return nil
	}
	if len(ids) < limit {
		limit = len(ids)
	}
	return append([]int32(nil), ids[:limit]...)
}
