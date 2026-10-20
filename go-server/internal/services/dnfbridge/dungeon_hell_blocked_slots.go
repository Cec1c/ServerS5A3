package dnfbridge

import (
	"context"
	"strconv"
	"strings"

	"longheng.io/server/internal/modules/dnf/worldmap"
)

// 本文件实现操作者清单第 69 条「挑战深渊进不去副本」的服务端那一半：
// **逐槽位**深渊派对准入判定 + 把不合规槽位下发给客户端。
//
// # 厂商口径（本机只读源码，逐行照抄）
//
// `S4A21-完整包-20260923\源码\服务端\Server\DfoServer\`
//
//  1. 准入判据 = **任务完成状态**，真源是 worldmap area 的 `[hell quest]` 段：
//
//     Game\Dungeon\DungeonEntryCostService.cs:486-525
//     internal bool CheckHellQuestRequirement(int characterId, WorldMapArea area, out int missingQuestId)
//     => EvaluateHellQuestRequirement(area,
//     questId => questId <= ushort.MaxValue && repository.IsQuestCleared(characterId, (ushort)questId),
//     out missingQuestId);
//
//     GameWorld\WorldMap.cs:412 ParseIntList("hell quest", content, area.HellQuestIds)
//     —— 段落名就是 `[hell quest]`，任务号**只能**从这份文档读，不得硬编码。
//
//  2. **按槽位逐个判**，不合规的进 `BlockedSlots`，**不整队拒绝**：
//
//     Network\Handlers\Dungeon\DungeonEntryHandler.cs:1465-1500
//     foreach (var member in state.Members) {
//     if (state.WorldMapArea?.HellDungeon != true) continue;      // 非深渊区域：根本没有门槛
//     if (_svc.EntryAdmission.CheckHellQuestRequirement(member.CharacterId, state.WorldMapArea, out var missingQuestId)) continue;
//     state.BlockedSlots.Add(member.SlotIndex);
//     state.BlockReasons.Add($"slot={member.SlotIndex}:quest={missingQuestId}");
//     }
//     FileLogger.Log($"... ENTER_SELECT_DUNGEON hell eligibility: cid=... worldMapArea=... hell=... blocked=[{...}]");
//
//  3. blocked 槽位随**进本选择页**下发（`NOTI 0x001B`，体 =
//     `EnterSelectDungeonStateBuilder.BuildA21EnterSelectDungeon(userIds, hellQuestBlockedSlots)`，
//     `Network\Handlers\Dungeon\DungeonEntryHandler.cs:780-786`）。布局见
//     dungeon_context_op27.go（本仓 `currentDungeonContext` 的 `List0` = `u8 条数 + N×u16`
//     就在厂商 `blockedCount + blockedCount×u16 slot` 的同一偏移上）。
//
// # 本仓现状（改动前后）
//
// 改动前（`DNFBRIDGE_HELL_BLOCKED_SLOTS` 未设置 = 本档零值 = **逐字节今天**）：
// `evaluateHellPartyDungeonEntry` 一律 `Intercepted`，`[hell quest]` 没做完回 0xAD、
// 做完了也回 0x01（`reason=hell_party_runtime_not_supported_by_this_build`），
// 也就是说**只要带深渊标志就进不去副本**（第 69 条的操作者报障）。
//
// 改动后（= `on`）：
//   - 按厂商口径**逐槽位**判 `[hell quest]`，不合规的槽位进 blocked 列表；
//   - **不再因为深渊准入拒绝进图**（不整队拒绝，也不拒发起者）：请求照常落到普通进图链，
//     op16 成功体照发（第 57 条的"必回包"因此只会更强，绝不会退回静默丢弃）；
//   - blocked 列表随进本选择页那条 class0/op27 下发（`blockedCount + N×u16 slot`）。
//
// # 开关
//
//	`DNFBRIDGE_HELL_BLOCKED_SLOTS`
//	  - 未设置/空/任何非 `on` 值（默认，零值）= 改动前行为，**逐字节不变**（含第 57 条的
//	    必回包拒绝）；
//	  - `on` = 逐槽位判定 + blocked 槽位下发 + 深渊请求照常进图。
//
// 一键回退 = 删除该 env（或写成 `off`）。
const hellBlockedSlotsEnv = "DNFBRIDGE_HELL_BLOCKED_SLOTS"

