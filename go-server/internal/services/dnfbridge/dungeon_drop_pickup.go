package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfdungeon "longheng.io/server/internal/modules/dnf/dungeon"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	"longheng.io/server/internal/modules/dnf/itemexpiration"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

const (
	// Opcode 39 is directional: C2S op39 reports monster death, while current
	// class0/S2C op39 is the ordinary scene-item pickup result.
	currentDungeonPickupResultOpcode      uint16 = 39
	currentDungeonPickupResultSize               = 19
	currentDungeonGoldPickupPartyRows            = 8
	currentDungeonGoldPickupResultSize           = 6 + currentDungeonGoldPickupPartyRows*5 + 1 + 4 + 4
	currentDungeonPickupRequestOpcode     uint16 = 43
	currentDungeonPickupInventoryFullCode byte   = 4
	currentDungeonPickupQuickSlotStart           = int16(3)
	currentDungeonPickupQuickSlotEnd             = int16(8)
)

var (
	errDungeonPickupRuntimeMissing     = errors.New("dnf dungeon pickup runtime is missing")
	errDungeonPickupTransactionMissing = errors.New("dnf dungeon pickup inventory transaction is missing")
	errDungeonPickupInventoryMissing   = errors.New("dnf dungeon pickup inventory record is missing")
	errDungeonPickupInventoryFull      = errors.New("dnf dungeon pickup inventory category is full")
	errDungeonPickupItemInvalid        = errors.New("dnf dungeon pickup item is invalid")
	errDungeonPickupStackLimit         = errors.New("dnf dungeon pickup exceeds real PVF stack limit")
	errDungeonPickupResponseInvalid    = errors.New("dnf dungeon pickup response values are invalid")
	errDungeonPickupDropStateInvalid   = errors.New("dnf dungeon pickup drop state is invalid")
)

func buildCurrentDungeonNormalPickupResultBody(
	dropObjectKey uint32,
	pickerActorObjectKey uint16,
	destinationSlot uint16,
) ([]byte, error) {
	return buildCurrentDungeonItemPickupResultBody(dropObjectKey, pickerActorObjectKey, destinationSlot, false)
}

func buildCurrentDungeonPickupResultForItem(dropObjectKey uint32, pickerActorObjectKey uint16, destinationSlot uint16, definition dungeonDropItemDefinition) ([]byte, error) {
	c := currentPVFItemTargetContainer(definition)
	return buildCurrentDungeonItemPickupResultBody(dropObjectKey, pickerActorObjectKey, destinationSlot, c.GuildMedal && c.ListType == 38 && c.SlotStart == 0)
}

func buildCurrentDungeonItemPickupResultBody(dropObjectKey uint32, pickerActorObjectKey uint16, destinationSlot uint16, allowZero bool) ([]byte, error) {
	if dropObjectKey == 0 || pickerActorObjectKey == 0 || (destinationSlot == 0 && !allowZero) {
		return nil, fmt.Errorf(
			"%w: drop=%d picker=%d destination=%d",
			errDungeonPickupResponseInvalid,
			dropObjectKey,
			pickerActorObjectKey,
			destinationSlot,
		)
	}
	var writer packetWriter
	writer.writeUint32(dropObjectKey)
	writer.writeUint16(pickerActorObjectKey)
	writer.writeBytes(make([]byte, 8)) // read by current EXE; semantics unproved, default constructor state
	writer.writeUint16(pickerActorObjectKey)
	writer.writeUint16(destinationSlot)
	writer.writeByte(0) // read by current EXE; not consumed by the visible permission/UI path
	body := writer.bytes()
	if len(body) != currentDungeonPickupResultSize {
		return nil, fmt.Errorf("%w: body_len=%d", errDungeonPickupResponseInvalid, len(body))
	}
	return body, nil
}

func buildCurrentDungeonGoldPickupResultBody(
	dropObjectKey uint32,
	pickerActorObjectKey uint16,
	amount uint32,
	partySlot int,
) ([]byte, error) {
	if dropObjectKey == 0 || pickerActorObjectKey == 0 || amount == 0 || amount > math.MaxInt32 ||
		partySlot < 0 || partySlot >= currentDungeonGoldPickupPartyRows {
		return nil, fmt.Errorf(
			"%w: drop=%d picker=%d amount=%d party_slot=%d",
			errDungeonPickupResponseInvalid,
			dropObjectKey,
			pickerActorObjectKey,
			amount,
			partySlot,
		)
	}
	var writer packetWriter
	writer.writeUint32(dropObjectKey)
	writer.writeUint16(pickerActorObjectKey)
	// 原生 op39 金币分支按队伍固定槽读取 8 行：每行 u8 present + u32 amount，
	// 中选行再追加 u8 component-count + 组件 + u32 尾标志。组件数 0 只走文本
	// 包装并清除地面对象；组件数 1、组件值 0 走地面对象保留的拾取反馈行，
	// 不会额外发钱。中选槽之外的槽位写 present=0/amount=0，总计 55 字节。
	// 原生解码与实际音效另行核实，本条只固定线上载荷布局。
	for slot := 0; slot < currentDungeonGoldPickupPartyRows; slot++ {
		if slot == partySlot {
			writer.writeByte(1)
			writer.writeUint32(amount)
			writer.writeByte(1)
			writer.writeUint32(0) // effect component, no additional currency
			writer.writeUint32(1) // native pickup-effect tail, matching the captured reference
		} else {
			writer.writeByte(0)
			writer.writeUint32(0)
		}
	}
	body := writer.bytes()
	if len(body) != currentDungeonGoldPickupResultSize {
		return nil, fmt.Errorf("%w: body_len=%d", errDungeonPickupResponseInvalid, len(body))
	}
	return body, nil
}

