package dnfbridge

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func (s *Service) sendSelectedCurrentItemLists(session *gameSession, source string) error {
	return s.sendSelectedCurrentItemListsWithRefresh(session, source, false)
}

func (s *Service) sendSelectedCurrentItemListsRefresh(session *gameSession, source string) error {
	return s.sendSelectedCurrentItemListsWithRefresh(session, source, true)
}

func (s *Service) sendSelectedCurrentItemListsWithRefresh(session *gameSession, source string, refresh bool) error {
	s.cleanupSelectedExpiredRentalEquipment(session, source)
	if err := s.sendSelectedCurrentContainerListsWithRefresh(session, source, refresh); err != nil {
		return err
	}
	// Full mode1/mode3 creates the equipped objects. The working Python
	// sequence does not append a generic equipment refresh. Guardian-gem raw
	// socket words are the one proved exception: they are not contained by the
	// mode1/mode3 create rows and must be replayed through the accepted list-3
	// op14 carrier after the character containers are established.
	if err := s.sendSelectedRentalWalletStateWithRefresh(session, source, refresh); err != nil {
		return err
	}
	if err := s.sendSelectedWornTitleEnchantRowRefresh(session, source+"_after_containers"); err != nil {
		return err
	}
	return s.sendSelectedGuardianGemWornMedalRefresh(session, source+"_after_containers")
}

func (s *Service) sendSelectedCurrentContainerListsRefresh(session *gameSession, source string) error {
	return s.sendSelectedCurrentContainerListsWithRefresh(session, source, true)
}

func (s *Service) sendSelectedCurrentContainerListsWithRefresh(session *gameSession, source string, refresh bool) error {
	return s.sendCurrentContainerListTypesWithRefresh(session, source, refresh, currentSelectedItemListTypes)
}

// sendCurrentContainerListTypesWithRefresh 只重建 listTypes 点名的容器。全量档传的就是
// `currentSelectedItemListTypes`，所以默认路径的分包数量、顺序、包体与加这个参数之前
// 逐字节相同；开盒子的 changed_only 档传的是"本次真正变了的容器"（见
// box_open_container_refresh.go），用的仍是同一个 builder、同一段日志与同一个信封。
func (s *Service) sendCurrentContainerListTypesWithRefresh(session *gameSession, source string, refresh bool, listTypes []byte) error {
	if session == nil {
		return nil
	}
	if session.selectedItemListRefreshSent && !refresh {
		s.logPacketEvent("game-upper-current-item-list-refresh-skipped",
			"conn_id", session.connID,
			"channel_id", session.channel.ID,
			"source", source,
			"char_id", session.selectedCharacterID,
			"reason", "already_sent")
		return nil
	}
	if session.selectedCharacterID == 0 {
		s.logPacketEvent("game-upper-current-item-list-refresh-deferred",
			"conn_id", session.connID,
			"channel_id", session.channel.ID,
			"source", source,
			"reason", "character_not_selected")
		if refresh {
			return fmt.Errorf("current item-list refresh requires a selected character")
		}
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), currentRepositorySnapshotTimeout)
	defer cancel()

	type currentContainerListPlan struct {
		listType    byte
		body        []byte
		entrySource string
		entryCount  int
	}
	plan := make([]currentContainerListPlan, 0, len(listTypes))
	for _, listType := range listTypes {
		body, entrySource, entryCount, ok := s.buildCurrentItemListBodyForSession(ctx, session, listType)
		if !ok {
			if refresh {
				return fmt.Errorf(
					"current item-list refresh could not build required list %d for character %d",
					listType,
					session.selectedCharacterID,
				)
			}
			continue
		}
		plan = append(plan, currentContainerListPlan{
			listType:    listType,
			body:        body,
			entrySource: entrySource,
			entryCount:  entryCount,
		})
	}

	sent := 0
	for _, entry := range plan {
		s.logPacketEvent("game-upper-current-item-list-refresh-send",
			"conn_id", session.connID,
			"channel_id", session.channel.ID,
			"source", source,
			"char_id", session.selectedCharacterID,
			"msg_id", uint16(dnfenum.CmdPacketLeaveParty),
			"list_type", entry.listType,
			"entry_count", entry.entryCount,
			"body_len", len(entry.body),
			"body_source", entry.entrySource,
			"entry_size", currentItemListEntrySizeForType(entry.listType),
			"mcp_handler", "sub_1D72380")
		if err := s.sendCurrentSceneItemList(session, uint16(dnfenum.CmdPacketLeaveParty), entry.body); err != nil {
			return err
		}
		sent++
	}
	if sent > 0 {
		session.selectedItemListRefreshSent = true
	}
	return nil
}

