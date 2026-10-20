package dnfbridge

import (
	"context"
	"fmt"
	"strconv"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func equipmentDisjointGameplayModule() gameplayModuleDefinition {
	opcode := uint16(dnfenum.CmdPacketDisjointItem)
	return gameplayModuleDefinition{
		Name: "equipment-disjoint",
		LegacyHandlers: map[uint16]gameplayHandler{
			opcode: func(service *Service, session *gameSession, request gameplayRequest) error {
				return service.handleCurrentDisjointItem(session, request.Body)
			},
		},
		UpperHandlers: map[uint16]gameplayHandler{
			opcode: func(service *Service, session *gameSession, request gameplayRequest) error {
				if !requireDefaultGameplayClassification(service, session, request, "game-disjoint-item-blocked", "current_exe_op26_command_class_mismatch") {
					return nil
				}
				return service.handleCurrentDisjointItem(session, request.Body)
			},
		},
		LegacyNormalizers: map[uint16]gameplayLegacyNormalizer{
			opcode: func(body []byte) []byte {
				if len(body) == currentDisjointItemRequestSize+4 || len(body) == currentDisjointItemRequestSize+8 {
					return append([]byte(nil), body[:len(body)-4]...)
				}
				return body
			},
		},
	}
}

func (s *Service) handleCurrentDisjointItem(session *gameSession, body []byte) error {
	// 批量分解（"阿拉德分解修理机"多选分解）在 op26 上是**另一条请求体形状**：9 字节
	// 共享前缀 + 4 字节固定标签 42 32 36 01 + u8 件数 + 件数×u16 槽位（契约证据见
	// disjoint_compound_protocol.go）。旧实现只认 18 字节、并把尾部当"72 位掩码"，
	// 于是 18/20/22/36/38/40/46/48/52/54/62 字节的批量请求全被当成畸形单件体回 19，
	// 玩家看到的就是"批量分解报错"。
	if currentDisjointBodyIsBatchShape(body) {
		return s.handleCurrentDisjointBatchRequest(session, body)
	}
	request, err := parseCurrentDisjointItemRequest(body)
	if err != nil {
		s.logGameEvent(session, "game-disjoint-item-rejected", "body_len", len(body), "reason", businessLogDiagnostic(err))
		return s.sendGameUpperFailure(session, uint16(dnfenum.CmdPacketDisjointItem), currentDisjointFailureCode(err))
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	catalog, err := s.currentPVFItemCatalog()
	if err == nil {
		err = s.commitCurrentDisjointItem(ctx, session, catalog, request)
	}
	if err != nil {
		s.logGameEvent(session, "game-disjoint-item-rejected", "slot", request.SourceSlot, "reason", businessLogDiagnostic(err))
		return s.sendGameUpperFailure(session, uint16(dnfenum.CmdPacketDisjointItem), currentDisjointFailureCode(err))
	}
	return nil
}

// currentEquipmentDisjointResolver 是装备分解的 PVF 产物解析（"必须是不带装扮标记的
// 装备"+ 既有的卖价/rarity 兜底都在 currentEquipmentDisjointRewards 里）。单件与批量
// 共用同一份，保证两条路径的合法性口径逐字一致。
func currentEquipmentDisjointResolver(config currentDisjointPVFConfig) func(dungeonDropItemDefinition, *dnfpvf.Document) ([]currentDisjointReward, error) {
	return func(def dungeonDropItemDefinition, document *dnfpvf.Document) ([]currentDisjointReward, error) {
		if def.Kind != dungeonDropItemEquipment || currentDisjointDocumentIsAvatar(document) {
			return nil, errCurrentDisjointSourceInvalid
		}
		return currentEquipmentDisjointRewards(config, document)
	}
}

// handleCurrentDisjointBatchRequest 处理批量分解（op26 紧凑槽位列表体）：
//
//   - 严格解码（长度等式 14+2n、固定标签、件数 1..72、槽位为正/非账号共享/不重复），
//     任何不合规回有界失败 19 且零消耗；
//   - 每一件都走与单件**完全相同**的来源校验（currentPlanDisjointItemSource）；
//   - 任一件不合法 ⇒ 整批拒绝、零消耗零发放（绝不部分成功），日志带 offending slot；
//   - 全部合法 ⇒ 单事务逐件消耗并产出（产物按落点合并堆叠），然后逐件回**已证实的
//     单件成功体**，再按 DNFBRIDGE_DISJOINT_BATCH_ACK_SHAPE 补一次"界面状态拉回权威"
//     的槽位下发（默认 op14 受影响槽位行；见下面的回包形状注释）。
func (s *Service) handleCurrentDisjointBatchRequest(session *gameSession, body []byte) error {
	opcode := uint16(dnfenum.CmdPacketDisjointItem)
	batch, err := decodeCurrentDisjointBatchRequest(body)
	if err != nil {
		s.logGameEvent(session, "game-disjoint-item-batch-rejected",
			"body_len", len(body), "slot", currentDisjointErrorSlot(err), "reason", businessLogDiagnostic(err))
		return s.sendGameUpperFailure(session, opcode, currentDisjointFailureCode(err))
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	catalog, err := s.currentPVFItemCatalog()
	var config currentDisjointPVFConfig
	if err == nil {
		config, err = loadCurrentDisjointPVFConfig(catalog.source)
	}
	var result currentDisjointBatchResult
	if err == nil {
		result, err = s.commitCurrentDisjointBatch(ctx, session, catalog, batch.Slots, currentEquipmentDisjointResolver(config))
	}
	if err != nil {
		s.logGameEvent(session, "game-disjoint-item-batch-rejected",
			"count", len(batch.Slots), "slots", batch.SlotListText(),
			"slot", currentDisjointErrorSlot(err), "tool_slot", batch.ToolSlot, "context", batch.Context,
			"reason", businessLogDiagnostic(err))
		return s.sendGameUpperFailure(session, opcode, currentDisjointFailureCode(err))
	}
	rewardRows, units := 0, uint32(0)
	for _, item := range result.Items {
		rewardRows += len(item.Rewards)
		for _, row := range item.Rewards {
			units += row.Granted
		}
	}
	// 回包形状由 DNFBRIDGE_DISJOINT_BATCH_ACK_SHAPE 决定（见
	// disjoint_compound_protocol.go 的 currentDisjointBatchAckShape 注释）。
	//
	// ① 默认档 single_aggregate_ack：**一条** ACK，行 = 整批产物按 (槽,物品) 合并后的
	//    本次获得总量 ⇒ 客户端只弹**一个**获得框（运维原话：「分解多样物品会有很多次
	//    弹窗…需要统一在一个框」）。来源槽取本批第一件（请求里真实存在过的槽位）。
	// ② 回退档 per_item_single_ack[_refresh]：逐件复用已证实的单件成功体
	//    （`01 | i16 来源槽 | u8 list | i16 tool | u8 行数 | (u16 槽,u32 物品,u32 本次获得)`），
	//    顺序 = 请求槽位顺序，即 2026-09-25..29 的线上形状（逐字节回退用）。
	// ③ 界面状态一律仍由**仓库权威投影**的槽位下发给定：
	//   - 默认/回退刷新档：对受影响槽位补 op14 行（来源槽已从仓储删除 ⇒ 空行
	//     0xFFFFFFFF；产物槽 ⇒ 该槽的权威最终堆叠量），走 sendSelectedIncrementalItemSlotRefreshes
	//     ——完成券消耗、称号簿放入、邮件领取用的都是同一条已上线路径，且它按
	//     (list,slot) 去重、每行内容都从仓储最终状态重读（因此"产物又落回刚腾空的
	//     来源槽"这种重叠也只会得到产物行，不会把产物刷没）。
	//   - one_ack_container_refresh：主背包 list 0 整容器下发（op13），
	//     连"哪些槽是空的"都由整表重建，代价是比单行刷新重。
	// 顺序固定为 ACK 在前、刷新在后：刷新行是绝对量（set 语义），放后面才能覆盖
	// 任何客户端本地乐观累加。
	shape := currentDisjointBatchAckShape()
	ackCount := currentDisjointBatchAckShapeAckCount(len(result.Items), shape)
	if shape == currentDisjointBatchAckShapeSingleAggregate {
		ackBody := buildCurrentDisjointBatchAggregateSuccessBody(batch, result.Items)
		if err := s.sendGameUpperRawClass(session, opcode, ackBody, dnfproto.DefaultChannelClassification); err != nil {
			return err
		}
	} else {
		for index := 0; index < ackCount; index++ {
			item := result.Items[index]
			ackBody := buildCurrentDisjointItemSuccessBody(currentDisjointItemRequest{
				SourceSlot: item.Slot,
				ListType:   dnfrepo.MainInventoryListType,
				ToolSlot:   batch.ToolSlot,
			}, item.Rewards)
			if err := s.sendGameUpperRawClass(session, opcode, ackBody, dnfproto.DefaultChannelClassification); err != nil {
				return err
			}
		}
	}
	refreshRows, refreshLists := 0, ""
	refreshShape := currentDisjointBatchRefreshShape()
	if currentDisjointBatchAckShapeUsesSlotRefresh(shape) {
		refreshes := currentDisjointBatchSlotRefreshes(result)
		if !currentDisjointBatchRefreshShapeUsesRewardSlots(refreshShape) {
			// 默认档：产物槽不进 op14 —— 产物由上面那条 ACK 的"本次获得"行承载
			// （与单件路径同构，见 disjoint_compound_protocol.go 的开关注释）。
			refreshes = currentDisjointBatchSourceSlotRefreshes(result)
		}
		if err := s.sendSelectedIncrementalItemSlotRefreshes(session, "game-disjoint-item-batch_after_ack", refreshes); err != nil {
			return err
		}
		refreshRows = len(refreshes)
		refreshLists = strconv.Itoa(int(dnfrepo.MainInventoryListType))
	}
	if currentDisjointBatchAckShapeUsesContainerRefresh(shape) {
		entryCount, err := s.sendCurrentDisjointBatchContainerRefresh(session)
		if err != nil {
			return err
		}
		refreshRows = entryCount
		refreshLists = strconv.Itoa(int(dnfrepo.MainInventoryListType))
	}
	s.logGameEvent(session, "game-disjoint-item-batch-committed",
		"count", len(result.Items), "slots", batch.SlotListText(),
		"reward_rows", rewardRows, "units", units,
		"tool_slot", batch.ToolSlot, "context", batch.Context,
		"ack_shape", shape, "ack_count", ackCount,
		"refresh_shape", refreshShape,
		"refresh_rows", refreshRows, "refresh_lists", refreshLists)
	return nil
}

// currentDisjointBatchSlotRefreshes 把一次批量分解里"内容变了"的槽位翻成 op14 刷新行清单，
// 并按 (list,slot) 去重（返回的就是最终下发到线上的行清单，日志 refresh_rows 直接取 len）。
//
// 产物槽排在来源槽前面：某一格既可能是"刚被分解掉的来源槽"、又可能是"产物落回来的槽"
// （同一事务里 PlacePickupItem 可以把 [material] 放进刚腾空的那一格），去重保留首次出现，
// 而每一行的内容都由 sendSelectedIncrementalItemSlotRefreshes 从仓储的**最终**状态重读，
// 所以这种重叠拿到的是产物行（权威量），不会把产物刷成空行。
func currentDisjointBatchSlotRefreshes(result currentDisjointBatchResult) []alignedcmd.ItemSlotRefresh {
	total := 0
	for _, item := range result.Items {
		total += len(item.Rewards) + 1
	}
	refreshes := make([]alignedcmd.ItemSlotRefresh, 0, total)
	seen := make(map[alignedcmd.ItemSlotRefresh]struct{}, total)
	appendRefresh := func(slot int16) {
		refresh := alignedcmd.ItemSlotRefresh{ListType: dnfrepo.MainInventoryListType, SlotIndex: slot}
		if _, duplicate := seen[refresh]; duplicate {
			return
		}
		seen[refresh] = struct{}{}
		refreshes = append(refreshes, refresh)
	}
	for _, item := range result.Items {
		for _, reward := range item.Rewards {
			appendRefresh(reward.Slot)
		}
	}
	for _, item := range result.Items {
		appendRefresh(item.Slot)
	}
	return refreshes
}

// currentDisjointBatchSourceSlotRefreshes 是**默认档**（DNFBRIDGE_DISJOINT_BATCH_REFRESH_SHAPE
// = source_only）的行清单：只覆盖本批**被消耗掉的来源槽**，一条产物槽行都不发。
//
// 为什么产物槽可以不下发：产物已经在同一回包的 op26 ACK 里以"本次获得"的数字给过
// 客户端了 —— 这正是**单件分解**的工作方式（`commitCurrentDisjointItem` 只发 ACK、
// 不发任何 op14，运维认定它是正确的），也是"客户端会把 ACK 的产物行加进背包/晶块仓"
// 的实证。把产物槽再放进 op14，只是让同一件道具在同一批回包里出现两次
// （ACK 的 `granted` + op14 的绝对量），这正是运维报的"一次又一次叠加"。
//
// 来源槽必须留：上一轮现场实验证明"不补刷新的逐件 ACK 客户端一件都不采纳"，
// 界面上的来源装备不会消失（见 disjoint_compound_protocol.go 顶部契约与 (j) 用例）。
//
// 行内容仍由 sendSelectedIncrementalItemSlotRefreshes 从仓储**最终**状态重读：
// 某一格既被消耗又被产物重新占用时，拿到的是产物行（权威量），不会被刷成空行 ——
// 与 legacy 档同一条口径，本函数只决定"哪些槽进清单"。
func currentDisjointBatchSourceSlotRefreshes(result currentDisjointBatchResult) []alignedcmd.ItemSlotRefresh {
	refreshes := make([]alignedcmd.ItemSlotRefresh, 0, len(result.Items))
	seen := make(map[alignedcmd.ItemSlotRefresh]struct{}, len(result.Items))
	for _, item := range result.Items {
		refresh := alignedcmd.ItemSlotRefresh{ListType: dnfrepo.MainInventoryListType, SlotIndex: item.Slot}
		if _, duplicate := seen[refresh]; duplicate {
			continue
		}
		seen[refresh] = struct{}{}
		refreshes = append(refreshes, refresh)
	}
	return refreshes
}

// sendCurrentDisjointBatchContainerRefresh 是兜底档：主背包 list 0 整容器下发（op13）。
// 用的是登录引导与"list 7 延迟整容器下发"同一条包语法（buildCurrentItemListBody +
// sendCurrentSceneItemList），行数取自仓储权威快照 ⇒ 被分解掉的槽不再出现在行里。
func (s *Service) sendCurrentDisjointBatchContainerRefresh(session *gameSession) (int, error) {
	if session == nil || session.selectedCharacterID == 0 {
		return 0, fmt.Errorf("disjoint batch container refresh requires a selected character")
	}
	ctx, cancel := context.WithTimeout(context.Background(), currentRepositorySnapshotTimeout)
	defer cancel()
	body, entrySource, entryCount, ok := s.buildCurrentItemListBodyForSession(ctx, session, dnfrepo.MainInventoryListType)
	if !ok {
		return 0, fmt.Errorf("disjoint batch container refresh could not build list %d", dnfrepo.MainInventoryListType)
	}
	s.logPacketEvent("game-disjoint-item-batch-container-refresh-send",
		"conn_id", session.connID,
		"char_id", session.selectedCharacterID,
		"msg_id", uint16(dnfenum.CmdPacketLeaveParty),
		"list_type", dnfrepo.MainInventoryListType,
		"entry_count", entryCount,
		"body_len", len(body),
		"body_source", entrySource,
		"entry_size", currentItemListEntrySizeForType(dnfrepo.MainInventoryListType))
	return entryCount, s.sendCurrentSceneItemList(session, uint16(dnfenum.CmdPacketLeaveParty), body)
}

func (s *Service) commitCurrentDisjointItem(ctx context.Context, session *gameSession, catalog *pvfDungeonDropCatalog, request currentDisjointItemRequest) error {
	if s == nil || session == nil || catalog == nil {
		return errCurrentDisjointUnavailable
	}
	config, err := loadCurrentDisjointPVFConfig(catalog.source)
	if err != nil {
		return err
	}
	result, err := s.commitCurrentDisjoint(ctx, session, catalog, request.SourceSlot, 0, currentEquipmentDisjointResolver(config))
	if err != nil {
		return err
	}
	if err := s.sendGameUpperRawClass(session, uint16(dnfenum.CmdPacketDisjointItem), buildCurrentDisjointItemSuccessBody(request, result.Rewards), dnfproto.DefaultChannelClassification); err != nil {
		return err
	}
	s.logGameEvent(session, "game-disjoint-item-committed", "slot", request.SourceSlot, "tool_slot", request.ToolSlot, "reward_rows", len(result.Rewards), "popup", "current_exe_op26")
	return nil
}