func currentDungeonGoldPickupPartySlot(session *gameSession) int {
	state := runtimePartyStateSnapshot(session)
	if state.PartyID == 0 {
		return 0
	}
	// 使用房间原生队伍表的固定槽位，保留离队成员留下的空洞：切片下标不等于
	// 线上槽位。
	for _, member := range runtimePartyMembers(state) {
		if member.UserID == session.selectedCharacterID {
			return int(member.Slot)
		}
	}
	return -1
}

func (s *Service) handleCurrentDungeonPickup(session *gameSession, body []byte) error {
	s.logGoldDiagnosticPickup(session, "game-dungeon-pickup-received", "body_len", len(body))
	request, err := dungeoncmd.DecodeGetItemRequest(body)
	if err != nil {
		s.logGameEvent(session, "game-dungeon-pickup-blocked",
			"body_len", len(body),
			"reason", "current_exe_op43_ordinary_request_malformed",
			"error", err)
		return nil
	}
	if request.FixedZero != 0 {
		s.logGameEvent(session, "game-dungeon-pickup-blocked",
			"drop_object_key", request.DropObjectKey,
			"fixed_byte", request.FixedZero,
			"reason", "current_exe_op43_fixed_zero_mismatch")
		return nil
	}
	if session == nil {
		return nil
	}
	// 队伍槽位快照在拿副本/实例锁之前完成，金币回包投影在资产事务之前构建并校验。
	goldPartySlot := currentDungeonGoldPickupPartySlot(session)
	session.dungeon.mu.Lock()
	defer session.dungeon.mu.Unlock()
	runtime := session.dungeon.runtime
	// 队伍级共享实例下，掉落账本是**两个会话共用**的：整个拾取流程（读状态 → 资产事务
	// → 标记 consumed）必须在实例锁下串行，否则两人同时点同一份掉落会各自入账一次。
	// 锁序固定为 session.dungeon.mu → instance.mu；单人/无队伍时返回 nil，无副作用。
	instance := runtime.lockPartyDungeonInstance()
	defer unlockPartyDungeonInstance(instance)
	// `runtime.DropOwner == nil` 在**队伍副本实例里不算缺账本**：实例的共享账本会在
	// 下面按需补出来（见 `partySharedDungeonDropOwnerLocked`），所以这里只对单人会话保持
	// 原来的 fail-closed 判据。
	if runtime == nil || runtime.Session == nil || runtime.Room == nil ||
		(runtime.DropOwner == nil && runtime.partyInstance == nil) {
		s.logGameEvent(session, "game-dungeon-pickup-blocked",
			"drop_object_key", request.DropObjectKey,
			"reason", "dungeon_drop_runtime_missing",
			"error", errDungeonPickupRuntimeMissing)
		return nil
	}
	scene, ok := runtime.Session.Scene()
	if !ok {
		s.logGameEvent(session, "game-dungeon-pickup-blocked",
			"drop_object_key", request.DropObjectKey,
			"reason", "dungeon_scene_missing",
			"error", errDungeonPickupRuntimeMissing)
		return nil
	}
	pickerActorObjectKey := currentSceneActorObjectKey(session.selectedCharacterID)
	roomKey := runtimeDungeonRoomKeyFromScene(scene)
	// 队伍副本实例：深渊奖励掉落登记在**实例的共享账本**上。第 4 次发版现场证明
	// 各人各持一份账本是"队员捡不到"的直接原因（队员发了 10 条 `type=43 GET_ITEM`，
	// 服务端 8 条回 `errDungeonDropNotFound`）。这里先按共享账本查、查不到再回落到
	// 本会话账本（普通怪掉落仍登记在各自账本里）。回退档（`legacy`）不碰账本指针，
	// 逐字节等于第 3 次发版。
	lookupOwners := make([]*runtimeDungeonDropOwner, 0, 2)
	if abyssPartyDropOwnerModeFromEnv().sharesLedger() {
		if shared := partySharedDungeonDropOwnerLocked(runtime); shared != nil {
			lookupOwners = append(lookupOwners, shared)
		}
	}
	if runtime.DropOwner != nil {
		duplicate := false
		for _, existing := range lookupOwners {
			if existing == runtime.DropOwner {
				duplicate = true
				break
			}
		}
		if !duplicate {
			lookupOwners = append(lookupOwners, runtime.DropOwner)
		}
	}
	var drop *runtimeDungeonDrop
	for _, lookupOwner := range lookupOwners {
		drop, err = lookupOwner.owned(
			request.DropObjectKey,
			roomKey,
			pickerActorObjectKey,
		)
		if err == nil {
			break
		}
	}
	if drop == nil && err == nil {
		err = errDungeonDropNotFound
	}
	if err != nil {
		// 回退：op29 预生成的建筑掉落（柱子那条 extra entry）在客户端眼里只有
		// **scene slot**（= `GlobalSeq`），没有我们发号器给的 objectKey。只有
		// `MapOwnedPassiveObject` 的掉落会进 `bySceneSlot`，所以普通击杀掉落的查找
		// 结果与今天完全一致；这一支在默认档下永远找不到东西（probe 档才会登记）。
		// 见 dungeon_passive_object_drop.go。
		if request.DropObjectKey <= math.MaxUint16 {
			if fallback, fallbackErr := runtime.DropOwner.ownedMapOwnedPassiveObjectBySceneSlot(
				uint16(request.DropObjectKey),
				roomKey,
				pickerActorObjectKey,
			); fallbackErr == nil {
				s.logGameEvent(session, "game-dungeon-pickup-scene-slot-fallback",
					"dungeon_id", runtime.Dungeon.ID,
					"room", scene.Coordinate.String(),
					"map_id", scene.Map.Map.ID,
					"drop_object_key", request.DropObjectKey,
					"scene_slot", fallback.SceneSlot,
					"item_id", fallback.Item.ItemID,
					"picker_actor_object_key", pickerActorObjectKey,
					"source", "op29_pre_generated_map_owned_passive_object_drop_has_no_actor_owner")
				drop, err = fallback, nil
			}
		}
	}
	if err != nil {
		s.logGameEvent(session, "game-dungeon-pickup-blocked",
			"dungeon_id", runtime.Dungeon.ID,
			"room", scene.Coordinate.String(),
			"map_id", scene.Map.Map.ID,
			"drop_object_key", request.DropObjectKey,
			"picker_actor_object_key", pickerActorObjectKey,
			"reason", "runtime_drop_owner_rejected",
			"error", businessLogDiagnostic(err))
		return nil
	}
	if drop.Status == runtimeDungeonDropConsumed {
		responseSize := currentDungeonPickupResultSize
		if drop.isGold() {
			responseSize = currentDungeonGoldPickupResultSize
		}
		if len(drop.PickupResponseBody) != responseSize {
			s.logGameEvent(session, "game-dungeon-pickup-blocked",
				"drop_object_key", drop.ObjectKey,
				"reason", "consumed_drop_response_missing",
				"error", fmt.Errorf("%w: response_len=%d", errDungeonPickupDropStateInvalid, len(drop.PickupResponseBody)))
			return nil
		}
		s.logGameEvent(session, "game-dungeon-pickup-replayed",
			"dungeon_id", runtime.Dungeon.ID,
			"room", scene.Coordinate.String(),
			"map_id", scene.Map.Map.ID,
			"drop_object_key", drop.ObjectKey,
			"item_id", drop.Item.ItemID,
			"gold_after", drop.GoldAfter,
			"destination_slot", drop.DestinationSlot,
			"request_context", request.ObjectContext,
			"request_player_x", request.PlayerX,
			"request_player_y", request.PlayerY,
			"request_drop_x", request.DropX,
			"request_drop_y", request.DropY,
			"response_opcode", currentDungeonPickupResultOpcode)
		if err := s.sendGameUpperRawClass(
			session,
			currentDungeonPickupResultOpcode,
			append([]byte(nil), drop.PickupResponseBody...),
			0,
		); err != nil {
			return err
		}
		if drop.isGold() && drop.GoldAfter > 0 {
			return s.sendGameUpperRawClass(
				session,
				uint16(dnfenum.CmdPacketWalkoutPartyMember),
				buildCurrentGoldStateBody(drop.GoldAfter),
				0,
			)
		}
		if drop.EpicPieceBalance > 0 {
			// 史诗碎片：掉落对象已经入过账号账本。重放同一次拾取时只补一条增量账本
			// （绝对值来自 drop 上的存档），绝不二次入账。官方24.9同时恢复
			// 原生拾取投影所用的哨兵槽位，见 epicPiecePickupFollowups。
			return s.epicPiecePickupFollowups(
				session,
				drop.Item.ItemID,
				drop.EpicPieceBalance,
				"dungeon_pickup_replay",
			)
		}
		if len(drop.PickupItemUpdateBody) > 0 {
			return s.sendGameUpperRawClass(
				session,
				uint16(dnfenum.CmdPacketWalkoutPartyMember),
				append([]byte(nil), drop.PickupItemUpdateBody...),
				0,
			)
		}
		return nil
	}
	if drop.Status != runtimeDungeonDropAvailable {
		s.logGameEvent(session, "game-dungeon-pickup-blocked",
			"drop_object_key", drop.ObjectKey,
			"status", drop.Status,
			"reason", "runtime_drop_state_invalid",
			"error", errDungeonPickupDropStateInvalid)
		return nil
	}
	if drop.isGold() {
		// 事务前构建并校验 55 字节投影：金额或队伍槽位非法时不入账、不消费
		// 掉落、不发包，避免提交后才发现无法回包。
		responseBody, err := buildCurrentDungeonGoldPickupResultBody(drop.ObjectKey, pickerActorObjectKey, drop.Amount, goldPartySlot)
		if err != nil {
			s.logGameEvent(session, "game-dungeon-pickup-blocked",
				"drop_object_key", drop.ObjectKey,
				"amount", drop.Amount,
				"party_slot", goldPartySlot,
				"reason", "gold_pickup_response_values_invalid",
				"error", err)
			return nil
		}
		repositories, ok := s.repositoryGroup()
		if !ok || repositories.CharacterAssets == nil {
			s.logGameEvent(session, "game-dungeon-pickup-blocked",
				"drop_object_key", drop.ObjectKey,
				"item_id", drop.Item.ItemID,
				"amount", drop.Amount,
				"reason", "character_gold_transaction_missing",
				"error", errDungeonPickupTransactionMissing)
			return nil
		}
		characterID := strconv.Itoa(int(session.selectedCharacterID))
		goldAfter, err := grantCurrentDungeonPickupGold(
			context.Background(),
			repositories.CharacterAssets,
			characterID,
			drop.Amount,
			time.Now().UTC(),
		)
		if err != nil {
			s.logGameEvent(session, "game-dungeon-pickup-blocked",
				"dungeon_id", runtime.Dungeon.ID,
				"room", scene.Coordinate.String(),
				"map_id", scene.Map.Map.ID,
				"drop_object_key", drop.ObjectKey,
				"item_id", drop.Item.ItemID,
				"amount", drop.Amount,
				"reason", "character_gold_transaction_failed_drop_remains_available",
				"error", err)
			return nil
		}
		drop.Status = runtimeDungeonDropConsumed
		drop.GoldAfter = goldAfter
		drop.PickupResponseBody = append([]byte(nil), responseBody...)
		s.logGameEvent(session, "game-dungeon-pickup-accepted",
			"dungeon_id", runtime.Dungeon.ID,
			"room", scene.Coordinate.String(),
			"map_id", scene.Map.Map.ID,
			"drop_object_key", drop.ObjectKey,
			"scene_slot", drop.SceneSlot,
			"item_id", drop.Item.ItemID,
			"amount", drop.Amount,
			"gold_after", goldAfter,
			"picker_actor_object_key", pickerActorObjectKey,
			"request_context", request.ObjectContext,
			"request_player_x", request.PlayerX,
			"request_player_y", request.PlayerY,
			"request_token0", request.Token0,
			"request_drop_x", request.DropX,
			"request_drop_y", request.DropY,
			"request_token1", request.Token1,
			"request_token2", request.Token2,
			"response_opcode", currentDungeonPickupResultOpcode,
			"response_body_len", len(responseBody),
			"asset_owner", "character_assets_uow_gold")
		if err := s.sendGameUpperRawClass(session, currentDungeonPickupResultOpcode, responseBody, 0); err != nil {
			return err
		}
		return s.sendGameUpperRawClass(
			session,
			uint16(dnfenum.CmdPacketWalkoutPartyMember),
			buildCurrentGoldStateBody(goldAfter),
			0,
		)
	}
	repositories, ok := s.repositoryGroup()
	if drop.DiscardOrigin != nil {
		if !ok || repositories.AccountAssets == nil {
			s.logGameEvent(session, "game-dungeon-pickup-blocked",
				"drop_object_key", drop.ObjectKey,
				"item_id", drop.Item.ItemID,
				"reason", "discard_origin_account_assets_transaction_missing",
				"error", errDungeonPickupTransactionMissing)
			return nil
		}
		characterID := strconv.Itoa(int(session.selectedCharacterID))
		destinationSlot, itemUpdate, err := restoreCurrentDungeonDiscardedItem(
			context.Background(),
			repositories,
			strings.TrimSpace(s.accountIDForSession(session)),
			characterID,
			*drop.DiscardOrigin,
			drop.Item,
			time.Now().UTC(),
		)
		if err != nil {
			s.logGameEvent(session, "game-dungeon-pickup-blocked",
				"dungeon_id", runtime.Dungeon.ID,
				"room", scene.Coordinate.String(),
				"map_id", scene.Map.Map.ID,
				"drop_object_key", drop.ObjectKey,
				"item_id", drop.Item.ItemID,
				"amount", drop.Amount,
				"source_slot", drop.DiscardOrigin.SourceSlot,
				"account_owned", drop.DiscardOrigin.AccountOwned,
				"reason", "discard_origin_restore_transaction_failed_drop_remains_available",
				"error", err)
			if errors.Is(err, errDungeonPickupInventoryFull) {
				// 自行丢弃物品也必须回满包失败，清除客户端等待和地面拾取中标记。
				return s.sendGameUpperRawClass(session, currentDungeonPickupRequestOpcode,
					[]byte{0, currentDungeonPickupInventoryFullCode}, 1)
			}
			return nil
		}
		responseBody, err := buildCurrentDungeonNormalPickupResultBody(
			drop.ObjectKey,
			pickerActorObjectKey,
			destinationSlot,
		)
		if err != nil {
			drop.Status = runtimeDungeonDropConsumed
			drop.DestinationSlot = destinationSlot
			s.logGameEvent(session, "game-dungeon-pickup-response-blocked-after-commit",
				"drop_object_key", drop.ObjectKey,
				"item_id", drop.Item.ItemID,
				"destination_slot", destinationSlot,
				"reason", "discard_origin_pickup_response_invariant_failed_drop_consumed_to_prevent_duplicate_grant",
				"error", err)
			return nil
		}
		drop.Status = runtimeDungeonDropConsumed
		drop.DestinationSlot = destinationSlot
		drop.PickupResponseBody = append([]byte(nil), responseBody...)
		itemUpdateBody := buildCurrentItemUpdateBody(
			drop.DiscardOrigin.ListType,
			[]currentItemListEntry{itemUpdate},
		)
		drop.PickupItemUpdateBody = append([]byte(nil), itemUpdateBody...)
		s.logGameEvent(session, "game-dungeon-pickup-accepted",
			"dungeon_id", runtime.Dungeon.ID,
			"room", scene.Coordinate.String(),
			"map_id", scene.Map.Map.ID,
			"drop_object_key", drop.ObjectKey,
			"scene_slot", drop.SceneSlot,
			"item_id", drop.Item.ItemID,
			"amount", drop.Amount,
			"destination_slot", destinationSlot,
			"picker_actor_object_key", pickerActorObjectKey,
			"request_context", request.ObjectContext,
			"request_player_x", request.PlayerX,
			"request_player_y", request.PlayerY,
			"request_drop_x", request.DropX,
			"request_drop_y", request.DropY,
			"response_opcode", currentDungeonPickupResultOpcode,
			"response_body_len", len(responseBody),
			"asset_owner", "discard_origin_account_assets_uow",
			"item_update_opcode", uint16(dnfenum.CmdPacketWalkoutPartyMember),
			"item_update_body_len", len(itemUpdateBody))
		if err := s.sendGameUpperRawClass(session, currentDungeonPickupResultOpcode, responseBody, 0); err != nil {
			return err
		}
		return s.sendGameUpperRawClass(
			session,
			uint16(dnfenum.CmdPacketWalkoutPartyMember),
			itemUpdateBody,
			0,
		)
	}
	// ── 史诗碎片 → 账号级史诗图鉴（本批修法）────────────────────────────────
	//
	// 根因（真档取证见 epic_piece_book.go 文件头）：`Etc/Independent_Drop.etc` 的
	// `[independent drop]` 段里有 244 个 1030xxxx 道具 id，其中 **236 个**就是
	// `etc/epicpieceinfo.etc [piece list]` 里的史诗碎片。它们由
	// `dungeon_drop_special.go` 原样投到地面，此前在**这里**被当成普通 `[material]`
	// 堆进材料页（list 0 槽 121..176）—— 这就是操作者看到的"史诗碎片进背包"。
	//
	// 修法：碎片**不占背包任何槽位**（参考实现文档原文："不写 character_inventory_items /
	// 不写 account_inventory_items"），只写账号级账本，回包的目的槽位写一个固定的
	// 非 0 哨兵（currentEpicPiecePickupEffectSlot），随后补一条增量账本包。
	if s.epicPieceBook().enabled() {
		if pieceCatalog, catalogErr := s.epicPieceCatalog(); catalogErr == nil &&
			pieceCatalog.isEpicPiece(drop.Item.ItemID) {
			if !ok || repositories.Account == nil {
				s.logGameEvent(session, "game-dungeon-pickup-blocked",
					"drop_object_key", drop.ObjectKey,
					"item_id", drop.Item.ItemID,
					"reason", "account_epic_piece_counter_missing",
					"error", errDungeonPickupTransactionMissing)
				return nil
			}
			accountID := strings.TrimSpace(s.accountIDForSession(session))
			balance, creditErr := addCurrentEpicPieceBalance(
				context.Background(),
				repositories.Account,
				accountID,
				drop.Item.ItemID,
				drop.Amount,
				time.Now().UTC(),
			)
			if creditErr != nil {
				s.logGameEvent(session, "game-dungeon-pickup-blocked",
					"dungeon_id", runtime.Dungeon.ID,
					"room", scene.Coordinate.String(),
					"map_id", scene.Map.Map.ID,
					"drop_object_key", drop.ObjectKey,
					"item_id", drop.Item.ItemID,
					"amount", drop.Amount,
					"account_id", accountID,
					"reason", "account_epic_piece_credit_failed_drop_remains_available",
					"error", creditErr)
				return nil
			}
			responseBody, responseErr := buildCurrentDungeonNormalPickupResultBody(
				drop.ObjectKey,
				pickerActorObjectKey,
				currentEpicPiecePickupEffectSlot,
			)
			if responseErr != nil {
				// 账本已经加过了。和"背包路径"同一个取舍：把地面对象标记为已消耗，
				// 否则重复拾取会再加一次。
				drop.Status = runtimeDungeonDropConsumed
				drop.DestinationSlot = currentEpicPiecePickupEffectSlot
				drop.EpicPieceBalance = balance
				s.logGameEvent(session, "game-dungeon-pickup-response-blocked-after-commit",
					"drop_object_key", drop.ObjectKey,
					"item_id", drop.Item.ItemID,
					"destination_slot", currentEpicPiecePickupEffectSlot,
					"reason", "epic_piece_pickup_response_invariant_failed_drop_consumed_to_prevent_duplicate_grant",
					"error", responseErr)
				return nil
			}
			drop.Status = runtimeDungeonDropConsumed
			drop.DestinationSlot = currentEpicPiecePickupEffectSlot
			drop.EpicPieceBalance = balance
			drop.PickupResponseBody = append([]byte(nil), responseBody...)
			s.logGameEvent(session, "game-dungeon-pickup-accepted",
				epicPieceBookFields(pieceCatalog, drop.Item.ItemID,
					"dungeon_id", runtime.Dungeon.ID,
					"room", scene.Coordinate.String(),
					"map_id", scene.Map.Map.ID,
					"drop_object_key", drop.ObjectKey,
					"scene_slot", drop.SceneSlot,
					"item_id", drop.Item.ItemID,
					"amount", drop.Amount,
					"destination_slot", currentEpicPiecePickupEffectSlot,
					"account_id", accountID,
					"absolute_balance", balance,
					"picker_actor_object_key", pickerActorObjectKey,
					"request_context", request.ObjectContext,
					"request_player_x", request.PlayerX,
					"request_player_y", request.PlayerY,
					"response_opcode", currentDungeonPickupResultOpcode,
					"response_body_len", len(responseBody),
					"asset_owner", "account_epic_piece_metadata_counter")...)
			if err := s.sendGameUpperRawClass(session, currentDungeonPickupResultOpcode, responseBody, 0); err != nil {
				return err
			}
			return s.epicPiecePickupFollowups(
				session,
				drop.Item.ItemID,
				balance,
				"dungeon_pickup_commit",
			)
		}
	}
	if !ok || repositories.CharacterItems == nil {
		s.logGameEvent(session, "game-dungeon-pickup-blocked",
			"drop_object_key", drop.ObjectKey,
			"item_id", drop.Item.ItemID,
			"reason", "character_items_transaction_missing",
			"error", errDungeonPickupTransactionMissing)
		return nil
	}
	characterID := strconv.Itoa(int(session.selectedCharacterID))
	activity, activityErr := s.currentActivityPickupPartyLocked(session, runtime, *drop)
	if activityErr != nil {
		return activityErr
	}
	destinationSlot, itemUpdate, err := grantCurrentDungeonPickupItemWithActivity(
		context.Background(),
		repositories,
		characterID,
		drop.Item,
		drop.Amount,
		s.gameplayNow(),
		activity,
		drop.QualitySeed,
	)
	if err != nil {
		s.logGameEvent(session, "game-dungeon-pickup-blocked",
			"dungeon_id", runtime.Dungeon.ID,
			"room", scene.Coordinate.String(),
			"map_id", scene.Map.Map.ID,
			"drop_object_key", drop.ObjectKey,
			"item_id", drop.Item.ItemID,
			"amount", drop.Amount,
			"reason", "character_items_transaction_failed_drop_remains_available",
			"error", err)
		if errors.Is(err, errDungeonPickupInventoryFull) {
			// 当前EXE的class1/43回调0x1fa77b0以失败码4提示满包，
			// 随后0x265a370清除地面物品的拾取中标记；缺应答会阻止重试。
			// 失败不发送class0/39成功通知，地面物品继续保留在账本中。
			return s.sendGameUpperRawClass(session, currentDungeonPickupRequestOpcode,
				[]byte{0, currentDungeonPickupInventoryFullCode}, 1)
		}
		return nil
	}
	responseBody, err := buildCurrentDungeonPickupResultForItem(
		drop.ObjectKey,
		pickerActorObjectKey,
		destinationSlot,
		drop.Item,
	)
	if err != nil {
		// The allocator returns a proved slot in the item's target container, so
		// this is an invariant failure after a committed transaction. Consume
		// the scene object to prevent a duplicate grant even if no ACK can be sent.
		drop.Status = runtimeDungeonDropConsumed
		drop.DestinationSlot = destinationSlot
		s.logGameEvent(session, "game-dungeon-pickup-response-blocked-after-commit",
			"drop_object_key", drop.ObjectKey,
			"item_id", drop.Item.ItemID,
			"destination_slot", destinationSlot,
			"reason", "pickup_response_invariant_failed_drop_consumed_to_prevent_duplicate_grant",
			"error", err)
		return nil
	}
	drop.Status = runtimeDungeonDropConsumed
	drop.DestinationSlot = destinationSlot
	drop.PickupResponseBody = append([]byte(nil), responseBody...)
	itemUpdateBody := buildCurrentItemUpdateBody(
		currentDungeonPickupListType(drop.Item),
		[]currentItemListEntry{itemUpdate},
	)
	drop.PickupItemUpdateBody = append([]byte(nil), itemUpdateBody...)
	s.logGameEvent(session, "game-dungeon-pickup-accepted",
		"dungeon_id", runtime.Dungeon.ID,
		"room", scene.Coordinate.String(),
		"map_id", scene.Map.Map.ID,
		"drop_object_key", drop.ObjectKey,
		"scene_slot", drop.SceneSlot,
		"item_id", drop.Item.ItemID,
		"amount", drop.Amount,
		"destination_slot", destinationSlot,
		"picker_actor_object_key", pickerActorObjectKey,
		"request_context", request.ObjectContext,
		"request_player_x", request.PlayerX,
		"request_player_y", request.PlayerY,
		"request_token0", request.Token0,
		"request_drop_x", request.DropX,
		"request_drop_y", request.DropY,
		"request_token1", request.Token1,
		"request_token2", request.Token2,
		"response_opcode", currentDungeonPickupResultOpcode,
		"response_body_len", len(responseBody),
		"asset_owner", "character_items_uow",
		"item_update_opcode", uint16(dnfenum.CmdPacketWalkoutPartyMember),
		"item_update_body_len", len(itemUpdateBody))
	if err := s.sendGameUpperRawClass(session, currentDungeonPickupResultOpcode, responseBody, 0); err != nil {
		return err
	}
	return s.sendGameUpperRawClass(
		session,
		uint16(dnfenum.CmdPacketWalkoutPartyMember),
		itemUpdateBody,
		0,
	)
}

