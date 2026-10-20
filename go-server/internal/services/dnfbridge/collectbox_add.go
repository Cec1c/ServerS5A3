package dnfbridge

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"strconv"
	"strings"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	dnfcollectbox "longheng.io/server/internal/modules/dnf/collectbox"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// 本文件是「把宝珠放进收集箱」的线协议 + 编排（客户端 op1022 =
// ENUM_CMDPACKET_ADD_COLLECTBOX_ITEM）。
//
// 现场症状：操作者口径「收集箱直接无法交互 无法放置宝珠」。香港 packet_log 全量历史
// 里 op1022 一条都没有（只有 4× op1021、2× op1024），因为服务端对 op1021 一个字节
// 都不回，客户端根本到不了"放宝珠"这一步。上一轮把 op1021 打开路径补上了，但
// op1022/1023/1024 仍然只观测不应答（logCurrentCollectboxObservedMutation），
// 所以**即使重新部署，宝珠依旧放不进去**。本文件补的就是这后半条。
//
// 请求体形状（唯一可依据的参考实现：86JP
// Server/DfoServer/Network/Handlers/CollectionBoxHandler.cs
// HandleInsertCollectBoxItem，要求 body.Length >= 8）：
//
//	[0:2] u16 LE box index
//	[2:4] u16 LE source inventory slot（list 0 = 主背包）
//	[4:8] u32 LE item id
//
// 应答形状（同参考实现 SendCommandAck(class1, header.type, resultCode, errorCode)）：
//
//	成功：class1 / op1022 body = `01`
//	失败：class1 / op1022 body = `00 <errorCode>`
//
// 90CN 客户端自己的 op1022 请求体尚无现场抓包（香港历史里没有），所以整条路
// 由 DNFBRIDGE_COLLECTBOX_MUTATION 开关兜底：默认 reference = 按参考实现办理；
// 回退值 observe_only = 恢复"只观测、不发明应答"的旧行为。无论哪个档，
// 请求体都会以 game-collectbox-mutation-observed 记全量 hex，供下一轮定形。
const (
	// collectboxMutationEnv 是「收集箱登记/取出/延期」总开关。
	// 默认 reference（按参考实现办理 op1022）；observe_only/off/0/false/no 一键回退
	// 到修复前行为（op1022/1023/1024 只记 game-collectbox-mutation-observed）。
	collectboxMutationEnv          = "DNFBRIDGE_COLLECTBOX_MUTATION"
	collectboxMutationReference    = "reference"
	collectboxMutationObserveOnly  = "observe_only"
	currentCollectboxAddBodySize   = 8
	currentCollectboxAddErrorCode  = byte(0x12)
	currentCollectboxAddSourceList = dnfrepo.MainInventoryListType
)

// currentCollectboxAddRequest 是 op1022 的请求读数。
type currentCollectboxAddRequest struct {
	BoxIndex   int32
	SourceSlot int16
	ItemID     int32
}

// parseCurrentCollectboxAddRequest 按参考实现口径解析 op1022 请求体。
//
// 只接受恰好 currentCollectboxAddBodySize 字节：参考实现读的是 u16/u16/u32 三个
// 定长字段，多出来的字节说明客户端版本不同，宁可拒绝也不猜偏移。
func parseCurrentCollectboxAddRequest(body []byte) (currentCollectboxAddRequest, bool) {
	if len(body) != currentCollectboxAddBodySize {
		return currentCollectboxAddRequest{}, false
	}
	request := currentCollectboxAddRequest{
		BoxIndex:   int32(binary.LittleEndian.Uint16(body[0:2])),
		SourceSlot: int16(binary.LittleEndian.Uint16(body[2:4])),
		ItemID:     int32(binary.LittleEndian.Uint32(body[4:8])),
	}
	if request.BoxIndex <= 0 || request.SourceSlot < 0 || request.ItemID <= 0 {
		return currentCollectboxAddRequest{}, false
	}
	return request, true
}

// buildCurrentCollectboxAddAckBody 生成成功应答 body（参考实现固定 `01`）。
func buildCurrentCollectboxAddAckBody() []byte {
	return []byte{currentCollectboxSelectAckOK}
}