const hellBlockedSlotsValueOn = "on"

// hellBlockedSlotsMode 是 hellBlockedSlotsEnv 的归一化取值。
type hellBlockedSlotsMode int

const (
	// hellBlockedSlotsOff 是默认档（零值）：逐字节等于改动前。
	hellBlockedSlotsOff hellBlockedSlotsMode = iota
	// hellBlockedSlotsOn = 逐槽位判定 + blocked 下发。
	hellBlockedSlotsOn
)

func (mode hellBlockedSlotsMode) String() string {
	if mode == hellBlockedSlotsOn {
		return hellBlockedSlotsValueOn
	}
	return "off"
}

// deliversBlockedSlots 报告是否启用"逐槽位判定 + blocked 槽位下发"。
func (mode hellBlockedSlotsMode) deliversBlockedSlots() bool {
	return mode == hellBlockedSlotsOn
}

// readHellBlockedSlotsMode 解析 DNFBRIDGE_HELL_BLOCKED_SLOTS。
// **只有显式 `on` 才启用**；未设置/空/其它任何值（含 `true`/`1`）都取默认档 `off`，
// 保证"默认 = 今天逐字节"这条承诺不会被一个手滑的别名破坏。
func readHellBlockedSlotsMode(value string) hellBlockedSlotsMode {
	if strings.EqualFold(strings.TrimSpace(value), hellBlockedSlotsValueOn) {
		return hellBlockedSlotsOn
	}
	return hellBlockedSlotsOff
}

// hellBlockedSlotsGate 返回当前 Service 的归一化档位（零值 options = off = 今天）。
func (s *Service) hellBlockedSlotsGate() hellBlockedSlotsMode {
	if s == nil {
		return hellBlockedSlotsOff
	}
	return s.options.hellBlockedSlots
}

// pvf_status 取值：这一区域的深渊门槛到底"读到没有"。
// 每一种都以真档 PVF 的 worldmap area 文档为唯一真源（不硬编码任务号）。
const (
	// hellPVFStatusAreaUnresolved：选中的副本不属于任何 worldmap area。
	hellPVFStatusAreaUnresolved = "pvf_area_unresolved"
	// hellPVFStatusNotHellDungeon：`[hell dungeon]` 没写 1 ⇒ 该区域不是深渊区域。
	hellPVFStatusNotHellDungeon = "pvf_hell_dungeon_flag_off"
	// hellPVFStatusSectionAbsent：文档里**没有** `[hell quest]` 段落 ⇒ 没有深渊门槛。
	hellPVFStatusSectionAbsent = "pvf_hell_quest_section_absent"
	// hellPVFStatusIDsEmpty：有 `[hell quest]` 段落但一个有效任务号都没有 ⇒ 没有门槛。
	hellPVFStatusIDsEmpty = "pvf_hell_quest_ids_empty"
	// hellPVFStatusIDsParsed：`[hell quest]` 读到了任务号 ⇒ 门槛成立。
	hellPVFStatusIDsParsed = "pvf_hell_quest_ids_parsed"
)

// hellQuestRequirement 是一次"这一区域要不要深渊门槛"的取证结果。
type hellQuestRequirement struct {
	AreaID           int64
	AreaResolved     bool
	HellDungeon      bool
	RequiredQuestIDs []int64
	// PvfStatus 是上面那组常量之一，可 grep。
	PvfStatus string
	// Gate 为真才需要逐槽位判定。
	Gate bool
	// Reason 是给日志看的稳定判据串。
	Reason string
}

