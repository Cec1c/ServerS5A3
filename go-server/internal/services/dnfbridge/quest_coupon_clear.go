package dnfbridge

import (
	"context"
	"encoding/binary"
	"fmt"
	"strconv"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfinventory "longheng.io/server/internal/modules/dnf/inventory"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfquest "longheng.io/server/internal/modules/dnf/quest"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// The matching EXE maps [gm quest clear] to 169 and [lv clear] to 179.
// Their item callbacks reach the common op515 sender (RVA 0x20edd89), which
// writes the action from the runtime item. Historical 167 captures and the
// former remaining-count ACK belong to the previous protocol assumption.
// See Docs/GUILD_POINTS.md and the op515 compatibility evidence.
const (
	currentQuestClearCouponActionIndex uint32 = 169
	currentQuestLevelClearActionIndex  uint32 = 179
	currentQuestClearCouponBodySize           = 19
	currentQuestClearCouponAckSize            = 8
	// currentQuestClearCouponDebugCommand 是客户端在同一动作里补发的 op176 文本前缀。
	currentQuestClearCouponDebugCommand = "//lvclear"

	// currentQuestClearCouponActionGmQuestClear 是 10164630 的 [action type]。
	currentQuestClearCouponActionGmQuestClear = "[gm quest clear]"
	// currentQuestClearCouponActionLevelClear 是 10301410 的 [action type]。
	// 真档里只有这一个成员且声明的是 90，而主线任务的真档最高 `[level]` 是 89
	// （quest.CouponMainQuestClearMaxLevel），所以两者选中的集合逐条相同。
	currentQuestClearCouponActionLevelClear = "[lv clear]"
)

type currentQuestClearCouponRequest struct {
	SourceSlotIndex int16
	ListType        byte
	ActionIndex     uint32
}

// decodeCurrentQuestClearCouponRequest 只在"这是完成券那条 op515"时返回 ok=true。
// 其它形状（含 action 164 的 damage-font 请求）一律 ok=false，交给既有路由。
func decodeCurrentQuestClearCouponRequest(body []byte) (currentQuestClearCouponRequest, bool) {
	if len(body) != currentQuestClearCouponBodySize {
		return currentQuestClearCouponRequest{}, false
	}
	request := currentQuestClearCouponRequest{
		SourceSlotIndex: int16(binary.LittleEndian.Uint16(body[0:2])),
		ListType:        body[2],
		ActionIndex:     binary.LittleEndian.Uint32(body[7:11]),
	}
	if request.ActionIndex != currentQuestClearCouponActionIndex && request.ActionIndex != currentQuestLevelClearActionIndex {
		return currentQuestClearCouponRequest{}, false
	}
	if request.SourceSlotIndex < 0 || request.ListType != dnfrepo.MainInventoryListType || binary.LittleEndian.Uint32(body[3:7]) != 0 || binary.LittleEndian.Uint32(body[11:15]) != 0 || binary.LittleEndian.Uint32(body[15:19]) != 0 {
		return request, false
	}
	return request, true
}

func buildCurrentQuestClearCouponAck(slot int16, action uint32) []byte {
	return dnfinventory.BuildStackableActionAck(slot, dnfrepo.MainInventoryListType, action)
}

func (s *Service) logQuestClearCouponEvent(session *gameSession, name string, request currentQuestClearCouponRequest, extra ...any) {
	fields := []any{
		"conn_id", sessionConnID(session),
		"character_id", sessionSelectedCharacterID(session),
		"msg_id", uint16(dnfenum.CmdPacketUseStackableAction),
		"source_slot", request.SourceSlotIndex,
		"list_type", request.ListType,
		"action_index", request.ActionIndex,
	}
	fields = append(fields, extra...)
	s.logGameEvent(session, name, fields...)
}

// handleCurrentQuestClearCouponUse 处理 op515 action 169/179（任务完成券右键使用）。
//
// 返回 (false, nil) 表示"这不是完成券那条 op515"，调用方继续走原路由；
// 返回 (true, nil) 表示本函数已经接管（成功回包，或按 fail-closed 只记日志）。
//
// fail-closed 的顺序保证：PVF 目录、仓储、角色、背包格、道具族、任务目录
// 全部校验通过之前不写任何字节；任务行落库成功之后才消耗道具——即使消耗这一步
// 失败，玩家也只是"任务完成了但券还在"，不会出现"券没了任务没完成"。
func (s *Service) handleCurrentQuestClearCouponUse(session *gameSession, body []byte) (bool, error) {
	request, matched := decodeCurrentQuestClearCouponRequest(body)
	if !matched {
		return false, nil
	}
	if session == nil || session.selectedCharacterID == 0 {
		s.logQuestClearCouponEvent(session, "game-quest-clear-coupon-blocked", request,
			"reason", "selected_character_missing",
			"quest_mutation", "none",
			"item_mutation", "none")
		return true, nil
	}
	repositories, ok := s.repositoryGroup()
	if !ok || repositories.Character == nil || repositories.Inventory == nil ||
		repositories.Quest == nil || repositories.CharacterSettlement == nil ||
		repositories.CharacterItems == nil {
		s.logQuestClearCouponEvent(session, "game-quest-clear-coupon-blocked", request,
			"reason", "repository_group_unavailable",
			"quest_mutation", "none",
			"item_mutation", "none")
		return true, nil
	}
	catalog, err := s.currentPVFItemCatalog()
	if err != nil || catalog == nil {
		s.logQuestClearCouponEvent(session, "game-quest-clear-coupon-blocked", request,
			"reason", "runtime_pvf_item_catalog_unavailable",
			"error", err,
			"quest_mutation", "none",
			"item_mutation", "none")
		return true, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()

	characterID := strconv.FormatUint(uint64(session.selectedCharacterID), 10)
	character, found, err := repositories.Character.Load(ctx, characterID)
	if err != nil {
		return true, err
	}
	if !found {
		s.logQuestClearCouponEvent(session, "game-quest-clear-coupon-blocked", request,
			"reason", "selected_character_not_found",
			"quest_mutation", "none",
			"item_mutation", "none")
		return true, nil
	}
	job, validJob := characterJobByte(character)
	if !validJob {
		s.logQuestClearCouponEvent(session, "game-quest-clear-coupon-blocked", request,
			"job", character.Job,
			"reason", "selected_character_job_invalid",
			"quest_mutation", "none",
			"item_mutation", "none")
		return true, nil
	}

	slotKey := currentCeraShopInventorySlotKey(dnfrepo.MainInventoryListType, request.SourceSlotIndex)
	inventory, found, err := repositories.Inventory.Load(ctx, characterID)
	if err != nil {
		return true, err
	}
	if !found {
		s.logQuestClearCouponEvent(session, "game-quest-clear-coupon-blocked", request,
			"reason", "inventory_record_missing",
			"quest_mutation", "none",
			"item_mutation", "none")
		return true, nil
	}
	stack, stackFound := inventory.Slots[slotKey]
	if !stackFound || stack.Count <= 0 || stack.ItemID <= 0 {
		s.logQuestClearCouponEvent(session, "game-quest-clear-coupon-blocked", request,
			"slot_key", slotKey,
			"reason", "source_slot_item_missing",
			"quest_mutation", "none",
			"item_mutation", "none")
		return true, nil
	}
	definition, err := catalog.ResolveItem(uint32(stack.ItemID))
	if err != nil {
		s.logQuestClearCouponEvent(session, "game-quest-clear-coupon-blocked", request,
			"slot_key", slotKey,
			"item_id", stack.ItemID,
			"reason", "source_item_not_resolvable_in_runtime_pvf",
			"error", err,
			"quest_mutation", "none",
			"item_mutation", "none")
		return true, nil
	}
	if definition.Kind != dungeonDropItemStackable ||
		(request.ActionIndex == currentQuestClearCouponActionIndex && !strings.EqualFold(definition.ActionType, currentQuestClearCouponActionGmQuestClear)) ||
		(request.ActionIndex == currentQuestLevelClearActionIndex && !strings.EqualFold(definition.ActionType, currentQuestClearCouponActionLevelClear)) {
		s.logQuestClearCouponEvent(session, "game-quest-clear-coupon-blocked", request,
			"slot_key", slotKey,
			"item_id", stack.ItemID,
			"item_kind", string(definition.Kind),
			"action_type", definition.ActionType,
			"pvf_path", definition.PVFPath,
			"reason", "source_item_is_not_a_level_based_quest_clear_coupon",
			"quest_mutation", "none",
			"item_mutation", "none")
		return true, nil
	}

	// 券的上限由券自己的 PVF 说明决定：「完成Lv1至Lv90的主线任务」+ `[minimum level] 90`
	// ⇒ cutoff = min(角色等级, 90)（dnfquest.CouponClearCutoff）。任务手册的
	// "任务完成"按钮不共用这个口径：它的确认框文案是客户端按"等级-1"算的，
	// 仍走 min(等级-1, 89)（dnfbridge.currentAutoCompleteMainQuestMaxLevel）。
	// 低等级角色即使绕过客户端的等级门禁拿到这张券，也只能清掉自己等级以下的
	// 主线任务，不会越级，因此这里不再单独复刻 [minimum level] 判定。
	cutoffLevel := dnfquest.CouponClearCutoff(character.Level)
	if cutoffLevel <= 0 {
		s.logQuestClearCouponEvent(session, "game-quest-clear-coupon-blocked", request,
			"level", character.Level,
			"reason", "character_level_below_coupon_cutoff_floor",
			"quest_mutation", "none",
			"item_mutation", "none")
		return true, nil
	}
	catalogQuest, err := s.loadQuestCatalog(ctx)
	if err != nil {
		s.logQuestClearCouponEvent(session, "game-quest-clear-coupon-blocked", request,
			"level", character.Level,
			"cutoff_level", cutoffLevel,
			"reason", "quest_catalog_unavailable",
			"error", err,
			"quest_mutation", "none",
			"item_mutation", "none")
		return true, nil
	}

	now := s.gameplayNow().UTC()
	eligibility := dnfquest.CharacterEligibility{
		Level:    character.Level,
		Job:      int(job),
		GrowType: characterGrowTypeInt(character),
	}
	questRecord, found, err := repositories.Quest.Load(ctx, characterID)
	if err != nil {
		return true, err
	}
	if !found {
		questRecord = dnfrepo.QuestRecord{CharacterID: characterID}
	}
	plan, err := catalogQuest.PlanCouponMainQuestClear(
		questRecord,
		eligibility,
		cutoffLevel,
		dnfquest.CouponClearScopeMainStorySweep,
		now,
	)
	if err != nil {
		s.logQuestClearCouponEvent(session, "game-quest-clear-coupon-blocked", request,
			"level", character.Level,
			"cutoff_level", cutoffLevel,
			"reason", "coupon_quest_clear_plan_rejected",
			"error", err,
			"quest_mutation", "none",
			"item_mutation", "none")
		return true, nil
	}

	if len(plan.ChangedFields) == 0 {
		// 幂等空操作：没有可完成的主线任务时不消耗这张券，但仍然回包，
		// 否则客户端会继续保持"点了没反应"的等待态。
		s.logQuestClearCouponEvent(session, "game-quest-clear-coupon-noop", request,
			"slot_key", slotKey,
			"item_id", stack.ItemID,
			"level", character.Level,
			"cutoff_level", cutoffLevel,
			"reason", "no_main_story_quest_at_or_below_cutoff_remains_incomplete",
			"quest_mutation", "none",
			"item_mutation", "none")
		// A success ACK makes the native client debit one item locally. An
		// unchanged inventory must use the full failure shape instead.
		return true, s.sendGameUpperRawClass(session, uint16(dnfenum.CmdPacketUseStackableAction), dnfinventory.BuildStackableActionFailure(request.SourceSlotIndex, request.ListType, byte(request.ActionIndex), 17), 1)
	}

	owner, err := dnfquest.NewOwner(repositories)
	if err != nil {
		return true, err
	}
	result, err := owner.ApplyCouponMainQuestClear(ctx, catalogQuest, dnfquest.CouponClearInput{
		CharacterID: characterID,
		Eligibility: eligibility,
		CutoffLevel: cutoffLevel,
		Scope:       dnfquest.CouponClearScopeMainStorySweep,
		ClearedAt:   now,
	})
	if err != nil {
		s.logQuestClearCouponEvent(session, "game-quest-clear-coupon-blocked", request,
			"level", character.Level,
			"cutoff_level", cutoffLevel,
			"reason", "coupon_quest_clear_commit_rejected",
			"error", err,
			"quest_mutation", "none",
			"item_mutation", "none")
		return true, nil
	}

	remaining, err := consumeCurrentQuestClearCoupon(ctx, repositories, characterID, slotKey, stack.ItemID)
	if err != nil {
		return true, err
	}
	s.logQuestClearCouponEvent(session, "game-quest-clear-coupon-committed", request,
		"slot_key", slotKey,
		"item_id", stack.ItemID,
		"pvf_path", definition.PVFPath,
		"action_type", definition.ActionType,
		"level", character.Level,
		"cutoff_level", cutoffLevel,
		"clear_scope", string(result.Scope),
		"include_sub_quests", true,
		"completed_quest_ids", int64ListText(result.ChangedQuestIDs),
		"completed_quest_count", len(result.ChangedQuestIDs),
		"completed_linked_sub_ids", int64ListText(result.ChangedLinkedSubIDs),
		"completed_linked_sub_count", len(result.ChangedLinkedSubIDs),
		"highest_completed_quest", result.HighestCompletedQuest,
		"remaining_coupon_count", remaining,
		"reward_granted", false,
		"body_source", "matched_exe_op515_action_ack")
	if err := s.sendCurrentQuestClearCouponAck(session, request, remaining, "committed"); err != nil {
		return true, err
	}
	if err := s.sendSelectedIncrementalItemSlotRefreshes(session, "quest_clear_coupon_after_ack", []alignedcmd.ItemSlotRefresh{{
		ListType:  dnfrepo.MainInventoryListType,
		SlotIndex: request.SourceSlotIndex,
	}}); err != nil {
		return true, err
	}
	if err := s.sendCurrentClearQuestListFromCommittedQuest(
		session,
		result.PostCommitQuest,
		"current_exe_op515_quest_clear_coupon_after_no_reward_main_quest_completion",
	); err != nil {
		return true, err
	}
	if err := s.sendCurrentActiveQuestSnapshotForSession(
		session,
		"current_exe_op515_quest_clear_coupon_after_completed_quest_bitmap",
	); err != nil {
		return true, err
	}
	return true, s.sendCurrentAcceptableQuestListOnlyForSession(
		session,
		"current_exe_op515_quest_clear_coupon_after_active_quest_snapshot",
	)
}

func (s *Service) sendCurrentQuestClearCouponAck(session *gameSession, request currentQuestClearCouponRequest, remaining int64, source string) error {
	body := buildCurrentQuestClearCouponAck(request.SourceSlotIndex, request.ActionIndex)
	s.logQuestClearCouponEvent(session, "game-quest-clear-coupon-ack-send", request,
		"ack_source", source,
		"remaining_coupon_count", remaining,
		"classification", dnfproto.DefaultChannelClassification,
		"body_len", len(body))
	return s.sendGameUpperRawClass(
		session,
		uint16(dnfenum.CmdPacketUseStackableAction),
		body,
		dnfproto.DefaultChannelClassification,
	)
}

// consumeCurrentQuestClearCoupon 在角色背包事务里扣掉 1 张券并回读校验。
// 复用 dnfrepo.CharacterItems.WithinCharacterItems，与瞬移药水消耗同一条路径。
func consumeCurrentQuestClearCoupon(
	ctx context.Context,
	repositories dnfrepo.Group,
	characterID string,
	slotKey string,
	itemID int64,
) (int64, error) {
	var remaining int64
	err := repositories.CharacterItems.WithinCharacterItems(ctx, characterID, func(inventory dnfrepo.InventoryRepository, _ dnfrepo.EquipmentRepository) error {
		record, found, err := inventory.Load(ctx, characterID)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("quest clear coupon inventory record missing: %s", characterID)
		}
		record = dnfrepo.CloneInventory(record)
		stack, ok := record.Slots[slotKey]
		if !ok || stack.Count <= 0 || stack.ItemID != itemID {
			return fmt.Errorf("quest clear coupon slot %s is not item %d", slotKey, itemID)
		}
		stack.Count--
		remaining = stack.Count
		if stack.Count <= 0 {
			delete(record.Slots, slotKey)
		} else {
			entry := currentItemListEntryFromStack(dnfrepo.MainInventoryListType, mustParseSlotIndex(slotKey), stack)
			stack.RawEntry = append([]byte(nil), entry.data[:]...)
			record.Slots[slotKey] = stack
		}
		record.UpdatedAt = time.Now().UTC()
		return dnfrepo.SaveInventoryFields(ctx, inventory, record, dnfrepo.InventoryFieldSlots)
	})
	if err != nil {
		return 0, err
	}
	return remaining, nil
}

// handleCurrentQuestClearCouponDebugCommand 只**观察** op176 里的完成券调试命令行。
//
// 客户端右键完成券时会先补发 `//lvclear <N>`。这条 opcode 是通用的 GM 调试命令行
// 通道：任何一个改过的客户端都能直接发它。把它当成"清任务"的授权会变成一个无需
// 消耗道具就能清主线任务的后门，所以这里只解析并记日志，不执行任何写操作——
// 真正的授权来自 op515 action 169/179 + 运行时 PVF 的券族判定 + 背包里的实物。
func (s *Service) handleCurrentQuestClearCouponDebugCommand(session *gameSession, body []byte) (bool, error) {
	command, ok := decodeCurrentQuestClearCouponDebugCommand(body)
	if !ok {
		return false, nil
	}
	fields := []any{
		"conn_id", sessionConnID(session),
		"character_id", sessionSelectedCharacterID(session),
		"msg_id", uint16(dnfenum.CmdPacketGmdebugCommand),
		"command", command,
		"quest_mutation", "none",
		"item_mutation", "none",
		"note", "op176 is a generic client-authored debug console; the coupon authority is op515 action 169/179 plus the runtime-PVF coupon family and the physical item",
	}
	if level, matched := parseCurrentQuestClearCouponDebugCommand(command); matched {
		fields = append(fields,
			"command_family", currentQuestClearCouponDebugCommand,
			"requested_level", level)
	}
	s.logGameEvent(session, "game-quest-clear-coupon-debug-command-observed", fields...)
	return true, nil
}

// decodeCurrentQuestClearCouponDebugCommand 解析 op176 体：u32 长度 + ASCII 文本。
func decodeCurrentQuestClearCouponDebugCommand(body []byte) (string, bool) {
	if len(body) < 4 {
		return "", false
	}
	length := int(binary.LittleEndian.Uint32(body[0:4]))
	if length <= 0 || length != len(body)-4 {
		return "", false
	}
	command := strings.TrimSpace(string(body[4:]))
	if command == "" {
		return "", false
	}
	return command, true
}

// parseCurrentQuestClearCouponDebugCommand 识别 `//lvclear <N>` 并返回 N。
func parseCurrentQuestClearCouponDebugCommand(command string) (int, bool) {
	fields := strings.Fields(strings.ToLower(strings.TrimSpace(command)))
	if len(fields) != 2 || fields[0] != currentQuestClearCouponDebugCommand {
		return 0, false
	}
	level, err := strconv.Atoi(fields[1])
	if err != nil || level <= 0 {
		return 0, false
	}
	return level, true
}

func mustParseSlotIndex(slotKey string) int16 {
	_, slot, ok := parseSceneInventorySlotKey(slotKey)
	if !ok {
		return 0
	}
	return slot
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