// sendCurrentCollectboxAddAck 发 class1/op1022 的成/败应答。
func (s *Service) sendCurrentCollectboxAddAck(session *gameSession, result *dnfcollectbox.PutResult, code byte) error {
	if session == nil {
		return nil
	}
	if result != nil {
		body := buildCurrentCollectboxAddAckBody()
		s.logGameEvent(session, "game-collectbox-add-ack-send",
			"char_id", session.selectedCharacterID,
			"box_index", result.BoxIndex,
			"slot_index", result.SlotIndex,
			"item_id", result.ItemID,
			"body_hex", hex.EncodeToString(body))
		return s.sendGameUpperRawClass(session, currentCollectboxAddItemMsgID, body, 1)
	}
	body := []byte{0, code}
	s.logGameEvent(session, "game-collectbox-add-ack-send",
		"char_id", session.selectedCharacterID,
		"result", "failure",
		"error_code", code,
		"body_hex", hex.EncodeToString(body))
	return s.sendGameUpperRawClass(session, currentCollectboxAddItemMsgID, body, 1)
}

// currentCollectboxAddPolicy 把 PVF 箱子段落翻成 domain 策略。
func currentCollectboxAddPolicy(box currentCollectboxBox) dnfcollectbox.Policy {
	allowed := make([]int32, 0, len(box.Slots))
	for _, slot := range box.Slots {
		if slot.ItemID > 0 {
			allowed = append(allowed, slot.ItemID)
		}
	}
	return dnfcollectbox.Policy{
		BoxIndex:       box.Index,
		SlotCount:      box.SlotCount(),
		AllowedItemIDs: allowed,
		ExpiresAt:      box.BaseExpiration,
		HasExpiry:      box.HasBaseExpiration,
	}
}