func (s *Service) sendSelectedIncrementalItemSlotRefreshes(
	session *gameSession,
	operation string,
	refreshes []alignedcmd.ItemSlotRefresh,
) error {
	if len(refreshes) == 0 {
		return nil
	}
	if session == nil || session.selectedCharacterID == 0 {
		return fmt.Errorf("aligned item-slot refresh requires a selected character")
	}
	repos, ok := s.repositoryGroup()
	if !ok || repos.Inventory == nil {
		return fmt.Errorf("aligned item-slot refresh requires inventory repository")
	}

	ctx, cancel := context.WithTimeout(context.Background(), currentRepositorySnapshotTimeout)
	defer cancel()
	characterID := strconv.Itoa(int(session.selectedCharacterID))
	inventory, found, err := repos.Inventory.Load(ctx, characterID)
	if err != nil {
		return fmt.Errorf("aligned item-slot refresh load character %s: %w", characterID, err)
	}
	if !found {
		return fmt.Errorf("aligned item-slot refresh inventory %s not found", characterID)
	}
	var account dnfrepo.AccountInventoryRecord
	accountLoaded := false
	loadAccount := func() error {
		if accountLoaded {
			return nil
		}
		if repos.AccountInventory == nil {
			return fmt.Errorf("aligned item-slot refresh requires account inventory repository")
		}
		accountID := s.accountIDForSession(session)
		if accountID == "" {
			return fmt.Errorf("aligned item-slot refresh requires account id for shared slot")
		}
		loaded, _, err := repos.AccountInventory.Load(ctx, accountID)
		if err != nil {
			return fmt.Errorf("aligned item-slot refresh load account %s: %w", accountID, err)
		}
		account = loaded
		accountLoaded = true
		return nil
	}

	entriesByList := make(map[byte][]currentItemListEntry)
	listOrder := make([]byte, 0, len(refreshes))
	seen := make(map[string]struct{}, len(refreshes))
	for _, refresh := range refreshes {
		if refresh.SlotIndex < 0 {
			return fmt.Errorf("aligned item-slot refresh has invalid slot %d", refresh.SlotIndex)
		}
		key := fmt.Sprintf("%d:%d", refresh.ListType, refresh.SlotIndex)
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		if _, knownList := entriesByList[refresh.ListType]; !knownList {
			listOrder = append(listOrder, refresh.ListType)
		}

		var stack dnfrepo.ItemStack
		var occupied bool
		if dnfrepo.IsAccountSharedInventorySlot(refresh.ListType, refresh.SlotIndex) {
			if err := loadAccount(); err != nil {
				return err
			}
			stack, occupied = account.Slots[key]
		} else {
			stack, occupied = inventory.Slots[key]
			if !occupied && refresh.ListType == 2 {
				stack, occupied = inventory.Warehouse[key]
			}
		}
		var entry currentItemListEntry
		if occupied && stack.ItemID > 0 && stack.Count >= 0 {
			entry = currentItemListEntryFromStack(refresh.ListType, refresh.SlotIndex, stack)
		} else {
			entry.patchCore(refresh.SlotIndex, math.MaxUint32, 0)
		}
		entriesByList[refresh.ListType] = append(entriesByList[refresh.ListType], entry)
	}

	for _, listType := range listOrder {
		entries := entriesByList[listType]
		if len(entries) == 0 {
			continue
		}
		if listType == currentPetInventoryListType {
			// list 7 的单行 op14 在客户端会走"宠物对象替换"路径：已存在的行只更新
			// 序号/数量，而**新建**一行要创建对象——现场实测（角色 3 领取宠物更名卡，
			// list 7 slot 191）客户端在库存界面模块 0x23082D3 直接闪退；同一批次里
			// list 0 的爆竹行完全正常。这里改成延迟整容器下发（登录引导用的同一条
			// list 7 容器包，已在 7:189/7:190 上验证），既保住宠物栏归属又不触发创建路径。
			s.scheduleDeferredPetInventoryRefresh(session, operation, len(entries))
			continue
		}
		if _, err := s.applyCurrentAvatarPVFTimeToEntries(ctx, entries); err != nil {
			s.logPacketEvent("game-upper-avatar-item-update-time-catalog-fallback", "char_id", session.selectedCharacterID, "list_type", listType, "err", err)
		}
		body := buildCurrentItemUpdateBody(listType, entries)
		s.logPacketEvent("game-aligned-command-item-slot-refresh-send",
			"conn_id", session.connID,
			"operation", operation,
			"char_id", session.selectedCharacterID,
			"msg_id", uint16(dnfenum.CmdPacketWalkoutPartyMember),
			"classification", 0,
			"list_type", listType,
			"entry_count", len(entries),
			"body_len", len(body),
			"sequence", "durable_owner_then_class1_ack_then_repository_backed_op14_changed_rows")
		if err := s.sendGameUpperRawClass(session, uint16(dnfenum.CmdPacketWalkoutPartyMember), body, 0); err != nil {
			return err
		}
	}
	return nil
}

