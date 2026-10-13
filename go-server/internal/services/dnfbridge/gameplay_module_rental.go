package dnfbridge

import (
	"context"
	"errors"
	"math"
	"time"

	"longheng.io/server/internal/modules/dnf/dnfenum"
)

func rentalGameplayModule() gameplayModuleDefinition {
	rentOpcode := uint16(dnfenum.CmdPacketRentEquipmentItem)
	chargeOpcode := uint16(dnfenum.CmdPacketChargeRentpoint)
	return gameplayModuleDefinition{
		Name: "equipment-rental",
		LegacyHandlers: map[uint16]gameplayHandler{
			rentOpcode: func(service *Service, session *gameSession, request gameplayRequest) error {
				return service.handleCurrentRentEquipment(session, request.Body)
			},
			chargeOpcode: func(service *Service, session *gameSession, request gameplayRequest) error {
				return service.handleCurrentChargeRentalPoint(session, request.Body)
			},
		},
		UpperHandlers: map[uint16]gameplayHandler{
			rentOpcode: func(service *Service, session *gameSession, request gameplayRequest) error {
				if !requireDefaultGameplayClassification(service, session, request, "game-upper-rental-equipment-request-blocked", "current_exe_op999_command_class_mismatch") {
					return nil
				}
				return service.handleCurrentRentEquipment(session, request.Body)
			},
			chargeOpcode: func(service *Service, session *gameSession, request gameplayRequest) error {
				if !requireDefaultGameplayClassification(service, session, request, "game-upper-rental-point-charge-request-blocked", "current_exe_op1000_command_class_mismatch") {
					return nil
				}
				return service.handleCurrentChargeRentalPoint(session, request.Body)
			},
		},
		LegacyNormalizers: map[uint16]gameplayLegacyNormalizer{
			rentOpcode: func(body []byte) []byte {
				return stripLegacyTransportTrailer(body, currentRentalRequestWireSize)
			},
			chargeOpcode: func(body []byte) []byte {
				return stripLegacyTransportTrailer(body, currentRentalRequestWireSize)
			},
		},
	}
}

