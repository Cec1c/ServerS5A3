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
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfquest "longheng.io/server/internal/modules/dnf/quest"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// 任务完成券（"Lv1~90主线任务完成券"家族）右键使用链路。
//
// # 现场证据（2026-09-25 香港封包日志 + 真档 PVF）
//
// 道具：item **10164630**，PVF `stackable/10164001/10164630.stk`
//
//	[name] Lv1~90主线任务完成券
//	[minimum level] 90   [stack limit] 1   [stackable type] `[etc]`
//	[action type] `[gm quest clear]`
//	[use action packet] 0
//
// （同族 `Lv1~90主线/成就任务完成券` item 10301410 =
// `stackable/10301001/10301410.stk`，`[action type] `[lv clear]` 90`。
// 全档 stackable 中带 `[gm quest clear]` 的只有 10164630，带 `[lv clear]` 的
// 只有 10301410。）
//
// 操作者右键这张券时，客户端在**同一毫秒**连发两条上行（
// `packet_log.20260925-034443.txt` line 427/429，操作者会话 game-000007）：
//
//	op176 ENUM_CMDPACKET_GMDEBUG_COMMAND body_len=16 hex=...000c0000002f2f6c76636c656172203930
//	   → u32(12) + ASCII `//lvclear 90`
//	op515 ENUM_CMDPACKET_USE_STACKABLE_ACTION body_len=19 hex=...57000000000000 00a700000000000000000000
//	   → i16 slot=87, u8 list=0, u32 action=**167**, reserved 全零
//
// 服务端此前对这两条都没有 handler：
//
//   - op176 落到 `handleGameCommand` 的 default 分支，只写一行
//     "unhandled latest game command"，一个字节都不回；
//   - op515 `DecodeUseStackableActionRequest` 只认 action 162（damage font），
//     167 直接判"unsupported"⇒ ResponseAllowed=false ⇒ bridge 也不回包。
//
// 客户端发完就在等结果，于是玩家看到的就是"右键使用后无反应"。
//
// # 回包形状
//
// op515 只有一个响应 opcode 且体内**不带 action index**，说明这个响应形状与
// action 无关；因此直接复用同包已验证的 damage-font（action 162）成功体
// （`inventory.buildUseStackableActionSuccessAck`）逐字节同形的 10 字节：
//
//	result=1, i16 slot, i16 0, i16 remaining, i16 0, u8 1
//
// 不新增、不臆造任何 opcode 或失败码：失败时保持现有行为（只记日志、不回包），
// 因为本 opcode 的失败路径在客户端不会像 op44 那样卡住界面（操作者连续点了三次
// 仍能继续操作其它界面）。
const (
	// currentQuestClearCouponActionIndex 是当前 EXE 为 `[gm quest clear]` 完成券
	// 发出的 op515 动作号。它由操作者右键那一刻的封包日志定死，不是推测值。
	currentQuestClearCouponActionIndex uint32 = 167
	currentQuestClearCouponBodySize           = 19
	currentQuestClearCouponAckSize            = 10
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
// 其它形状（含 action 162 的 damage-font 请求）一律 ok=false，交给既有路由。
func decodeCurrentQuestClearCouponRequest(body []byte) (currentQuestClearCouponRequest, bool) {
	if len(body) != currentQuestClearCouponBodySize {
		return currentQuestClearCouponRequest{}, false
	}
	request := currentQuestClearCouponRequest{
		SourceSlotIndex: int16(binary.LittleEndian.Uint16(body[0:2])),
		ListType:        body[2],
		ActionIndex:     binary.LittleEndian.Uint32(body[7:11]),
	}
	if request.ActionIndex != currentQuestClearCouponActionIndex {
		return currentQuestClearCouponRequest{}, false
	}
	return request, true
}

func currentQuestClearCouponActionSupported(actionType string) bool {
	switch strings.ToLower(strings.TrimSpace(actionType)) {
	case currentQuestClearCouponActionGmQuestClear, currentQuestClearCouponActionLevelClear:
		return true
	default:
		return false
	}
}

// buildCurrentQuestClearCouponAck 生成 op515 成功回包体。
// 与同包已证实的 damage-font 成功体逐字节同形（见上面的注释）。
func buildCurrentQuestClearCouponAck(slot int16, remaining int64) []byte {
	if remaining < 0 {
		remaining = 0
	}
	var writer packetWriter
	writer.writeByte(1)
	writer.writeUint16(uint16(slot))
	writer.writeUint16(0)
	writer.writeUint16(uint16(remaining))
	writer.writeUint16(0)
	writer.writeByte(1)
	return writer.bytes()
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

// handleCurrentQuestClearCouponUse 处理 op515 action 167（任务完成券右键使用）。
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
	if definition.Kind != dungeonDropItemStackable || !currentQuestClearCouponActionSupported(definition.ActionType) {
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
		return true, s.sendCurrentQuestClearCouponAck(session, request, stack.Count, "noop")
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
		"body_source", "current_exe_op515_action_167_with_damage_font_verified_ack_shape")
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
	body := buildCurrentQuestClearCouponAck(request.SourceSlotIndex, remaining)
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
// 真正的授权来自 op515 action 167 + 运行时 PVF 的券族判定 + 背包里的实物。
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
		"note", "op176 is a generic client-authored debug console; the coupon authority is op515 action 167 plus the runtime-PVF coupon family and the physical item",
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