// currentPetInventoryRefreshDelay 是"宠物背包(list 7)有变更"时整容器下发的延迟：
// 让领取事务（op95 ACK + op97 邮件快照）先走完，再按容器补一次 list 7。
const currentPetInventoryRefreshDelay = 1500 * time.Millisecond

// scheduleDeferredPetInventoryRefresh 把 list 7（宠物背包）的变更改成"稍后整容器下发"。
//
// 为什么不能直接发 op14 单行：现场实测角色 3 领取宠物更名卡时，服务端在 op95 ACK +
// op97 邮件快照之后紧跟一条 list 7 slot 191 的 op14 行，客户端立刻在库存界面模块
// 0x23082D3 闪退（同批次的 list 0 爆竹行完全正常，且此前同一条 list 0 领取链路
// 在 09:48 的抓包里被证明可用）。客户端对 list 7 的 op14 走的是"宠物对象替换"路径——
// 已存在的行只更新序号/数量，新建一行才会创建对象，这正是崩溃点；而整容器下发
// （登录引导用的同一条 list 7 容器包）已经在 7:189/7:190 上被验证过。
//
// 注：2026-09-28 曾把同样的"整容器下发"推广到"领取邮件附件"后的 list 0，用来规避
// 同址闪退；实机 A/B 证明**无效**（改走 msg_id=13 整容器之后客户端照崩），故已回退，
// 详见 changlog.md 里该次实验的结论。领取邮件附件闪退的成因不在服务端下行形状。
func (s *Service) scheduleDeferredPetInventoryRefresh(session *gameSession, operation string, entryCount int) {
	if s == nil || session == nil || session.connID == "" {
		return
	}
	characterID := session.selectedCharacterID
	if characterID == 0 {
		return
	}
	queue := s.ensureGameplayTimeQueue()
	if queue == nil {
		// 没有定时器队列时不做任何替换动作：宁愿晚一点（下次容器刷新/重登）补，
		// 也不能退回那条会让客户端崩溃的 op14 单行。
		s.logWarn("dnfbridge pet inventory refresh deferred without timer queue",
			"conn_id", session.connID,
			"operation", operation,
			"char_id", characterID,
			"entry_count", entryCount)
		return
	}
	name := fmt.Sprintf("pet-inventory-refresh-%s", session.connID)
	s.logPacketEvent("game-pet-inventory-refresh-deferred",
		"conn_id", session.connID,
		"operation", operation,
		"char_id", characterID,
		"list_type", currentPetInventoryListType,
		"entry_count", entryCount,
		"delay_ms", currentPetInventoryRefreshDelay.Milliseconds(),
		"reason", "list7_op14_row_creation_crashes_client")
	queue.ScheduleAfter(name, currentPetInventoryRefreshDelay, func(time.Time) {
		if session.selectedCharacterID != characterID {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), currentRepositorySnapshotTimeout)
		defer cancel()
		body, entrySource, sentCount, ok := s.buildCurrentItemListBodyForSession(ctx, session, currentPetInventoryListType)
		if !ok {
			s.logWarn("dnfbridge pet inventory refresh deferred build failed",
				"conn_id", session.connID,
				"operation", operation,
				"char_id", characterID)
			return
		}
		if err := s.sendCurrentSceneItemList(session, uint16(dnfenum.CmdPacketLeaveParty), body); err != nil {
			s.logWarn("dnfbridge pet inventory refresh deferred send failed",
				"conn_id", session.connID,
				"operation", operation,
				"char_id", characterID,
				"error", err)
			return
		}
		s.logPacketEvent("game-pet-inventory-container-refresh-send",
			"conn_id", session.connID,
			"operation", operation,
			"char_id", characterID,
			"msg_id", uint16(dnfenum.CmdPacketLeaveParty),
			"list_type", currentPetInventoryListType,
			"entry_count", sentCount,
			"body_len", len(body),
			"body_source", entrySource,
			"sequence", "deferred_after_claim_avoids_list7_op14_row_creation")
	})
}

func (s *Service) sendSelectedEquipmentItemUpdate(session *gameSession, source string) error {
	return s.sendSelectedEquipmentItemUpdateWithRefresh(session, source, false)
}