func (s *Service) handleCurrentRentEquipment(session *gameSession, body []byte) error {
	rentOpcode := uint16(dnfenum.CmdPacketRentEquipmentItem)
	request, err := decodeCurrentRentEquipmentRequest(body)
	if err != nil {
		s.logGameEvent(session, "game-upper-rental-equipment-request-blocked", "body_len", len(body), "reason", err)
		// 厂商 `RentalHandler.cs:49` 在解析失败时回 `BuildFailureAck()`；我方改动前不回包。
		return s.sendCurrentRentalFailureAck(session, rentOpcode, "rent_request_parse_failed", "body_len", len(body), "detail", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	repositories, characterID, character, err := s.currentRentalSelectedCharacter(ctx, session)
	if err != nil {
		s.logGameEvent(session, "game-upper-rental-equipment-request-blocked", "item_id", request.ItemID, "reason", err)
		// 厂商 `RentalHandler.cs:58` 在拿不到 owned lease 时回 `BuildFailureAck()`。
		return s.sendCurrentRentalFailureAck(session, rentOpcode, "rent_selected_character_unavailable", "item_id", request.ItemID, "detail", err)
	}
	catalog, source, items, err := s.currentRentalSources(ctx)
	if err != nil {
		s.logGameEvent(session, "game-upper-rental-equipment-request-blocked", "item_id", request.ItemID, "reason", err)
		return s.sendCurrentRentalFailureAck(session, rentOpcode, "rent_pvf_catalog_unavailable", "item_id", request.ItemID, "detail", err)
	}
	jobValue := numericCharacterStat(character.Job)
	if jobValue < 0 || jobValue > math.MaxUint16 {
		s.logGameEvent(session, "game-upper-rental-equipment-request-blocked", "item_id", request.ItemID, "job", character.Job, "reason", "job_out_of_u16_range")
		return s.sendCurrentRentalFailureAck(session, rentOpcode, "rent_job_out_of_u16_range", "item_id", request.ItemID, "job", character.Job)
	}
	jobTag, err := currentRentalCharacterJobTag(source, byte(jobValue))
	if err != nil {
		s.logGameEvent(session, "game-upper-rental-equipment-request-blocked", "item_id", request.ItemID, "job", jobValue, "reason", err)
		return s.sendCurrentRentalFailureAck(session, rentOpcode, "rent_character_job_tag_unavailable", "item_id", request.ItemID, "job", jobValue, "detail", err)
	}
	now := time.Now().UTC()
	effectiveLevel, overEquipContract := currentRentalEffectiveLevel(
		ctx,
		repositories.Account,
		s.accountIDForSession(session),
		character.Level,
		now,
	)
	tier, ok := catalog.tierForLevel(effectiveLevel)
	if !ok {
		s.logGameEvent(session, "game-upper-rental-equipment-request-blocked",
			"item_id", request.ItemID,
			"level", character.Level,
			"effective_level", effectiveLevel,
			"over_equip_contract", overEquipContract,
			"reason", "no_pvf_tier")
		return s.sendCurrentRentalFailureAck(session, rentOpcode, "rent_no_pvf_tier",
			"item_id", request.ItemID,
			"level", character.Level,
			"effective_level", effectiveLevel,
			"over_equip_contract", overEquipContract)
	}
	expectedPacked := uint32(uint16(jobValue)) | uint32(uint16(tier))<<16
	if request.PackedJobTier != expectedPacked {
		s.logGameEvent(session, "game-upper-rental-equipment-request-blocked",
			"item_id", request.ItemID,
			"packed_job_tier", request.PackedJobTier,
			"expected_packed_job_tier", expectedPacked,
			"job", jobValue,
			"tier", tier,
			"level", character.Level,
			"effective_level", effectiveLevel,
			"over_equip_contract", overEquipContract,
			"reason", "current_exe_job_tier_key_mismatch")
		return s.sendCurrentRentalFailureAck(session, rentOpcode, "rent_job_tier_key_mismatch",
			"item_id", request.ItemID,
			"packed_job_tier", request.PackedJobTier,
			"expected_packed_job_tier", expectedPacked,
			"job", jobValue,
			"tier", tier,
			"level", character.Level,
			"effective_level", effectiveLevel,
			"over_equip_contract", overEquipContract)
	}
	pointCost, ok := catalog.itemCost(jobTag, tier, request.ItemID)
	if !ok {
		s.logGameEvent(session, "game-upper-rental-equipment-request-blocked", "item_id", request.ItemID, "job_tag", jobTag, "tier", tier, "reason", "item_not_in_current_pvf_group")
		return s.sendCurrentRentalFailureAck(session, rentOpcode, "rent_item_not_in_pvf_group", "item_id", request.ItemID, "job_tag", jobTag, "tier", tier)
	}
	definition, err := validateCurrentRentalItem(source, items, request.ItemID, jobTag)
	if err != nil {
		s.logGameEvent(session, "game-upper-rental-equipment-request-blocked", "item_id", request.ItemID, "job_tag", jobTag, "tier", tier, "reason", err)
		return s.sendCurrentRentalFailureAck(session, rentOpcode, "rent_item_validation_failed", "item_id", request.ItemID, "job_tag", jobTag, "tier", tier, "detail", err)
	}
	owner, err := currentRentalAssetOwner(repositories)
	if err != nil {
		s.logGameEvent(session, "game-upper-rental-equipment-request-blocked", "item_id", request.ItemID, "reason", err)
		return s.sendCurrentRentalFailureAck(session, rentOpcode, "rent_asset_owner_unavailable", "item_id", request.ItemID, "detail", err)
	}
	result, err := rentCurrentEquipment(ctx, owner, s.accountIDForSession(session), characterID, request.ItemID, pointCost, definition, now)
	if err != nil {
		resultCode := uint32(1)
		if errors.Is(err, errCurrentRentalInventoryFull) {
			resultCode = 2
		}
		s.logGameEvent(session, "game-upper-rental-equipment-transaction-blocked",
			"character_id", characterID,
			"item_id", request.ItemID,
			"point_cost", pointCost,
			"result_code", resultCode,
			"reason", err)
		// 厂商 `RentalHandler.cs:76/108`：只有 `CanRentWeapon` 失败（背包满）回
		// `BuildResultAck(InventoryFullResult=2)`；其余事务拒绝（幸运星不足、应用失败）
		// `failureResult` 仍是 0 ⇒ 回 1 字节 `BuildFailureAck()`。
		if currentRentalTransactionRejectUsesVendorFailureAck(currentRentalRejectAckMode(), resultCode) {
			return s.sendCurrentRentalFailureAck(session, rentOpcode, "rent_transaction_rejected_vendor_failure_ack",
				"character_id", characterID,
				"item_id", request.ItemID,
				"point_cost", pointCost,
				"legacy_result_code", resultCode,
				"detail", err)
		}
		return s.sendCurrentRentEquipmentResult(session, resultCode)
	}
	if err := s.sendCurrentRentEquipmentResult(session, 0); err != nil {
		return err
	}
	if err := s.sendSelectedCurrentRentalPointState(session, "op999_after_atomic_rent", true); err != nil {
		return err
	}
	if err := s.sendSelectedCurrentRentalInventoryItemUpdate(session, repositories.Inventory, characterID, request.ItemID, result); err != nil {
		return err
	}
	s.logGameEvent(session, "game-upper-rental-equipment-applied",
		"character_id", characterID,
		"item_id", request.ItemID,
		"job", jobValue,
		"job_tag", jobTag,
		"tier", tier,
		"point_cost", pointCost,
		"remaining_points", result.Points,
		"slot", result.Slot,
		"equipped", result.Equipped,
		"expire_unix", result.ExpireAt.Unix(),
		"pvf_path", definition.PVFPath,
		"durability", definition.Durability)
	return nil
}

func (s *Service) handleCurrentChargeRentalPoint(session *gameSession, body []byte) error {
	chargeOpcode := uint16(dnfenum.CmdPacketChargeRentpoint)
	request, err := decodeCurrentChargeRentalPointRequest(body)
	if err != nil {
		s.logGameEvent(session, "game-upper-rental-point-charge-request-blocked", "body_len", len(body), "reason", err)
		// 厂商 `LuckyStarHandler.cs:39/125-131` 在数量非法时回 1 字节 `00`；我方改动前不回包。
		return s.sendCurrentRentalFailureAck(session, chargeOpcode, "charge_request_parse_failed", "body_len", len(body), "detail", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	repositories, characterID, _, err := s.currentRentalSelectedCharacter(ctx, session)
	if err != nil {
		s.logGameEvent(session, "game-upper-rental-point-charge-request-blocked", "count", request.Count, "reason", err)
		return s.sendCurrentRentalFailureAck(session, chargeOpcode, "charge_selected_character_unavailable", "count", request.Count, "detail", err)
	}
	catalog, _, _, err := s.currentRentalSources(ctx)
	if err != nil {
		s.logGameEvent(session, "game-upper-rental-point-charge-request-blocked", "count", request.Count, "reason", err)
		return s.sendCurrentRentalFailureAck(session, chargeOpcode, "charge_pvf_catalog_unavailable", "count", request.Count, "detail", err)
	}
	owner, err := currentRentalAssetOwner(repositories)
	if err != nil {
		s.logGameEvent(session, "game-upper-rental-point-charge-request-blocked", "count", request.Count, "reason", err)
		return s.sendCurrentRentalFailureAck(session, chargeOpcode, "charge_asset_owner_unavailable", "count", request.Count, "detail", err)
	}
	result, err := purchaseCurrentRentalPoints(ctx, owner, s.accountIDForSession(session), characterID, request.Count, catalog.Limit, catalog.GoldPerPoint, time.Now().UTC())
	if err != nil {
		s.logGameEvent(session, "game-upper-rental-point-charge-transaction-blocked",
			"character_id", characterID,
			"count", request.Count,
			"point_limit", catalog.Limit,
			"gold_per_point", catalog.GoldPerPoint,
			"reason", err)
		// 厂商 `LuckyStarHandler.cs:108/125-131`：事务拒绝回 1 字节 `00`
		// （我方改动前回 `00 01` 两字节）。
		if currentRentalRejectAckVendorShapes(currentRentalRejectAckMode()) {
			return s.sendCurrentRentalFailureAck(session, chargeOpcode, "charge_transaction_rejected_vendor_failure_ack",
				"character_id", characterID,
				"count", request.Count,
				"point_limit", catalog.Limit,
				"gold_per_point", catalog.GoldPerPoint,
				"detail", err)
		}
		return s.sendGameUpperFailure(session, chargeOpcode, 1)
	}
	// 厂商 `LuckyStarClientNotifier.cs:44-68`：成功体为
	// `01 <i32 mode> <i32 quantity> <i32 totalLuckyStar>`，`mode`/`quantity` 原样回显请求。
	vendorShapes := currentRentalRejectAckVendorShapes(currentRentalRejectAckMode())
	response := buildCurrentRentalChargeSuccessBody(request.ChargeType, request.Count, result.Points, vendorShapes)
	if err := s.sendGameUpperSuccess(session, chargeOpcode, response); err != nil {
		return err
	}
	if err := s.sendSelectedCurrentRentalPointState(session, "op1000_after_atomic_charge", true); err != nil {
		return err
	}
	appliedFields := []any{
		"character_id", characterID,
		"count", request.Count,
		"new_points", result.Points,
		"remaining_gold", result.Gold,
		"gold_per_point", catalog.GoldPerPoint,
		"point_limit", catalog.Limit,
	}
	// 默认档（`missing`）**不加任何新字段**，保证开启开关前后这一条事件逐字符一致。
	if vendorShapes {
		appliedFields = append(appliedFields,
			"ack_body_source", "vendor_network_handlers_lucky_star_client_notifier_build_charge_rent_point_success_body",
			"ack_echoes_request_quantity", true,
			"reject_ack_mode", currentRentalRejectAckMode())
	}
	s.logGameEvent(session, "game-upper-rental-point-charge-applied", appliedFields...)
	return nil
}