// resolveHellQuestRequirement 从**真档 PVF** 的 worldmap area 判定这一区域是否需要
// 深渊任务门槛。任何"读不到"的情形都算"该区域没有深渊门槛"（不拦），但都会在
// PvfStatus 里写清楚是哪一种。
func resolveHellQuestRequirement(area worldmap.Area, areaResolved bool) hellQuestRequirement {
	result := hellQuestRequirement{
		AreaResolved: areaResolved,
		PvfStatus:    hellPVFStatusAreaUnresolved,
		Reason:       "hell_party_area_not_indexed",
	}
	if !areaResolved {
		return result
	}
	result.AreaID = area.ID
	result.HellDungeon = area.HellDungeon.Value == 1
	if !result.HellDungeon {
		// 厂商 `DungeonEntryHandler.cs:1468-1469`：`if (state.WorldMapArea?.HellDungeon != true) continue;`
		result.PvfStatus = hellPVFStatusNotHellDungeon
		result.Reason = "hell_party_area_is_not_hell_dungeon"
		return result
	}
	if !area.HasSection("hell quest") {
		result.PvfStatus = hellPVFStatusSectionAbsent
		result.Reason = "hell_party_area_has_no_hell_quest_section"
		return result
	}
	required := make([]int64, 0, len(area.HellQuestIDs))
	for _, questID := range area.HellQuestIDs {
		if questID <= 0 {
			continue
		}
		required = append(required, questID)
	}
	result.RequiredQuestIDs = required
	if len(required) == 0 {
		result.PvfStatus = hellPVFStatusIDsEmpty
		result.Reason = "hell_party_area_hell_quest_list_empty"
		return result
	}
	result.PvfStatus = hellPVFStatusIDsParsed
	result.Gate = true
	result.Reason = "hell_party_hell_quest_requirement_active"
	return result
}

// hellEligibilityMember 是参与逐槽位判定的一个 roster 槽位。
type hellEligibilityMember struct {
	// Slot 是客户端队伍框的固定槽位；单人/无队伍回退仍为 0。
	Slot uint16
	// CharacterID 是该槽位的角色 id。
	CharacterID uint16
}

// hellBlockedSlot 是"这一个槽位不合规"的取证记录。
type hellBlockedSlot struct {
	Slot         uint16
	CharacterID  uint16
	MissingQuest int64
}

// hellBlockedSlotRecord 是缓存在会话上的 blocked 列表（供 op27 下发体使用）。
type hellBlockedSlotRecord struct {
	Slots     []hellBlockedSlot
	AreaID    int64
	PvfStatus string
	// Delivered 记录这份列表是否已经随某条 class0/op27 发出过。
	Delivered bool
}

// hellEligibilityEvaluation 是一次逐槽位判定的完整结果。
type hellEligibilityEvaluation struct {
	Requirement hellQuestRequirement
	// Members 是按槽位升序的 roster（单人/无队伍时只有自己那一个槽位）。
	Members []hellEligibilityMember
	// Blocked 是按槽位升序的"不合规槽位"。
	Blocked []hellBlockedSlot
	// Evaluated 是真正完成判定的槽位数（读不到任务记录的槽位不计入）。
	Evaluated int
	// QuestRecordUnavailable 是任务记录读不到的槽位数（fail-open：不 block，但要记）。
	QuestRecordUnavailable int
	// RequesterSlot / RequesterBlocked 是发起这次请求的那一个槽位。
	RequesterSlot    uint16
	RequesterBlocked bool
	// Reason 是给日志看的稳定判据串。
	Reason string
}

// hellEligibilityRoster 取"这一支队伍此刻的稳定槽位序"。
//
// 取源与整个进图链同口径：权威 RuntimePartyManager（`runtimePartyStateForSession`，
// 缺失时回退会话本地投影），槽位 = `runtimePartyMembers` 成员的固定 `Slot`
// （与 `runtimePartyMemberIndex`、队伍框行号、op153 的 slot 字节同源）。
// 单人/无队伍时厂商的 host slot 也是 0（`TryResolveDungeonHostSlot`：`currentParty == null`
// 直接返回 `slotIndex = 0`），所以这里回落到"只有自己，slot 0"。
func (s *Service) hellEligibilityRoster(session *gameSession) []hellEligibilityMember {
	if session == nil || session.selectedCharacterID == 0 {
		return nil
	}
	state := s.runtimePartyStateForSession(session)
	members := runtimePartyMembers(state)
	roster := make([]hellEligibilityMember, 0, len(members))
	singleMember := len(members) <= 1
	for _, member := range members {
		if member.UserID == 0 {
			continue
		}
		slot := member.Slot
		if singleMember {
			slot = 0
		}
		roster = append(roster, hellEligibilityMember{Slot: uint16(slot), CharacterID: member.UserID})
	}
	if len(roster) == 0 {
		return []hellEligibilityMember{{Slot: 0, CharacterID: session.selectedCharacterID}}
	}
	return roster
}