// handleCurrentCollectboxAdd 处理客户端"把宝珠放进收集箱"（class1/op1022）。
//
// 成功后按参考实现顺序回三样东西：先把背包那一格刷新掉（宝珠离开背包），再刷新
// 该箱的 class0/op1027 状态（宝珠出现在箱子里），最后回 op1022 成功应答。
func (s *Service) handleCurrentCollectboxAdd(session *gameSession, body []byte) error {
	if !s.collectboxMutationEnabled() {
		return s.logCurrentCollectboxObservedMutation(session, gameplayRequest{
			Opcode: currentCollectboxAddItemMsgID,
			Body:   body,
		})
	}
	request, ok := parseCurrentCollectboxAddRequest(body)
	if !ok {
		s.logGameEvent(session, "game-collectbox-add-blocked",
			"opcode", currentCollectboxAddItemMsgID,
			"body_len", len(body),
			"body_hex", hex.EncodeToString(body),
			"reason", "current_client_collectbox_add_body_unparsed")
		return s.sendCurrentCollectboxAddAck(session, nil, currentCollectboxAddErrorCode)
	}
	s.logGameEvent(session, "game-collectbox-add-request",
		"char_id", session.selectedCharacterID,
		"body_len", len(body),
		"body_hex", hex.EncodeToString(body),
		"box_index", request.BoxIndex,
		"source_slot", request.SourceSlot,
		"item_id", request.ItemID)

	catalog, ok := s.currentCollectboxCatalog()
	if !ok || len(catalog.Boxes) == 0 {
		s.logGameEvent(session, "game-collectbox-add-blocked",
			"opcode", currentCollectboxAddItemMsgID,
			"box_index", request.BoxIndex,
			"reason", "collectbox_catalog_unavailable")
		return s.sendCurrentCollectboxAddAck(session, nil, currentCollectboxAddErrorCode)
	}
	box, ok := catalog.Box(request.BoxIndex)
	if !ok {
		s.logGameEvent(session, "game-collectbox-add-blocked",
			"opcode", currentCollectboxAddItemMsgID,
			"box_index", request.BoxIndex,
			"reason", "collectbox_box_not_in_catalog")
		return s.sendCurrentCollectboxAddAck(session, nil, currentCollectboxAddErrorCode)
	}
	repositories, ok := s.repositoryGroup()
	if !ok {
		s.logGameEvent(session, "game-collectbox-add-blocked",
			"opcode", currentCollectboxAddItemMsgID,
			"box_index", request.BoxIndex,
			"reason", "repositories_unavailable")
		return s.sendCurrentCollectboxAddAck(session, nil, currentCollectboxAddErrorCode)
	}
	owner, err := dnfcollectbox.NewOwner(repositories)
	if err != nil {
		s.logGameEvent(session, "game-collectbox-add-blocked",
			"opcode", currentCollectboxAddItemMsgID,
			"box_index", request.BoxIndex,
			"reason", "owner_unavailable",
			"error", err)
		return s.sendCurrentCollectboxAddAck(session, nil, currentCollectboxAddErrorCode)
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	result, err := owner.Put(ctx, dnfcollectbox.PutCommand{
		SelectedCharacterID: session.selectedCharacterID,
		SourceList:          currentCollectboxAddSourceList,
		SourceSlot:          request.SourceSlot,
		ItemID:              request.ItemID,
	}, currentCollectboxAddPolicy(box))
	if err != nil {
		s.logGameEvent(session, "game-collectbox-add-blocked",
			"opcode", currentCollectboxAddItemMsgID,
			"box_index", request.BoxIndex,
			"source_slot", request.SourceSlot,
			"item_id", request.ItemID,
			"reason", "domain_rejected",
			"error", err)
		return s.sendCurrentCollectboxAddAck(session, nil, currentCollectboxAddErrorCode)
	}

	// ① 背包那一格：宝珠必须先从背包消失，客户端才会跟着箱子走。
	if err := s.sendSelectedIncrementalItemSlotRefreshes(session, "collectbox_add", []alignedcmd.ItemSlotRefresh{
		{ListType: dnfrepo.MainInventoryListType, SlotIndex: result.SourceSlot},
	}); err != nil {
		return err
	}
	// ② 箱子状态：把登记结果按 class0/op1027 推回去（与初始化同一形状）。
	// 选中标志照实写：只有请求箱恰好就是本会话当前选中箱时才置 1，绝不为了让
	// 界面对上就谎报选中态（客户端自己知道在看哪个箱子）。
	if err := s.sendCurrentCollectboxStateWithItems(session, box,
		session.collectboxSelectedIndex == box.Index, "collectbox_add", result.RegisteredItemIDs); err != nil {
		return err
	}
	// ③ 命令应答：参考实现把 ack 放在最后。
	if err := s.sendCurrentCollectboxAddAck(session, &result, 0); err != nil {
		return err
	}
	s.logGameEvent(session, "game-collectbox-add-committed",
		"char_id", session.selectedCharacterID,
		"box_index", result.BoxIndex,
		"box_title", currentCollectboxBoxTitle(box),
		"slot_index", result.SlotIndex,
		"item_id", result.ItemID,
		"source_slot", result.SourceSlot,
		"source_remaining", result.SourceRemaining,
		"source_emptied", result.SourceEmptied,
		"target_key", result.TargetKey,
		"registered_count", len(s.currentCollectboxRegisteredItemIDs(session, box)),
		"state_source", "runtime_pvf_collectbox_policy_atomic_character_inventory")
	return nil
}

// currentCollectboxMutationEnabled 报告 op1022 是否按参考实现办理。
// 与 collectboxEnabled 一样，options 零值 = 修复前行为（只观测）。
func (s *Service) collectboxMutationEnabled() bool {
	return s != nil && s.collectboxEnabled() &&
		collectboxMutationValue(s.options.collectboxMutation) == collectboxMutationReference
}

func collectboxMutationValue(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case collectboxMutationReference, "on", "1", "true", "yes":
		return collectboxMutationReference
	default:
		return collectboxMutationObserveOnly
	}
}

// currentCollectboxRegisteredItemsForSession 一次加载该角色全部箱子的登记条目。
// 读失败只记一条事件并返回空表 —— 与其它目录读取一致，绝不让会话中断。
func (s *Service) currentCollectboxRegisteredItemsForSession(session *gameSession) map[int32][]int32 {
	if s == nil || session == nil || session.selectedCharacterID == 0 {
		return nil
	}
	repositories, ok := s.repositoryGroup()
	if !ok || repositories.Inventory == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), currentRepositorySnapshotTimeout)
	defer cancel()
	characterID := strconv.Itoa(int(session.selectedCharacterID))
	record, found, err := repositories.Inventory.Load(ctx, characterID)
	if err != nil || !found {
		s.logGameEvent(session, "game-collectbox-registry-load-failed",
			"char_id", session.selectedCharacterID,
			"found", found,
			"error", err)
		return nil
	}
	return dnfcollectbox.RegisteredItemIDsByBox(record)
}