func (s *Service) sendSelectedEquipmentItemUpdateWithRefresh(session *gameSession, source string, refresh bool) error {
	if session == nil {
		return nil
	}
	if session.selectedEquipmentUpdateSent && !refresh {
		s.logPacketEvent("game-upper-current-equipment-item-update-skipped",
			"conn_id", session.connID,
			"channel_id", session.channel.ID,
			"source", source,
			"char_id", session.selectedCharacterID,
			"reason", "already_sent")
		return nil
	}
	if session.selectedCharacterID == 0 {
		s.logPacketEvent("game-upper-current-equipment-item-update-deferred",
			"conn_id", session.connID,
			"channel_id", session.channel.ID,
			"source", source,
			"reason", "character_not_selected")
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), currentRepositorySnapshotTimeout)
	defer cancel()

	body, entrySource, entryCount, ok := s.buildCurrentEquippedItemListBodyForSession(ctx, session)
	if !ok {
		return nil
	}
	if entryCount == 0 {
		s.logPacketEvent("game-upper-current-equipment-item-update-skipped",
			"conn_id", session.connID,
			"channel_id", session.channel.ID,
			"source", source,
			"char_id", session.selectedCharacterID,
			"msg_id", uint16(dnfenum.CmdPacketWalkoutPartyMember),
			"list_type", 3,
			"entry_count", entryCount,
			"body_len", len(body),
			"body_source", entrySource,
			"reason", "no_equipped_entries")
		session.selectedEquipmentUpdateSent = true
		return nil
	}

	s.logPacketEvent("game-upper-current-equipment-item-update-send",
		"conn_id", session.connID,
		"channel_id", session.channel.ID,
		"source", source,
		"char_id", session.selectedCharacterID,
		"msg_id", uint16(dnfenum.CmdPacketWalkoutPartyMember),
		"list_type", 3,
		"entry_count", entryCount,
		"body_len", len(body),
		"body_source", entrySource,
		"entry_size", currentItemListEntryWireSize,
		"evidence", "current_0x77_rows_restored_after_0x54_update_crashed")
	if err := s.sendGameUpperRawClass(session, uint16(dnfenum.CmdPacketWalkoutPartyMember), body, 0); err != nil {
		return err
	}
	session.selectedEquipmentUpdateSent = true
	return nil
}

func (s *Service) sendSelectedCurrentEquipmentSlotItemUpdates(session *gameSession, source string) error {
	if session == nil {
		return nil
	}
	if session.selectedCharacterID == 0 {
		s.logPacketEvent("game-upper-current-equipment-slot-items-refresh-deferred",
			"conn_id", session.connID,
			"channel_id", session.channel.ID,
			"source", source,
			"reason", "character_not_selected")
		return nil
	}
	repos, ok := s.repositoryGroup()
	if !ok || repos.Equipment == nil {
		s.logPacketEvent("game-upper-current-equipment-slot-items-refresh-skipped",
			"conn_id", session.connID,
			"channel_id", session.channel.ID,
			"source", source,
			"char_id", session.selectedCharacterID,
			"reason", "equipment_repository_unavailable")
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), currentRepositorySnapshotTimeout)
	defer cancel()

	characterID := strconv.Itoa(int(session.selectedCharacterID))
	entries, found, err := currentItemListEntriesFromEquipment(ctx, repos.Equipment, characterID, repos.Pet)
	if err != nil {
		return err
	}
	if !found || len(entries) == 0 {
		s.logPacketEvent("game-upper-current-equipment-slot-items-refresh-skipped",
			"conn_id", session.connID,
			"channel_id", session.channel.ID,
			"source", source,
			"char_id", session.selectedCharacterID,
			"reason", "no_occupied_equipment_entries")
		return nil
	}
	patchedUsePeriods, usePeriodErr := s.applyCurrentPVFUsePeriodsToEntriesWithLoadedCatalog(ctx, entries)
	if usePeriodErr != nil {
		s.logPacketEvent("game-upper-current-equipment-slot-items-use-period-wire-projection-failed",
			"conn_id", session.connID,
			"char_id", session.selectedCharacterID,
			"list_type", currentSocketListEquipment,
			"err", usePeriodErr)
	}
	if patchedUsePeriods > 0 {
		s.logPacketEvent("game-upper-current-equipment-slot-items-use-period-wire-projection-applied",
			"conn_id", session.connID,
			"char_id", session.selectedCharacterID,
			"list_type", currentSocketListEquipment,
			"patched_rows", patchedUsePeriods)
	}
	body := buildCurrentItemUpdateBody(currentSocketListEquipment, entries)
	s.logPacketEvent("game-upper-current-equipment-slot-items-refresh-send",
		"conn_id", session.connID,
		"channel_id", session.channel.ID,
		"source", source,
		"char_id", session.selectedCharacterID,
		"msg_id", uint16(dnfenum.CmdPacketWalkoutPartyMember),
		"classification", 0,
		"list_type", currentSocketListEquipment,
		"entry_count", len(entries),
		"body_len", len(body),
		"entry_size", currentItemListEntryWireSize,
		"sequence", "op14_list3_occupied_equipment_item_rows")
	return s.sendGameUpperRawClass(session, uint16(dnfenum.CmdPacketWalkoutPartyMember), body, 0)
}