// evaluateHellEligibility 按厂商口径逐槽位判定 `[hell quest]` 完成状态。
//
// 判定用的"已完成"集合与既有进图闸门同源（`dungeonQuestStateSets` 的 cleared 集），
// 即同一份持久化任务记录，不新造第二套口径。
func (s *Service) evaluateHellEligibility(
	ctx context.Context,
	session *gameSession,
	requirement hellQuestRequirement,
) hellEligibilityEvaluation {
	evaluation := hellEligibilityEvaluation{Requirement: requirement}
	if session == nil || session.selectedCharacterID == 0 {
		evaluation.Reason = "hell_party_selected_character_unavailable"
		return evaluation
	}
	evaluation.Members = s.hellEligibilityRoster(session)
	evaluation.RequesterSlot = 0
	for _, member := range evaluation.Members {
		if member.CharacterID == session.selectedCharacterID {
			evaluation.RequesterSlot = member.Slot
			break
		}
	}
	if !requirement.Gate {
		evaluation.Reason = requirement.Reason
		return evaluation
	}
	if ctx == nil {
		ctx = context.Background()
	}
	for _, member := range evaluation.Members {
		record, _, err := s.dungeonQuestRecord(ctx, member.CharacterID)
		if err != nil {
			// fail-open：读不到任务记录就**不 block**（blocked 只影响客户端 UI 提示，
			// 误报"这个槽位不能打深渊"比漏报更糟）。原因记进事件，绝不含混。
			evaluation.QuestRecordUnavailable++
			continue
		}
		_, cleared := dungeonQuestStateSets(record)
		evaluation.Evaluated++
		missing := int64(0)
		for _, questID := range requirement.RequiredQuestIDs {
			if _, ok := cleared[questID]; !ok {
				missing = questID
				break
			}
		}
		if missing == 0 {
			continue
		}
		evaluation.Blocked = append(evaluation.Blocked, hellBlockedSlot{
			Slot:         member.Slot,
			CharacterID:  member.CharacterID,
			MissingQuest: missing,
		})
		if member.CharacterID == session.selectedCharacterID {
			evaluation.RequesterBlocked = true
		}
	}
	if len(evaluation.Blocked) == 0 {
		evaluation.Reason = "hell_party_all_members_eligible"
		return evaluation
	}
	evaluation.Reason = "hell_party_quest_required"
	return evaluation
}

// recordHellBlockedSlots 把一次判定的结果写进会话缓存，供后面那条 class0/op27 下发体读取。
// 每次判定**整体覆盖**（含清空），所以永远不会把上一次选择/上一支队伍的槽位留在缓存里。
func recordHellBlockedSlots(session *gameSession, evaluation hellEligibilityEvaluation) {
	if session == nil {
		return
	}
	session.hellBlocked = hellBlockedSlotRecord{
		Slots:     append([]hellBlockedSlot(nil), evaluation.Blocked...),
		AreaID:    evaluation.Requirement.AreaID,
		PvfStatus: evaluation.Requirement.PvfStatus,
	}
}

// clearHellBlockedSlots 清空会话上的 blocked 列表（离开选图世界时调用）。
func clearHellBlockedSlots(session *gameSession) {
	if session == nil {
		return
	}
	session.hellBlocked = hellBlockedSlotRecord{}
}

// hellBlockedSlotsPending 报告"有 blocked 槽位还没随 op27 下发过"。
// 默认档恒为 false（这条路径在默认档一个字节都不碰）。
func (s *Service) hellBlockedSlotsPending(session *gameSession) bool {
	if !s.hellBlockedSlotsGate().deliversBlockedSlots() || session == nil {
		return false
	}
	return len(session.hellBlocked.Slots) > 0 && !session.hellBlocked.Delivered
}

