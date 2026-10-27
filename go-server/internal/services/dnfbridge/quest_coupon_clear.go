package dnfbridge

import (
	"context"
	"encoding/binary"
	"fmt"
	"strconv"
	"strings"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/jobmap"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfquest "longheng.io/server/internal/modules/dnf/quest"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// Native op515: request = i16 slot, u8 list, u32 zero, u32 action, 2*u32 zero.
// Matched client SHA256 eeb8de2282001dc012eaf5b6f3df5a920539ab93cc3d07a626d9b478a301c9dd:
// PVF action table maps first awakening=118, gm quest clear=169, lv clear=179.
// RVA 0x20edc40 sends first awakening 118; RVA 0x20f3580 reads success
// [1, slot:u16, list:u8, action:u32], failure [0, error:u8, slot:u16, list:u8, action:u8].
// A success causes the client to consume one, so a no-op must use failure.
const (
	currentQuestClearCouponActionIndex        uint32 = 169
	currentQuestLevelClearActionIndex         uint32 = 179
	currentFirstAwakeningCouponActionIndex    uint32 = 118
	currentSecondAwakeningCouponActionIndex   uint32 = 121
	currentQuestClearCouponBodySize                  = 19
	currentQuestClearCouponAckSize                   = 10 // Legacy fatigue helper below; not the quest reply.
	currentQuestClearCouponDebugCommand              = "//lvclear"
	currentQuestClearCouponActionGmQuestClear        = "[gm quest clear]"
	currentQuestClearCouponActionLevelClear          = "[lv clear]"
)

type currentQuestClearCouponRequest struct {
	SourceSlotIndex int16
	ListType        byte
	ActionIndex     uint32
	ReservedValid   bool
}

func decodeCurrentQuestClearCouponRequest(body []byte) (currentQuestClearCouponRequest, bool) {
	if len(body) != currentQuestClearCouponBodySize {
		return currentQuestClearCouponRequest{}, false
	}
	r := currentQuestClearCouponRequest{SourceSlotIndex: int16(binary.LittleEndian.Uint16(body[:2])), ListType: body[2], ActionIndex: binary.LittleEndian.Uint32(body[7:11])}
	if r.ActionIndex != currentQuestClearCouponActionIndex && r.ActionIndex != currentFirstAwakeningCouponActionIndex && r.ActionIndex != currentQuestLevelClearActionIndex && r.ActionIndex != currentSecondAwakeningCouponActionIndex {
		return currentQuestClearCouponRequest{}, false
	}
	r.ReservedValid = binary.LittleEndian.Uint32(body[3:7]) == 0 && binary.LittleEndian.Uint32(body[11:15]) == 0 && binary.LittleEndian.Uint32(body[15:19]) == 0
	return r, true
}

func buildCurrentQuestCouponResult(r currentQuestClearCouponRequest, success bool) []byte {
	var w packetWriter
	if success {
		w.writeByte(1)
	} else {
		w.writeByte(0)
		w.writeByte(19)
	}
	w.writeUint16(uint16(r.SourceSlotIndex))
	w.writeByte(r.ListType)
	if success {
		w.writeUint32(r.ActionIndex)
	} else {
		w.writeByte(byte(r.ActionIndex))
	}
	return w.bytes()
}

// Kept for the existing fatigue potion path; quest tickets use the native
// action-bearing result above.
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

func (s *Service) logQuestClearCouponEvent(session *gameSession, name string, r currentQuestClearCouponRequest, extra ...any) {
	fields := []any{"conn_id", sessionConnID(session), "character_id", sessionSelectedCharacterID(session), "source_slot", r.SourceSlotIndex, "list_type", r.ListType, "action_index", r.ActionIndex}
	s.logGameEvent(session, name, append(fields, extra...)...)
}

func resolveCurrentQuestCoupon(catalog *pvfDungeonDropCatalog, itemID int64, action uint32) (dnfquest.ClearCoupon, error) {
	coupon := dnfquest.ClearCoupon{ItemID: itemID}
	if catalog == nil || catalog.source == nil || itemID <= 0 || itemID > 0xffffffff {
		return coupon, dnfquest.ErrCouponClearUnavailable
	}
	def, err := catalog.ResolveItem(uint32(itemID))
	if err != nil {
		return coupon, err
	}
	if def.Kind != dungeonDropItemStackable {
		return coupon, dnfquest.ErrCouponClearUnavailable
	}
	switch strings.ToLower(strings.TrimSpace(def.ActionType)) {
	case currentQuestClearCouponActionGmQuestClear:
		if action != currentQuestClearCouponActionIndex {
			return coupon, dnfquest.ErrCouponClearUnavailable
		}
		coupon.Kind = dnfquest.CouponMainStory
		coupon.CutoffLevel = 90
	case currentQuestClearCouponActionLevelClear:
		if action != currentQuestLevelClearActionIndex {
			return coupon, dnfquest.ErrCouponClearUnavailable
		}
		coupon.Kind = dnfquest.CouponMainStory
		coupon.CutoffLevel = 90
	case "[second awaken clear]":
		if action != currentSecondAwakeningCouponActionIndex {
			return coupon, dnfquest.ErrCouponClearUnavailable
		}
		coupon.Kind = dnfquest.CouponSecondAwakening
	case "[first awaken clear]":
		if action != currentFirstAwakeningCouponActionIndex {
			return coupon, dnfquest.ErrCouponClearUnavailable
		}
		coupon.Kind = dnfquest.CouponFirstAwakening
	default:
		return coupon, dnfquest.ErrCouponClearUnavailable
	}
	doc, err := parseDungeonCardPVFDocument(catalog.source, def.PVFPath)
	if err != nil {
		return coupon, err
	}
	minLevel, _ := doc.Int("minimum level")
	maxLevel, _ := doc.Int("maximum level")
	coupon.MinimumLevel = int(minLevel)
	coupon.MaximumLevel = int(maxLevel)
	if strings.EqualFold(def.ActionType, currentQuestClearCouponActionLevelClear) {
		limits := doc.Ints("action type")
		if len(limits) != 1 || limits[0] <= 0 {
			return coupon, dnfquest.ErrCouponClearLevelInvalid
		}
		coupon.CutoffLevel = int(limits[0])
	}
	tags := doc.Texts("usable job")
	if len(tags) > 0 {
		all := false
		for _, tag := range tags {
			if strings.EqualFold(strings.TrimSpace(tag), "[all]") {
				all = true
			}
		}
		if !all {
			for job := 0; job <= jobmap.MaxID; job++ {
				for _, tag := range tags {
					if jobmap.MatchesQuestTag(tag, job) {
						coupon.Jobs = append(coupon.Jobs, job)
						break
					}
				}
			}
			if len(coupon.Jobs) == 0 {
				return coupon, dnfquest.ErrQuestNotAcceptable
			}
		}
	}
	if coupon.Kind == dnfquest.CouponSecondAwakening {
		return resolveSecondAwakeningCouponConditions(coupon, doc)
	}
	return coupon, nil
}

func (s *Service) handleCurrentQuestClearCouponUse(session *gameSession, body []byte) (bool, error) {
	r, matched := decodeCurrentQuestClearCouponRequest(body)
	if !matched {
		return false, nil
	}
	if r.ActionIndex == currentSecondAwakeningCouponActionIndex {
		return s.handleCurrentSecondAwakeningCoupon(session, r)
	}
	if session == nil || session.selectedCharacterID == 0 {
		return true, nil
	}
	reject := func(reason string, err error) (bool, error) {
		s.logQuestClearCouponEvent(session, "game-quest-clear-coupon-blocked", r, "reason", reason, "error", err)
		return true, s.sendGameUpperRawClass(session, uint16(dnfenum.CmdPacketUseStackableAction), buildCurrentQuestCouponResult(r, false), dnfproto.DefaultChannelClassification)
	}
	if !r.ReservedValid || r.ListType != dnfrepo.MainInventoryListType || r.SourceSlotIndex < 0 || sessionHasActiveDungeonRuntime(session) {
		return reject("request_invalid", nil)
	}
	repositories, ok := s.repositoryGroup()
	if !ok || repositories.CharacterSettlement == nil || repositories.Inventory == nil {
		return reject("repository_unavailable", nil)
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	id := strconv.Itoa(int(session.selectedCharacterID))
	inv, found, err := repositories.Inventory.Load(ctx, id)
	if err != nil || !found || inv.CharacterID != id {
		return reject("inventory_unavailable", err)
	}
	stack, found := inv.Slots[currentCeraShopInventorySlotKey(r.ListType, r.SourceSlotIndex)]
	if !found || stack.Count <= 0 {
		return reject("source_missing", nil)
	}
	catalog, err := s.currentPVFItemCatalog()
	if err != nil {
		return reject("pvf_unavailable", err)
	}
	coupon, err := resolveCurrentQuestCoupon(catalog, stack.ItemID, r.ActionIndex)
	if err != nil {
		return reject("coupon_invalid", err)
	}
	quests, err := s.loadQuestCatalog(ctx)
	if err != nil {
		return reject("quest_catalog_unavailable", err)
	}
	input := dnfquest.UseClearCouponInput{AccountID: s.accountIDForSession(session), CharacterID: id, Slot: r.SourceSlotIndex, Coupon: coupon, Now: s.gameplayNow()}
	if coupon.Kind == dnfquest.CouponFirstAwakening {
		input.Profiles, input.Skills, err = s.currentProfessionResources(ctx)
		if err != nil {
			return reject("profession_unavailable", err)
		}
	}
	owner, err := dnfquest.NewOwner(repositories)
	if err != nil {
		return reject("owner_unavailable", err)
	}
	result, err := owner.UseClearCoupon(ctx, quests, input)
	if err != nil {
		return reject("commit_rejected", err)
	}
	if !result.Changed {
		s.logQuestClearCouponEvent(session, "game-quest-clear-coupon-no-safe-work", r, "blocked_question_ids", int64ListText(result.BlockedQuestionIDs), "damaged_question_ids", int64ListText(result.DamagedQuestionIDs))
		return reject("nothing_to_complete", nil)
	}
	s.logQuestClearCouponEvent(session, "game-quest-clear-coupon-question-guards", r, "blocked_question_ids", int64ListText(result.BlockedQuestionIDs), "damaged_question_ids", int64ListText(result.DamagedQuestionIDs))
	s.logQuestClearCouponEvent(session, "game-quest-clear-coupon-committed", r, "item_id", stack.ItemID, "cutoff_level", result.CutoffLevel, "completed_quest_ids", int64ListText(result.ChangedQuestIDs), "awakened", result.Awakened, "remaining", result.Remaining, "reward_granted", false)
	if err := s.sendGameUpperRawClass(session, uint16(dnfenum.CmdPacketUseStackableAction), buildCurrentQuestCouponResult(r, true), dnfproto.DefaultChannelClassification); err != nil {
		return true, err
	}
	if err := s.sendSelectedIncrementalItemSlotRefreshes(session, "quest_coupon_committed", []alignedcmd.ItemSlotRefresh{{ListType: r.ListType, SlotIndex: r.SourceSlotIndex}}); err != nil {
		return true, err
	}
	if result.Awakened {
		if err := s.sendCurrentAwakeningCouponSnapshots(ctx, session, result); err != nil {
			return true, err
		}
	}
	return true, s.sendCommittedStorySweepSnapshots(session, result.Character, result.Quests,
		result.ChangedQuestIDs, "quest_coupon")
}

func (s *Service) sendCurrentAwakeningCouponSnapshots(ctx context.Context, session *gameSession, result dnfquest.UseClearCouponResult) error {
	// Ticket ACK does not have op34's profession actor rebuild. Refresh actual
	// town actor state through native mode0, then its points and skill layout.
	body := s.buildCurrentSceneObjectListBodyForSessionInContext(ctx, session, session.selectedCharacterID, result.Character.Name, result.Character, true, currentConnectionTownActorOwnerContext(session))
	if len(body) == 0 {
		return fmt.Errorf("awakening coupon actor projection unavailable")
	}
	if err := s.sendGameUpperRawClass(session, uint16(dnfenum.CmdPacketSetUDPIPPort), body, 0); err != nil {
		return err
	}
	state := buildCurrentFinishLoadingCharacterStateBody(result.Character, result.Skill.Points)
	if len(state) != currentFinishLoadingCharacterStateBodySize {
		return errCurrentFinishQuestAckShape
	}
	if err := s.sendGameUpperRawClass(session, currentDungeonCharacterStateMsgID, state, 0); err != nil {
		return err
	}
	repositories, _ := s.repositoryGroup()
	commands := s.loadSkillCommandProjection(ctx, repositories, result.Skill.CharacterID)
	skillBody, _, err := buildCurrentSceneSkillInfoBody(result.Skill, result.Skill.Layouts[currentSkillInfoTreeIndex], commands)
	if err != nil {
		return err
	}
	return s.sendGameUpperRawClass(session, currentSkillInfoMsgID, skillBody, 0)
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