func grantCurrentDungeonPickupItemWithActivity(
	ctx context.Context,
	repositories dnfrepo.Group,
	characterID string,
	definition dungeonDropItemDefinition,
	amount uint32,
	now time.Time,
	activity *dnfrepo.ActivityEvent,
	qualitySeed ...uint32,
) (uint16, currentItemListEntry, error) {
	if repositories.CharacterItems == nil || characterID == "" {
		return 0, currentItemListEntry{}, errDungeonPickupTransactionMissing
	}
	if definition.ItemID == 0 || definition.SlotStart <= 0 || definition.SlotEnd < definition.SlotStart || amount == 0 {
		return 0, currentItemListEntry{}, fmt.Errorf(
			"%w: item=%d slots=%d..%d amount=%d",
			errDungeonPickupItemInvalid,
			definition.ItemID,
			definition.SlotStart,
			definition.SlotEnd,
			amount,
		)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	var err error
	definition, err = currentPVFItemDefinitionForGrantAt(definition, now)
	if err != nil {
		return 0, currentItemListEntry{}, err
	}
	// 拾取须同时读取持久化扩容状态，否则已扩容角色也只会按基础32格判满。
	owner, err := dnfdungeon.NewOwner(repositories)
	if err != nil {
		return 0, currentItemListEntry{}, errDungeonPickupTransactionMissing
	}
	var itemUpdate currentItemListEntry
	placement := currentDungeonPickupPlacement(definition, amount, qualitySeed...)
	result, err := owner.GrantPickupItem(ctx, dnfdungeon.PickupItemCommand{
		Activity:    activity,
		CharacterID: characterID,
		Placement:   placement,
		UpdatedAt:   now,
		Finalize: func(slot int16, stack dnfrepo.ItemStack) (dnfrepo.ItemStack, error) {
			if stack.ItemID != int64(definition.ItemID) || stack.Count <= 0 {
				return dnfrepo.ItemStack{}, errDungeonPickupItemInvalid
			}
			itemUpdate = currentItemListEntryFromStack(
				placement.Definition.ListType,
				slot,
				stack,
			)
			stack.RawEntry = append([]byte(nil), itemUpdate.data[:]...)
			return stack, nil
		},
	})
	if err != nil {
		return 0, currentItemListEntry{}, mapCurrentDungeonPickupOwnerError(err)
	}
	return result.Slot, itemUpdate, nil
}

func addCurrentDungeonPickupToInventory(
	record *dnfrepo.InventoryRecord,
	definition dungeonDropItemDefinition,
	amount uint32,
	qualitySeed ...uint32,
) (uint16, error) {
	result, _, err := dnfdungeon.PlacePickupItem(
		record,
		currentDungeonPickupPlacement(definition, amount, qualitySeed...),
	)
	if err != nil {
		return 0, mapCurrentDungeonPickupOwnerError(err)
	}
	return result, nil
}

func currentDungeonPickupPlacement(
	definition dungeonDropItemDefinition,
	amount uint32,
	qualitySeed ...uint32,
) dnfdungeon.PickupItemPlacement {
	if definition.DailyDeleteItem && definition.ExpirationDate.IsZero() {
		definition.ExpirationDate = itemexpiration.NextDailyDeleteAt(time.Now().UTC())
	}
	kind := dnfdungeon.PickupItemKind(definition.Kind)
	policy := currentPVFItemPlacementPolicy(definition).ForGrant()
	placement := dnfdungeon.PickupItemPlacement{
		Definition: dnfdungeon.PickupItemDefinition{
			ItemID:          definition.ItemID,
			Kind:            kind,
			StackLimit:      definition.StackLimit,
			SlotStart:       definition.SlotStart,
			SlotEnd:         definition.SlotEnd,
			PreferQuickSlot: policy.PreferQuickSlots,
			AllowQuickSlots: policy.AllowQuickSlots,
		},
		Amount: amount,
		// 所有"把可堆叠道具放进背包"的路径都先并入同身份的既有行：判据交给
		// dnfrepo.CanMergeItemStack（仓库层唯一实现），身份键或实例到期时间冲突的行不并。
		MergeIdentity: currentPVFGrantIdentity(definition),
	}
	container := currentPVFItemTargetContainer(definition)
	if container.GuildMedal || container.GuardianGem || container.PetArtifact || container.PetBody || container.PetConsumable {
		placement.Definition.ListType = container.ListType
		placement.Definition.SlotStart = container.SlotStart
		placement.Definition.SlotEnd = container.SlotEnd
		placement.Definition.PreferQuickSlot = false
		placement.Definition.AllowQuickSlots = false
	}
	if !definition.ExpirationDate.IsZero() {
		placement.NormalizeExisting = func(stack dnfrepo.ItemStack) (dnfrepo.ItemStack, error) {
			stack, _ = applyCurrentPVFItemExpiration(stack, definition)
			return stack, nil
		}
	}
	placement.BuildNew = func(slot int16) (dnfrepo.ItemStack, error) {
		return buildCurrentDungeonPickupStack(slot, definition, amount, qualitySeed...)
	}
	return placement
}

func buildCurrentDungeonPickupStack(
	slot int16,
	definition dungeonDropItemDefinition,
	amount uint32,
	qualitySeed ...uint32,
) (dnfrepo.ItemStack, error) {
	container := currentPVFItemTargetContainer(definition)
	if slot < 0 || amount == 0 || (slot == 0 && !container.GuildMedal && !container.PetBody) {
		return dnfrepo.ItemStack{}, errDungeonPickupItemInvalid
	}
	entry, err := buildCurrentDungeonItemInstanceState(uint16(slot), definition, amount, qualitySeed...)
	if err != nil {
		return dnfrepo.ItemStack{}, err
	}
	var aikenState [5]byte
	copy(aikenState[:], entry.data[0x72:0x77])
	extra := map[string]string{
		"source":    "dungeon_pvf_drop_pickup",
		"item_kind": string(definition.Kind),
		"pvf_path":  definition.PVFPath,
	}
	if definition.StackableType != "" {
		extra["stackable_type"] = definition.StackableType
	}
	if definition.StackLimit > 0 {
		extra["stack_limit"] = strconv.FormatInt(definition.StackLimit, 10)
	}
	if definition.Durability > 0 {
		extra["durability"] = strconv.FormatUint(uint64(definition.Durability), 10)
		extra["max_durability"] = strconv.FormatUint(uint64(definition.Durability), 10)
	}
	if definition.EquipmentType != "" {
		extra["equipment_type"] = definition.EquipmentType
	}
	if currentNewPVFEquipmentSealFlag(definition) != 0 {
		extra["seal_flag"] = "1"
	}
	stack := dnfrepo.ItemStack{
		ItemID: int64(definition.ItemID),
		Count:  int64(amount),
		Extra:  extra,
	}
	if definition.DailyDeleteItem && !definition.ExpirationDate.IsZero() {
		stack.Extra[itemexpiration.DailyDeleteDeadlineKey] = strconv.FormatInt(definition.ExpirationDate.Unix(), 10)
		stack.Extra["expire_time"] = stack.Extra[itemexpiration.DailyDeleteDeadlineKey]
		stack.Extra["expire_unix"] = stack.Extra[itemexpiration.DailyDeleteDeadlineKey]
		stack.ExpireAt = definition.ExpirationDate
		if definition.Kind == dungeonDropItemEquipment {
			stack.Extra["expiration_source"] = currentPVFDailyEquipmentGrantSource
		}
	}
	if definition.Kind == dungeonDropItemEquipment {
		if err := applyCurrentEquipmentQualitySeed(&stack, binary.LittleEndian.Uint32(entry.data[6:10])); err != nil {
			return dnfrepo.ItemStack{}, err
		}
	}
	if !definition.ExpirationDate.IsZero() {
		stack, _ = applyCurrentPVFItemExpiration(stack, definition)
	}
	listType := byte(dnfrepo.MainInventoryListType)
	if container := currentPVFItemTargetContainer(definition); container.GuildMedal || container.GuardianGem || container.PetArtifact || container.PetBody || container.PetConsumable {
		listType = container.ListType
	}
	entry = currentItemListEntryFromStack(listType, slot, stack)
	stack.RawEntry = append([]byte(nil), entry.data[:]...)
	if definition.AikenInitial != nil {
		return currentAikenWriteState(stack, slot, aikenState)
	}
	return stack, nil
}

func insertCurrentDungeonPickup(
	record *dnfrepo.InventoryRecord,
	definition dungeonDropItemDefinition,
	amount uint32,
	slot int16,
	qualitySeed ...uint32,
) (uint16, error) {
	if record == nil {
		return 0, errDungeonPickupItemInvalid
	}
	stack, err := buildCurrentDungeonPickupStack(slot, definition, amount, qualitySeed...)
	if err != nil {
		return 0, err
	}
	if record.Slots == nil {
		record.Slots = make(map[string]dnfrepo.ItemStack)
	}
	record.Slots[currentDungeonPickupMainSlotKey(slot)] = stack
	return uint16(slot), nil
}

func mapCurrentDungeonPickupOwnerError(err error) error {
	switch {
	case errors.Is(err, dnfdungeon.ErrOwnerUnavailable):
		return errDungeonPickupTransactionMissing
	case errors.Is(err, dnfdungeon.ErrInventoryNotFound):
		return errDungeonPickupInventoryMissing
	case errors.Is(err, dnfdungeon.ErrPickupInventoryFull):
		return fmt.Errorf("%w: %v", errDungeonPickupInventoryFull, err)
	case errors.Is(err, dnfdungeon.ErrPickupStackLimit):
		return fmt.Errorf("%w: %v", errDungeonPickupStackLimit, err)
	case errors.Is(err, dnfdungeon.ErrPickupItemInvalid):
		return fmt.Errorf("%w: %v", errDungeonPickupItemInvalid, err)
	default:
		return err
	}
}

func currentDungeonPickupMainSlotKey(slot int16) string {
	return "0:" + strconv.FormatInt(int64(slot), 10)
}

// 原有通用发奖调用不带副本拾取来源，不计入获取统计。
func grantCurrentDungeonPickupItem(ctx context.Context, repositories dnfrepo.Group, characterID string, definition dungeonDropItemDefinition, amount uint32, now time.Time, qualitySeed ...uint32) (uint16, currentItemListEntry, error) {
	return grantCurrentDungeonPickupItemWithActivity(ctx, repositories, characterID, definition, amount, now, nil, qualitySeed...)
}

// Mirror the supported pickup placement, not every family supported by other grants.
func currentDungeonPickupListType(definition dungeonDropItemDefinition) byte {
	c := currentPVFItemTargetContainer(definition)
	if c.GuildMedal || c.GuardianGem || c.PetArtifact || c.PetBody || c.PetConsumable {
		return c.ListType
	}
	return dnfrepo.MainInventoryListType
}