// hellBlockedSlotNumbers 返回要写进 op27 下发体的 u16 槽位（升序，去重）。
// 默认档恒返回 nil（= 下发体逐字节等于今天那 37 字节全零体）。
func (s *Service) hellBlockedSlotNumbers(session *gameSession) []uint16 {
	if !s.hellBlockedSlotsGate().deliversBlockedSlots() || session == nil {
		return nil
	}
	if len(session.hellBlocked.Slots) == 0 {
		return nil
	}
	slots := make([]uint16, 0, len(session.hellBlocked.Slots))
	seen := make(map[uint16]struct{}, len(session.hellBlocked.Slots))
	for _, blocked := range session.hellBlocked.Slots {
		if _, ok := seen[blocked.Slot]; ok {
			continue
		}
		seen[blocked.Slot] = struct{}{}
		slots = append(slots, blocked.Slot)
	}
	sortUint16Ascending(slots)
	return slots
}

// markHellBlockedSlotsDelivered 记录"这份 blocked 列表已经下发过"。
func markHellBlockedSlotsDelivered(session *gameSession) {
	if session == nil {
		return
	}
	session.hellBlocked.Delivered = true
}

// logHellEligibilityBlocked 逐槽位记一条**可 grep** 的 blocked 事件。
// 字段：char_id（被 block 的那名成员）/ slot / missing_quest / area / pvf_status / mode。
func (s *Service) logHellEligibilityBlocked(
	session *gameSession,
	evaluation hellEligibilityEvaluation,
	blocked hellBlockedSlot,
	mode string,
	source string,
) {
	if s == nil || session == nil {
		return
	}
	s.logGameEvent(session, "game-hell-eligibility-blocked",
		"conn_id", session.connID,
		"source", source,
		"char_id", blocked.CharacterID,
		"slot", blocked.Slot,
		"missing_quest", blocked.MissingQuest,
		"area", evaluation.Requirement.AreaID,
		"area_resolved", evaluation.Requirement.AreaResolved,
		"hell_dungeon", evaluation.Requirement.HellDungeon,
		"required_hell_quest_ids", formatQuestIDList(evaluation.Requirement.RequiredQuestIDs),
		"pvf_status", evaluation.Requirement.PvfStatus,
		"mode", mode,
		"requester_char_id", session.selectedCharacterID,
		"requester_slot", evaluation.RequesterSlot,
		"roster_slots", formatHellEligibilityRoster(evaluation.Members),
		"source_section", "worldmap_area_hell_quest",
		"vendor_evidence", "DungeonEntryCostService.CheckHellQuestRequirement+DungeonEntryHandler.ENTER_SELECT_DUNGEON_hell_eligibility")
}

// logHellEligibilityEvaluated 记一条汇总事件（含"没有门槛"与"全员通过"这两种
// 没有 blocked 的情形），让复测单行就能判读 pvf_status 与槽位数。
func (s *Service) logHellEligibilityEvaluated(
	session *gameSession,
	evaluation hellEligibilityEvaluation,
	mode string,
	source string,
) {
	if s == nil || session == nil {
		return
	}
	s.logGameEvent(session, "game-hell-eligibility-evaluated",
		"conn_id", session.connID,
		"source", source,
		"char_id", session.selectedCharacterID,
		"area", evaluation.Requirement.AreaID,
		"area_resolved", evaluation.Requirement.AreaResolved,
		"hell_dungeon", evaluation.Requirement.HellDungeon,
		"hell_quest_gate", evaluation.Requirement.Gate,
		"required_hell_quest_ids", formatQuestIDList(evaluation.Requirement.RequiredQuestIDs),
		"pvf_status", evaluation.Requirement.PvfStatus,
		"member_count", len(evaluation.Members),
		"roster_slots", formatHellEligibilityRoster(evaluation.Members),
		"evaluated_slots", evaluation.Evaluated,
		"quest_record_unavailable_slots", evaluation.QuestRecordUnavailable,
		"blocked_count", len(evaluation.Blocked),
		"blocked_slots", formatHellBlockedSlots(evaluation.Blocked),
		"requester_slot", evaluation.RequesterSlot,
		"requester_blocked", evaluation.RequesterBlocked,
		"mode", mode,
		"reason", evaluation.Reason,
		"body_source", "worldmap_area_hell_quest_pvf_and_persisted_quest_record")
}

