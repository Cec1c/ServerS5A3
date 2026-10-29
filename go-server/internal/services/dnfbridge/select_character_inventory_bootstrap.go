package dnfbridge

import (
	"context"
	"fmt"
	"strconv"

	dnfenum "longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// currentSelectInventoryBootstrapListTypes is the complete selected-character
// container set installed after the local actor has been bound. The bodies and
// envelope are built with the current NoPack op13 reader contract (16-byte
// fixed upper header and current 0x77 item rows).
//
// 2026-09-28：末项 list 3（已穿戴 / 装备栏）是新加的第七个容器。现场证据是
// "list 3 从来没有作为容器下发过"（137 次引导 × 6 个 list，没有一次是 3）
// ⇒ 客户端的装备栏没有任何权威行，"穿上装备、装备栏空、模型却对"。
// 它排在最后、仍在 item-lock 快照与完整 mode1 之前；写入时机在客户端第一次
// op19 之前，所以 changlog.md:9986 那条"op13 重建管理器会把原生 op19 刚放好的
// 对象丢掉"的前提不存在（那条讲的是登录之后的宠物槽 26 搬运）。
// 完整取证、scope 与一键回退见 login_equipment_container.go。
var currentSelectInventoryBootstrapListTypes = []byte{0, 1, 2, 7, 12, currentGuildMedalInventoryListType, currentSocketListEquipment}

// currentSelectInventoryBootstrapListTypesRollback 是 2026-09-28 之前的六个容器，
// 顺序逐字节不变。写 DNFBRIDGE_LOGIN_EQUIPMENT_CONTAINER=suppressed 时用的就是它。
var currentSelectInventoryBootstrapListTypesRollback = []byte{0, 1, 2, 7, 12, currentGuildMedalInventoryListType}

type currentSelectInventoryBootstrapPacket struct {
	listType    byte
	body        []byte
	entrySource string
	entryCount  int
}

// sendCurrentSelectInventoryBootstrap owns the one-shot inventory bootstrap
// for a selected character. It must be called after the scene has created and
// bound the selected actor, and before the full equipment-bearing mode1 packet.
// Runtime mutations continue to use the native ACK path, op14, or an explicit
// op13 refresh where the command contract requires one.
func (s *Service) sendCurrentSelectInventoryBootstrap(session *gameSession, source string) error {
	return s.sendCurrentSelectInventoryBootstrapWithMainPreserved(session, source, false)
}

func (s *Service) sendCurrentSelectInventoryBootstrapWithMainPreserved(session *gameSession, source string, preserveMain bool) error {
	if session == nil || session.selectedCharacterID == 0 {
		return nil
	}
	if session.selectedItemListBootstrapCharacterID == session.selectedCharacterID {
		s.logPacketEvent("game-upper-select-inventory-bootstrap-skipped",
			"conn_id", session.connID,
			"channel_id", session.channel.ID,
			"source", source,
			"char_id", session.selectedCharacterID,
			"reason", "already_sent")
		return nil
	}

	s.cleanupSelectedExpiredRentalEquipment(session, source)
	ctx, cancel := context.WithTimeout(context.Background(), currentRepositorySnapshotTimeout)
	defer cancel()
	preserveMain = preserveMain && session.selectedMainInventoryCharacterID == session.selectedCharacterID
	if preserveMain {
		s.logGameEvent(session, "转职体验保留现有主背包对象", "source", source,
			"char_id", session.selectedCharacterID, "list_type", 0,
			"依据", "本连接已成功发送当前角色主背包，体验窗口仍持有转职书对象")
	}

	if !preserveMain {
		if err := s.migrateCurrentFateAccount(ctx, session); err != nil {
			return err
		}
		// 存量史诗碎片清扫（在**构包之前**，所以不需要任何 op14 刷新）。
		//
		// 为什么在这里：操作者背包 `0:122` 里那件 10300783 是"碎片当普通材料入包"
		// 那条缺陷路径塞进去的（现场封包日志 packet_log.txt line 996 的 op14 就是它）。
		// 只要它还在 `dnf_inventory_items` 里，客户端每次登录都会把材料页渲染一遍，
		// 操作者看到的就还是"碎片在材料里"。清扫把数量并入账号级图鉴账本并从背包
		// 移除，幂等（第二次没有命中就不写任何字节）。
		//
		// 失败一律**不阻断登录**：碎片留在背包是可修的数据问题，登不进去是可用性问题。
		if purged, purgeErr := s.purgeCurrentInventoryEpicPieces(ctx, session, source); purgeErr != nil {
			s.logWarn("dnfbridge epic piece inventory purge failed",
				"conn_id", session.connID,
				"char_id", session.selectedCharacterID,
				"source", source,
				"error", purgeErr)
		} else if purged > 0 {
			s.logPacketEvent("game-epic-piece-inventory-purge-committed",
				"conn_id", session.connID,
				"channel_id", session.channel.ID,
				"source", source,
				"char_id", session.selectedCharacterID,
				"purged_rows", purged,
				"asset_owner", "account_epic_piece_metadata_counter",
				"env", epicPieceGrantEnv)
		}

		// 存量小晶块/灵魂清扫（同样在**构包之前**，所以不需要任何 op14 刷新）。
		//
		// 为什么在这里：运维的总规则是"不管如何获得小晶块都进入晶块仓库"。逐条改发放
		// 路径（拾取/翻牌/盒子/礼包/任务/兑换/交易…）永远会漏；把收口放在"下一次进游戏
		// 之前"则对任何路径都成立。DB 现场：角色 1 `0:130` item 3037 count 16233
		// （Extra 只有身份键、没有 source = 邮件领取堆栈），角色 3/4/6 的材料页/快捷格
		// 里也有 `source=dungeon_pvf_drop_pickup` 的晶块行。见 cube_warehouse_routing.go。
		if purged, purgeErr := s.purgeCurrentInventoryCubes(ctx, session, source); purgeErr != nil {
			s.logWarn("dnfbridge cube warehouse purge failed",
				"conn_id", session.connID,
				"char_id", session.selectedCharacterID,
				"source", source,
				"error", purgeErr)
		} else if purged > 0 {
			s.logPacketEvent("game-cube-warehouse-purge-committed",
				"conn_id", session.connID,
				"channel_id", session.channel.ID,
				"source", source,
				"char_id", session.selectedCharacterID,
				"purged_rows", purged,
				"asset_owner", "account_inventory_crystal_warehouse",
				"env", cubeWarehouseRoutingEnv)
		}

	}
	// 7142的暖主背包继续由原生ACK/op14维护。首次登录已经执行迁移清扫；
	// 在保留对象的体验入图不再次无通知搬走主背包物品，其它入口仍执行原清扫。
	// Build the complete character snapshot before writing its first packet. A
	// later repository failure must not leave the client with only a subset of
	// its selected-character containers installed.
	//
	// list 3（已穿戴）由 DNFBRIDGE_LOGIN_EQUIPMENT_CONTAINER 决定在不在清单里
	// （默认在；写 suppressed 一键回退到六个容器，见 login_equipment_container.go）。
	listTypes := loginEquipmentBootstrapListTypes()
	if !loginEquipmentContainerAuthoritativeEnabled() {
		// 回退档也要留下"开关取的是哪个值"的证据：这条日志是操作者在
		// packet_log.txt 里区分"修复档没生效"和"我写了回退字面量"的唯一判据。
		s.logPacketEvent("game-login-equipment-container-suppressed",
			"conn_id", session.connID,
			"channel_id", session.channel.ID,
			"source", source,
			"char_id", session.selectedCharacterID,
			"list", currentSocketListEquipment,
			"rows", 0,
			"switch", loginEquipmentContainerSwitch(),
			"switch_env", loginEquipmentContainerEnv,
			"reason", "rollback_literal_restores_six_container_login_bootstrap")
	}
	plan := make([]currentSelectInventoryBootstrapPacket, 0, len(listTypes))
	for _, listType := range listTypes {
		if preserveMain && listType == dnfrepo.MainInventoryListType {
			continue
		}
		body, entrySource, entryCount, ok := s.buildCurrentSelectInventoryBootstrapBody(ctx, session, listType)
		if !ok {
			s.logPacketEvent("game-upper-select-inventory-bootstrap-deferred",
				"conn_id", session.connID,
				"channel_id", session.channel.ID,
				"source", source,
				"char_id", session.selectedCharacterID,
				"list_type", listType,
				"reason", "repository_snapshot_unavailable")
			return fmt.Errorf("selected character %d inventory list %d snapshot unavailable", session.selectedCharacterID, listType)
		}
		plan = append(plan, currentSelectInventoryBootstrapPacket{
			listType:    listType,
			body:        body,
			entrySource: entrySource,
			entryCount:  entryCount,
		})
	}
	// The current client keeps a separate equipment-lock model. It does not
	// derive that model from op13 item rows, so build the complete durable
	// snapshot before any container write and deliver it immediately after all
	// containers. An explicit empty list is required to clear a stale local
	// locked state before an item-use request such as op338 can be generated.
	lockSnapshot, lockSnapshotOK := s.buildCurrentItemLockSnapshotForSession(ctx, session)
	if !lockSnapshotOK {
		s.logPacketEvent("game-upper-select-inventory-bootstrap-deferred",
			"conn_id", session.connID,
			"channel_id", session.channel.ID,
			"source", source,
			"char_id", session.selectedCharacterID,
			"reason", "item_lock_snapshot_unavailable")
		return fmt.Errorf("selected character %d item-lock snapshot unavailable", session.selectedCharacterID)
	}
	fateState, fateKnown, err := s.currentFateState(ctx, session)
	if err != nil {
		return err
	}
	if !fateKnown {
		return fmt.Errorf("selected character %d raid currency snapshot unavailable", session.selectedCharacterID)
	}
	for index, packet := range plan {
		s.logPacketEvent("game-upper-select-inventory-bootstrap-send",
			"conn_id", session.connID,
			"channel_id", session.channel.ID,
			"source", source,
			"char_id", session.selectedCharacterID,
			"packet_index", index,
			"msg_id", uint16(dnfenum.CmdPacketLeaveParty),
			"list_type", packet.listType,
			"entry_count", packet.entryCount,
			"body_len", len(packet.body),
			"body_source", packet.entrySource,
			"entry_size", currentItemListEntrySizeForType(packet.listType),
			"wire_contract", "current_exe_sub_1D72380_fixed16_0x77",
			"lifecycle", "current_exe_actor_bound_then_character_containers_before_full_mode1")
		if packet.listType == currentSocketListEquipment {
			// 这一行就是操作者下一条 packet_log 里能用一句话证明"装备栏容器
			// 已经在登录时下发"的判据（默认档才可能出现；回退档打的是
			// game-login-equipment-container-suppressed）。见 login_equipment_container.go。
			s.logPacketEvent("game-login-equipment-container-sent",
				"conn_id", session.connID,
				"channel_id", session.channel.ID,
				"source", source,
				"char_id", session.selectedCharacterID,
				"list", packet.listType,
				"rows", packet.entryCount,
				"msg_id", uint16(dnfenum.CmdPacketLeaveParty),
				"classification", 0,
				"list_type", packet.listType,
				"entry_count", packet.entryCount,
				"body_len", len(packet.body),
				"body_source", packet.entrySource,
				"entry_size", currentItemListEntrySizeForType(packet.listType),
				"switch", loginEquipmentContainerSwitch(),
				"switch_env", loginEquipmentContainerEnv,
				"lifecycle", "current_exe_actor_bound_before_item_lock_snapshot_before_full_mode1",
				"evidence", "equipment_list_never_sent_as_container_137_bootstraps_x_6_lists_without_list3")
		}
		if err := s.sendCurrentSceneItemList(session, uint16(dnfenum.CmdPacketLeaveParty), packet.body); err != nil {
			return err
		}
	}
	if err := s.sendCurrentSelectItemLockSnapshot(
		session,
		source+"_after_selected_containers_before_full_mode1",
		lockSnapshot,
	); err != nil {
		return err
	}
	// 账号级史诗图鉴账本（真档 966 条碎片目录）。
	//
	// 位置依据：厂商 9.16 参考服务端的同名事件 source 字符串是
	// `initial_town_before_final_transition_after_actor_before_full_player_state_after_selected_containers`
	// —— 即"actor 已绑定、容器已发完之后、完整 mode1 之前"，就是这里。
	// 它按**账号**下发（`account_id=dnf:…`），所以同账号下换角色登录会拿到同一份进度。
	// 形状与开关见 epic_piece_book.go。
	if err := s.sendCurrentEpicPieceBalanceSnapshot(
		ctx,
		session,
		source+"_after_selected_containers_before_full_mode1",
	); err != nil {
		return err
	}
	if err := s.sendCurrentAikenSetSnapshot(ctx, session); err != nil {
		return err
	}
	if err := s.sendPreparedCurrentFateSnapshot(session, fateState, true); err != nil {
		return err
	}
	session.selectedItemListRefreshSent = true
	session.selectedItemListBootstrapCharacterID = session.selectedCharacterID
	s.armCurrentDailyDeleteClock(session)
	s.logPacketEvent("game-upper-select-inventory-bootstrap-committed",
		"conn_id", session.connID,
		"channel_id", session.channel.ID,
		"source", source,
		"char_id", session.selectedCharacterID,
		"packet_count", len(plan)+1,
		"item_lock_snapshot_body_len", len(lockSnapshot))
	return nil
}

func (s *Service) buildCurrentSelectInventoryBootstrapBody(
	ctx context.Context,
	session *gameSession,
	listType byte,
) ([]byte, string, int, bool) {
	body, source, count, ok := s.buildCurrentItemListBodyForSession(ctx, session, listType)
	if ok {
		return body, source, count, true
	}
	// A real snapshot read error must survive every healthy-empty fallback.
	if source == currentInventorySnapshotFailureSource {
		return nil, source, count, false
	}

	// An absent inventory record is a real empty inventory. 86JP still creates
	// every client container on selection, so emit an empty current-EXE body;
	// repository errors remain fail-closed and are never disguised as empty.
	repos, repositoriesOK := s.repositoryGroup()
	if !repositoriesOK {
		return nil, source, count, false
	}
	characterID := strconv.Itoa(int(session.selectedCharacterID))
	if listType == currentSocketListEquipment {
		// list 3 的"记录缺失"和 list 0 的"背包记录缺失"是同一件事：这个角色
		// 还没有任何已穿戴行 = 真实的空装备栏。86JP 选角时同样会把每一个客户端
		// 容器建出来，所以这里发一个空容器体（`03 0000`，与 list 38 的空体同形）；
		// **仓储错误仍然 fail-closed**，绝不被伪装成空容器。
		if repos.Equipment == nil {
			return nil, source, count, false
		}
		_, found, err := repos.Equipment.Load(ctx, characterID)
		if err != nil || found {
			return nil, source, count, false
		}
		return buildCurrentItemListBody(listType, nil, dnfrepo.CharacterContainerState{}), "equipment_absent_empty", 0, true
	}
	// A configured account warehouse makes a healthy absent list-0 snapshot
	// known in the main builder. Reaching this fallback therefore means that
	// snapshot failed; an absent character record cannot turn it into empty.
	if listType == dnfrepo.MainInventoryListType && repos.AccountInventory != nil {
		return nil, source, count, false
	}
	if repos.Inventory == nil {
		return nil, source, count, false
	}
	_, found, err := repos.Inventory.Load(ctx, characterID)
	if err != nil || found {
		return nil, source, count, false
	}
	state := s.loadCurrentItemListContainerState(ctx, repos.Settings, characterID, listType)
	return buildCurrentItemListBody(listType, nil, state), "inventory_absent_empty", 0, true
}