// formatHellEligibilityRoster 把 roster 渲染成 `0:99,1:100`。
func formatHellEligibilityRoster(members []hellEligibilityMember) string {
	if len(members) == 0 {
		return ""
	}
	parts := make([]string, 0, len(members))
	for _, member := range members {
		parts = append(parts, strconv.Itoa(int(member.Slot))+":"+strconv.Itoa(int(member.CharacterID)))
	}
	return strings.Join(parts, ",")
}

// formatHellBlockedSlots 把 blocked 列表渲染成 `slot=1:quest=7777` 这样的判读串。
func formatHellBlockedSlots(blocked []hellBlockedSlot) string {
	if len(blocked) == 0 {
		return ""
	}
	parts := make([]string, 0, len(blocked))
	for _, slot := range blocked {
		parts = append(parts, "slot="+strconv.Itoa(int(slot.Slot))+":quest="+strconv.FormatInt(slot.MissingQuest, 10))
	}
	return strings.Join(parts, ",")
}

// formatHellBlockedSlotNumbers 把已经进下发体的槽位号渲染成 `1,2`。
func formatHellBlockedSlotNumbers(slots []uint16) string {
	if len(slots) == 0 {
		return ""
	}
	parts := make([]string, 0, len(slots))
	for _, slot := range slots {
		parts = append(parts, strconv.Itoa(int(slot)))
	}
	return strings.Join(parts, ",")
}

// sortUint16Ascending 是对极小切片（≤ 队伍上限 4）的插入排序，避免为一个日志/下发
// 列表引入排序依赖；输出顺序与客户端队伍框行号一致（升序）。
func sortUint16Ascending(values []uint16) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j-1] > values[j]; j-- {
			values[j-1], values[j] = values[j], values[j-1]
		}
	}
}

// buildCurrentDungeonEntryPackets 是 Service 侧的进图包构建入口。
//
// 默认档（未设置 `DNFBRIDGE_HELL_BLOCKED_SLOTS`）逐字节走改动前那条
// `buildCurrentDungeonEntryPackets`：`entry_option != 0` 仍然按"当前 EXE 不支持的模式"
// 拒绝（op16 回 `{0, 0x02, 0}`）。
//
// `on` 档：深渊派对请求（op16 body[5:7] 非 0 = 厂商 `HellPartyRequestFlag`）不再被当成
// 不支持的模式，整条普通进图链照常构建并下发 —— 否则"逐槽位判定 + blocked 下发"做得再对，
// 玩家点了「挑战深渊派对」还是停在选图界面（第 69 条报障的另一半就在这一行）。
//
// `DNFBRIDGE_DUNGEON_ENTRY_SELECTION_MODE=allow`（默认 strict = 逐字节今天）是**另一条
// 独立轴**：放行 op16 `body[7]`（`SelectionMode`）的非 0 值。两条轴互不影响，可以各自单独
// A/B；证据与现场 RECV 行见 `dungeon_entry_packets.go` 的 `readDungeonEntrySelectionMode` 块。
func (s *Service) buildCurrentDungeonEntryPackets(
	runtime *runtimeDungeonState,
	scene worldmap.DungeonRoomScene,
) (currentDungeonEntryPackets, error) {
	hellMode := currentDungeonEntryStrictValidation
	if s.hellBlockedSlotsGate().deliversBlockedSlots() && runtime != nil &&
		hellPartySelectRequested(runtime.Request) {
		hellMode = currentDungeonEntryAllowHellOption
	}
	return buildCurrentDungeonEntryPacketsForModes(
		runtime, scene, hellMode, s.dungeonEntrySelectionModeGate())
}

// dungeonEntrySelectionModeGate 返回当前 Service 的归一化档位
// （零值 options = strict = 今天逐字节）。
func (s *Service) dungeonEntrySelectionModeGate() currentDungeonEntrySelectionModeMode {
	if s == nil {
		return currentDungeonEntrySelectionModeStrict
	}
	return s.options.dungeonEntrySelectionMode
}
