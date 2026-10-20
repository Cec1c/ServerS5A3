// Package inventory routes current-EXE inventory commands to repository-backed
// owners. Mutations return success only after the required transaction and
// authoritative ACK/refresh sequence have been constructed.
package inventory

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfequip "longheng.io/server/internal/modules/dnf/equip"
	dnfguardianshield "longheng.io/server/internal/modules/dnf/guardianshield"
	"longheng.io/server/internal/modules/dnf/premium"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrevivecoin "longheng.io/server/internal/modules/dnf/revivecoin"
)

type handler struct{}

// NewHandler 创建背包协议处理器。
func NewHandler() alignedcmd.Handler {
	return handler{}
}

// Domain 返回背包业务域。
func (handler) Domain() dnfenum.AlignedDomain {
	return dnfenum.AlignedDomainInventory
}

// Handle 解析已经对齐协议号的背包请求，并在关键写路径闭合前阻止 bridge 伪造成功 ACK。
func (handler) Handle(ctx context.Context, req alignedcmd.Request) (alignedcmd.Result, error) {
	switch dnfenum.CmdPacket(req.Opcode) {
	case dnfenum.CmdPacketDeleteItem:
		parsed, err := DecodeDeleteRequest(req.Body)
		cmd := NewDeleteCommand(req, parsed)
		if err != nil {
			return blockedParsedResult(cmd.Operation, cmd.String(), err), nil
		}
		if err := checkDeleteListType(cmd.SourceListType); err != nil {
			return ownerBlockedResult(cmd, err), nil
		}
		owner, err := NewOwner(req.Repositories)
		if err != nil {
			return ownerBlockedResult(cmd, err), nil
		}
		result, err := owner.Delete(ctx, cmd)
		if err != nil {
			return ownerBlockedResult(cmd, err), nil
		}
		return ownerAppliedDeleteResult(req.Opcode, cmd, result), nil
	case dnfenum.CmdPacketMoveItemspace:
		parsed, err := DecodeMoveItemspaceRequest(req.Body)
		cmd := NewMoveCommand(req, parsed)
		if err != nil {
			return blockedParsedResult(cmd.Operation, cmd.String(), err), nil
		}
		if dnfguardianshield.IsWireList(cmd.SourceListType) || dnfguardianshield.IsWireList(cmd.DestinationListType) {
			if cmd.MoveCount != 0 || cmd.SourceInstanceValue < 0 || cmd.DestinationInstance != 0 || cmd.DestinationStack != 0 ||
				(cmd.SourceListType == dnfguardianshield.WireListEquipped && cmd.SourceInstanceValue != 0) ||
				(cmd.SourceListType == dnfguardianshield.WireListCatalog && cmd.SourceInstanceValue == 0) {
				return ownerRejectedGuardianShieldMoveResult(req.Opcode, cmd, fmt.Errorf("invalid current guardian shield op19 scalar fields")), nil
			}
			if req.GuardianShield == nil {
				return ownerRejectedGuardianShieldMoveResult(req.Opcode, cmd, fmt.Errorf("guardian shield PVF resolver is unavailable")), nil
			}
			owner, ownerErr := dnfguardianshield.NewOwner(req.Repositories, req.GuardianShield)
			if ownerErr != nil {
				return ownerRejectedGuardianShieldMoveResult(req.Opcode, cmd, ownerErr), nil
			}
			result, ownerErr := owner.Move(ctx, dnfguardianshield.MoveCommand{
				CharacterID:          req.SelectedCharacterID,
				AccountID:            req.AccountID,
				SourceListType:       cmd.SourceListType,
				SourceSlotIndex:      cmd.SourceSlotIndex,
				SourceShieldItemID:   uint32(cmd.SourceInstanceValue),
				DestinationListType:  cmd.DestinationListType,
				DestinationSlotIndex: cmd.DestinationSlotIndex,
			})
			if ownerErr != nil {
				return ownerRejectedGuardianShieldMoveResult(req.Opcode, cmd, ownerErr), nil
			}
			return ownerAppliedGuardianShieldMoveResult(req.Opcode, cmd, result), nil
		}
		petEquipmentMove := isProvenPetEquipmentMove(cmd)
		if petEquipmentMove {
			if err := validateProvenPetEquipmentMove(cmd); err != nil {
				return ownerBlockedResult(cmd, err), nil
			}
			var owner *dnfequip.Owner
			if cmd.DestinationSlotIndex == 26 {
				owner, err = dnfequip.NewOwner(req.Repositories)
			} else if req.EquipmentPlacement == nil {
				return ownerBlockedResult(cmd, fmt.Errorf("pet artifact move requires the current PVF placement validator")), nil
			} else {
				owner, err = dnfequip.NewOwnerWithPlacementValidator(req.Repositories, dnfequip.PlacementValidatorFunc(func(ctx context.Context, placement dnfequip.Placement) error {
					return req.EquipmentPlacement(ctx, alignedcmd.EquipmentPlacementRequest{
						CharacterID:     placement.CharacterID,
						ItemID:          placement.ItemID,
						SourceListType:  placement.SourceListType,
						SourceSlotIndex: placement.SourceSlotIndex,
						TargetSlotIndex: placement.TargetSlotIndex,
					})
				}))
			}
			if err != nil {
				return ownerBlockedResult(cmd, err), nil
			}
			ownerCommand := normalizedEquipmentMoveCommand(cmd)
			// Current NoPack uses actor-worn endpoint 3 or 17 on the wire.
			// Both resolve to the same durable EquipmentRecord. Keep the raw
			// endpoint in cmd for request evidence while the ACK reports the
			// owner's committed list-3 endpoint.
			result, err := owner.Move(ctx, ownerCommand)
			if err != nil {
				return ownerBlockedResult(cmd, err), nil
			}
			return ownerAppliedPetEquipmentMoveResult(req.Opcode, cmd, result), nil
		}
		if (cmd.SourceListType == listTypePet || cmd.DestinationListType == listTypePet) &&
			!(cmd.SourceListType == listTypePet && cmd.DestinationListType == listTypePet) {
			return blockedParsedResult(cmd.Operation, cmd.String(), fmt.Errorf("unsupported current-EXE pet move endpoints")), nil
		}
		if isActorWornEndpoint(cmd.SourceListType) || isActorWornEndpoint(cmd.DestinationListType) {
			var owner *dnfequip.Owner
			if req.EquipmentPlacement == nil {
				return ownerBlockedResult(cmd, fmt.Errorf("normal equipment move requires the current PVF placement validator")), nil
			} else {
				owner, err = dnfequip.NewOwnerWithPlacementValidator(req.Repositories, dnfequip.PlacementValidatorFunc(func(ctx context.Context, placement dnfequip.Placement) error {
					return req.EquipmentPlacement(ctx, alignedcmd.EquipmentPlacementRequest{
						CharacterID:     placement.CharacterID,
						ItemID:          placement.ItemID,
						SourceListType:  placement.SourceListType,
						SourceSlotIndex: placement.SourceSlotIndex,
						TargetSlotIndex: placement.TargetSlotIndex,
					})
				}))
			}
			if err != nil {
				return ownerBlockedResult(cmd, err), nil
			}
			owner.SetNameTagChecker(req.NameTagChecker)
			// 克隆装扮外观权威（运行时 PVF）；为零时 equip 域一个字节都不多写。
			owner.SetAvatarClassifier(req.AvatarClassifier)
			ownerCommand := normalizedEquipmentMoveCommand(cmd)
			// 登录引导窗口标记只透传给穿戴 owner；是否生效由
			// DNFBRIDGE_EQUIP_LOGIN_REASSERT_NOOP（默认关闭）决定。
			ownerCommand.LoginWearWindowOpen = req.LoginWearWindowOpen
			// 同槽重试计数（登录纪元内第几条声明）：≥2 时域侧两条登录闸门惰性
			// ⇒ 反转端点兜底把它执行成真·unequip。见 dnfbridge/equip_worn_slot_claim_occurrence.go。
			ownerCommand.WornSlotClaimOccurrence = req.WornSlotClaimOccurrence
			// 甲：镜像回穿（我们刚把那件脱下、客户端随即又穿回去）⇒ 域侧判 ClientEcho noop。
			ownerCommand.MirrorReequipEcho = req.MirrorReequipEcho
			// 玩家请求标志位（op19 尾字节）：域侧据此绕过复述门禁。
			ownerCommand.PlayerRequestFlag = req.PlayerRequestFlag
			result, err := owner.Move(ctx, ownerCommand)
			if err != nil {
				return ownerBlockedResult(cmd, err), nil
			}
			response := ownerAppliedEquipmentMoveResult(req.Opcode, cmd, result, req.EquipAppearanceSlotGateOff, req.LoginWearReassertNoopRefresh)
			if req.EquipmentInventorySlotRefresh && result.Changed {
				response.ItemSlotRefreshes = equipmentMoveInventorySlotRefreshes(result)
			}
			return response, nil
		}
		owner, err := NewOwner(req.Repositories)
		if err != nil {
			return ownerBlockedResult(cmd, err), nil
		}
		result, err := owner.Move(ctx, cmd)
		if err != nil {
			return ownerRejectedMoveResult(req.Opcode, cmd, err), nil
		}
		return ownerAppliedMoveResult(ctx, req.Repositories.Settings, req.Opcode, cmd, result), nil
	case dnfenum.CmdPacketRequestItemSortLock, dnfenum.CmdPacketRequestItemSortUnlock:
		// DOC-067 位置锁：713=置1、714=置0（不是 toggle）。请求体 u8 list + u16 slot；
		// 成功回同一 opcode 的 class1 `1 + list + slot`，失败不回包（错误码枚举未证，不臆造）。
		parsed, err := DecodeSortLockRequest(req.Body)
		locked := dnfenum.CmdPacket(req.Opcode) == dnfenum.CmdPacketRequestItemSortLock
		cmd := NewSortLockCommand(req, parsed, locked)
		if err != nil {
			return blockedParsedResult(cmd.Operation, cmd.String(), err), nil
		}
		owner, err := NewOwner(req.Repositories)
		if err != nil {
			return ownerBlockedResult(cmd, err), nil
		}
		result, err := owner.ApplySortLock(ctx, cmd)
		if err != nil {
			return ownerBlockedResult(cmd, err), nil
		}
		return ownerAppliedSortLockResult(req.Opcode, cmd, result), nil
	case dnfenum.CmdPacketChangeDeckInfo:
		deck, err := decodeCurrentGuardianShieldDeckRequest(req.Body)
		if err != nil {
			return rejectedGuardianShieldDeckResult(req.Opcode, err), nil
		}
		if req.GuardianShield == nil {
			return rejectedGuardianShieldDeckResult(req.Opcode, fmt.Errorf("guardian shield PVF resolver is unavailable")), nil
		}
		owner, err := dnfguardianshield.NewOwner(req.Repositories, req.GuardianShield)
		if err != nil {
			return rejectedGuardianShieldDeckResult(req.Opcode, err), nil
		}
		result, err := owner.SaveDeck(ctx, req.SelectedCharacterID, req.AccountID, deck)
		if err != nil {
			return rejectedGuardianShieldDeckResult(req.Opcode, err), nil
		}
		return appliedGuardianShieldDeckResult(req.Opcode, result), nil
	case dnfenum.CmdPacketSortItem:
		parsed, err := DecodeSortItemRequest(req.Body)
		cmd := NewSortCommand(req, parsed)
		if err != nil {
			return blockedParsedResult(cmd.Operation, cmd.String(), err), nil
		}
		owner, err := NewOwner(req.Repositories)
		if err != nil {
			return ownerBlockedResult(cmd, err), nil
		}
		// Read the authoritative wallet BEFORE owner.Sort persists anything: a read
		// failure must reject the request with no inventory movement and no ACK.
		// The sort itself never writes the Character gold stat, so the prepared
		// balance stays authoritative for the response built after the commit.
		gold, err := loadSortRefreshGold(ctx, req.Repositories.Character, cmd.SourceListType, strconv.FormatUint(uint64(cmd.SelectedCharacterID), 10))
		if err != nil {
			return ownerBlockedResult(cmd, err), nil
		}
		result, err := owner.Sort(ctx, cmd)
		if err != nil {
			return ownerBlockedResult(cmd, err), nil
		}
		return ownerAppliedSortResult(ctx, req.Repositories.Settings, req.Opcode, cmd, applySortRefreshGold(result, gold)), nil
	case dnfenum.CmdPacketBuyItem:
		parsed, err := DecodeBuyItemRequest(req.Body)
		cmd := NewBuyCommand(req, parsed)
		return blockedParsedResult(cmd.Operation, cmd.String(), err), nil
	case dnfenum.CmdPacketSellItem:
		parsed, err := DecodeDeleteOrSellRequest(req.Body)
		cmd := NewSellCommand(req, parsed)
		if err != nil {
			return blockedParsedResult(cmd.Operation, cmd.String(), err), nil
		}
		owner, err := NewOwner(req.Repositories)
		if err != nil {
			return ownerBlockedResult(cmd, err), nil
		}
		result, err := owner.Sell(ctx, cmd)
		if err != nil {
			return ownerBlockedResult(cmd, err), nil
		}
		return ownerAppliedSellResult(req.Opcode, cmd, result), nil
	case dnfenum.CmdPacketRepairEquipment:
		parsed, err := DecodeRepairEquipmentRequest(req.Body)
		cmd := NewRepairCommand(req, parsed)
		if err != nil {
			return blockedParsedResult(cmd.Operation, cmd.String(), err), nil
		}
		if cmd.SourceSlotIndex == -1 {
			// 全部修理 (86JP TryRepairAll): 穿戴装备 + 主列表快捷栏 slot 3..8,
			// 合计费用一次扣除, ACK 槽位 0xFFFF, 客户端本地拉满。
			if cmd.SourceListType != listTypeEquipment && cmd.SourceListType != listTypeMain {
				return ownerBlockedResult(cmd, fmt.Errorf("%w: %d", ErrUnsupportedList, cmd.SourceListType)), nil
			}
			owner, err := dnfequip.NewOwner(req.Repositories)
			if err != nil {
				return ownerBlockedResult(cmd, err), nil
			}
			result, err := owner.RepairAll(ctx, dnfequip.RepairCommand{
				SelectedCharacterID: cmd.SelectedCharacterID,
				AccountID:           req.AccountID,
				SlotIndex:           cmd.SourceSlotIndex,
				QuickRepair:         cmd.QuickRepair,
				AutoRepair:          cmd.AutoRepair,
			}, req.RepairCostResolver)
			if err != nil {
				return ownerBlockedResult(cmd, err), nil
			}
			return ownerAppliedRepairAllResult(req.Opcode, cmd, result), nil
		}
		if cmd.SourceListType == listTypeEquipment {
			owner, err := dnfequip.NewOwner(req.Repositories)
			if err != nil {
				return ownerBlockedResult(cmd, err), nil
			}
			result, err := owner.Repair(ctx, dnfequip.RepairCommand{
				SelectedCharacterID: cmd.SelectedCharacterID,
				AccountID:           req.AccountID,
				SlotIndex:           cmd.SourceSlotIndex,
				QuickRepair:         cmd.QuickRepair,
				AutoRepair:          cmd.AutoRepair,
			}, req.RepairCostResolver)
			if err != nil {
				return ownerBlockedResult(cmd, err), nil
			}
			return ownerAppliedEquippedRepairResult(req.Opcode, cmd, result), nil
		}
		owner, err := NewOwner(req.Repositories)
		if err != nil {
			return ownerBlockedResult(cmd, err), nil
		}
		result, err := owner.Repair(ctx, cmd, req.RepairCostResolver)
		if err != nil {
			return ownerBlockedResult(cmd, err), nil
		}
		return ownerAppliedRepairResult(req.Opcode, cmd, result), nil
	case dnfenum.CmdPacketDisjointItem:
		parsed, err := DecodeDisjointItemRequest(req.Body)
		cmd := NewDisjointCommand(req, parsed)
		return blockedParsedResult(cmd.Operation, cmd.String(), err), nil
	case dnfenum.CmdPacketUseStackable:
		parsed, err := DecodeUseStackableRequest(req.Body)
		cmd := NewUseStackableCommand(req, parsed)
		if err != nil {
			return blockedParsedResult(cmd.Operation, cmd.String(), err), nil
		}
		if err := checkUseStackableListType(cmd.SourceListType); err != nil {
			return ownerBlockedResult(cmd, err), nil
		}
		if req.ExperienceBuffResolver != nil {
			effect, err := req.ExperienceBuffResolver(int64(cmd.ItemTemplateID))
			if err != nil {
				return ownerBlockedResult(cmd, err), nil
			}
			if effect.Kind != "" {
				cmd.UseExperienceBuff = &effect
				cmd.ExperienceBuffNow = req.ExperienceBuffNow
			}
		}
		if err := applyUpgradeRateBuffResolution(req, &cmd); err != nil {
			return ownerBlockedResult(cmd, err), nil
		}
		owner, err := NewOwner(req.Repositories)
		if err != nil {
			return ownerBlockedResult(cmd, err), nil
		}
		result, err := owner.UseStackable(ctx, cmd, req.PremiumContractResolver, req.RandomRewardItemResolver)
		if err != nil {
			return ownerBlockedResult(cmd, err), nil
		}
		return ownerAppliedUseStackableResult(req.Opcode, cmd, result), nil
	case dnfenum.CmdPacketUseSharedEffectItem:
		// 爆竹族（[random reward item] 且带共享特效）在当前客户端走 op251，而不是 op44：
		// 线上现场 body = u32 itemCode + u16 slot。此前没有任何 handler，服务端一个包都不回，
		// 玩家表现就是"右键没消耗、没特效，背包只能看不能点"。这里复用 op44 的 owner 路径。
		parsed, err := DecodeUseSharedEffectItemRequest(req.Body)
		cmd := NewUseSharedEffectItemCommand(req, parsed)
		if err != nil {
			return blockedParsedResult(cmd.Operation, cmd.String(), err), nil
		}
		if err := checkUseStackableListType(cmd.SourceListType); err != nil {
			return ownerBlockedResult(cmd, err), nil
		}
		owner, err := NewOwner(req.Repositories)
		if err != nil {
			return ownerBlockedResult(cmd, err), nil
		}
		result, err := owner.UseStackable(ctx, cmd, req.PremiumContractResolver, req.RandomRewardItemResolver)
		if err != nil {
			return ownerBlockedResult(cmd, err), nil
		}
		return ownerAppliedUseStackableResult(req.Opcode, cmd, result), nil
	case dnfenum.CmdPacketUseStackableAction:
		parsed, err := DecodeUseStackableActionRequest(req.Body)
		cmd := NewUseStackableActionCommand(req, parsed)
		if err != nil {
			return blockedParsedResult(cmd.Operation, parsed.String(), err), nil
		}
		owner, err := NewOwner(req.Repositories)
		if err != nil {
			return ownerBlockedResult(cmd, err), nil
		}
		result, err := owner.UnlockDamageFont(ctx, cmd, req.DamageFontResolver, req.DamageFontNow)
		if err != nil {
			return ownerBlockedResult(cmd, err), nil
		}
		return ownerAppliedDamageFontUnlockResult(req.Opcode, cmd, result), nil
	case dnfenum.CmdPacketSelectDamageFontSkin:
		parsed, err := DecodeSelectDamageFontRequest(req.Body)
		cmd := NewSelectDamageFontCommand(req, parsed)
		if err != nil {
			return alignedcmd.Result{
				Handled:         true,
				ResponseAllowed: true,
				Operation:       cmd.Operation,
				Reason:          fmt.Sprintf("select damage font parse failed: %v", err),
				UpperResponses: []alignedcmd.UpperResponse{
					class1Response(req.Opcode, buildSelectDamageFontErrorAck(damageFontSelectionErrorUnavailable)),
				},
			}, nil
		}
		owner, err := NewOwner(req.Repositories)
		if err != nil {
			return ownerBlockedResult(cmd, err), nil
		}
		result, err := owner.SelectDamageFont(ctx, cmd, req.DamageFontNow)
		if err != nil {
			return ownerBlockedResult(cmd, err), nil
		}
		return ownerAppliedDamageFontSelectionResult(req.Opcode, cmd, result), nil
	case dnfenum.CmdPacketUpgradeItem:
		// v150's separate flag sender writes u8 mode, u8 list38. Keep the
		// ordinary decoder/owner unchanged for every other request.
		if len(req.Body) >= 2 && req.Body[1] == 38 {
			return handleGuildMedalUpgrade(ctx, req), nil
		}
		parsed, err := DecodeUpgradeItemRequest(req.Body)
		cmd := NewUpgradeCommand(req, parsed)
		if err != nil {
			return alignedcmd.Result{
				Handled:         true,
				ResponseAllowed: true,
				Operation:       cmd.Operation,
				Reason:          fmt.Sprintf("upgrade_item parse failed: %v; returned current-EXE op50 error ACK", err),
				UpperResponses: []alignedcmd.UpperResponse{
					class1Response(req.Opcode, buildUpgradeItemErrorAck(upgradeErrorInvalidTarget)),
				},
			}, nil
		}
		if req.UpgradeTicketResolver != nil && !upgradeMaterialSlotIsAccountShared(cmd) {
			ticketOwner, ownerErr := NewOwner(req.Repositories)
			if ownerErr != nil {
				return ownerBlockedResult(cmd, ownerErr), nil
			}
			ticketResult, ticketErr := ticketOwner.UpgradeTicket(ctx, cmd, req.UpgradeTicketResolver)
			if ticketErr != nil {
				return ownerBlockedResult(cmd, ticketErr), nil
			}
			if ticketResult.TicketResolved {
				return ownerAppliedUpgradeTicketResult(ctx, req.Repositories.Settings, req.Opcode, cmd, ticketResult), nil
			}
			// 材料槽里是便携强化/增幅消耗品（一次性增幅器 / 凯丽的强化器）：它不是券，
			// 不在这里消耗；把身份挂到 cmd 上，请求继续走 Upgrade 的 portable 档
			// （表材料/金币被替代、失败可摧毁、保护券生效）。legacy 档下
			// `upgradePortableConsumableEnabled()` 为 false，cmd 一个字段都不改，
			// 请求逐字节回到改动前的行为（174 / 22）。见 upgrade_portable_consumable.go。
			if portable := ticketResult.Portable; portable != nil && upgradePortableConsumableEnabled() {
				cmd.UpgradePortable = portable
			}
		}
		owner, err := NewOwner(req.Repositories)
		if err != nil {
			return ownerBlockedResult(cmd, err), nil
		}
		cmd.UpgradePolicyRequired = true
		policyLevel, levelErr := upgradeLevelOfSlot(req.Repositories, ctx, cmd)
		cmd.UpgradePolicyCurrentLevel = policyLevel
		if req.UpgradePolicyResolver == nil {
			cmd.UpgradePolicyError = "upgrade policy resolver missing"
		} else if levelErr != nil {
			cmd.UpgradePolicyError = levelErr.Error()
		} else if policy, policyErr := req.UpgradePolicyResolver(cmd.Mode, int64(cmd.TargetItemTemplateID), int(policyLevel)); policyErr == nil {
			cmd.UpgradeSuccessWeight = policy.SuccessWeight
			cmd.UpgradePenaltyType = policy.PenaltyType
			cmd.UpgradePenaltyValue = policy.PenaltyValue
			cmd.UpgradeMaterialItemID = policy.MaterialItemID
			cmd.UpgradeMaterialCount = policy.MaterialCount
			cmd.UpgradeDestroyBonusItemID = policy.DestroyBonusItemID
			cmd.UpgradeDestroyBonusCount = policy.DestroyBonusCount
			cmd.UpgradeDestroyBonusIdentity = policy.DestroyBonusIdentity
			cmd.UpgradeDestroyBonusContainer = policy.DestroyBonusContainer
			cmd.UpgradeNoticeLevel = policy.NoticeLevel
		} else {
			cmd.UpgradePolicyError = policyErr.Error()
		}
		result, err := owner.Upgrade(ctx, cmd)
		if err != nil {
			var quoteErr *upgradeCostResolutionError
			if errors.As(err, &quoteErr) {
				rejected := ownerAppliedUpgradeResult(ctx, req.Repositories.Settings, req.Opcode, cmd, UpgradeResult{ErrorCode: upgradeErrorInvalidMaterial})
				rejected.Reason += "; " + err.Error()
				return rejected, nil
			}
			return ownerBlockedResult(cmd, err), nil
		}
		return ownerAppliedUpgradeResult(ctx, req.Repositories.Settings, req.Opcode, cmd, result), nil
	case dnfenum.CmdPacketUpgradeItemSeparate:
		return handlerSeparateUpgrade(ctx, req)
	case dnfenum.CmdPacketEnchantByBead:
		parsed, err := DecodeEnchantByBeadRequest(req.Body)
		cmd := NewEnchantCommand(req, parsed)
		if err != nil {
			return alignedcmd.Result{
				Handled:         true,
				ResponseAllowed: true,
				Operation:       cmd.Operation,
				Reason:          fmt.Sprintf("enchant_by_bead parse failed: %v; returned current-EXE 0x0110 error ACK", err),
				UpperResponses: []alignedcmd.UpperResponse{
					class1Response(req.Opcode, buildEnchantByBeadErrorAck(enchantErrorInvalidBead)),
				},
			}, nil
		}
		if req.EnchantBeadResolver == nil {
			return alignedcmd.Result{
				Handled:         true,
				ResponseAllowed: false,
				Operation:       cmd.Operation,
				Reason:          fmt.Sprintf("enchant_by_bead blocked: runtime-PVF resolver unavailable; refusing to trust request metadata: %v", ErrEnchantResolverRequired),
			}, nil
		}
		owner, err := NewOwner(req.Repositories)
		if err != nil {
			return ownerBlockedResult(cmd, err), nil
		}
		result, err := owner.Enchant(ctx, cmd, req.EnchantBeadResolver)
		if err != nil {
			return ownerBlockedResult(cmd, err), nil
		}
		return ownerAppliedEnchantResult(ctx, req.Repositories.Settings, req.Opcode, cmd, result), nil
	case dnfenum.CmdPacketPurifyItem:
		parsed, err := DecodePurifyItemRequest(req.Body)
		cmd := NewPurifyItemCommand(req, parsed)
		if err != nil {
			return alignedcmd.Result{
				Handled:         true,
				ResponseAllowed: true,
				Operation:       cmd.Operation,
				Reason:          fmt.Sprintf("purify_item parse failed: %v; returned current-EXE op204 fixed failure ACK", err),
				UpperResponses: []alignedcmd.UpperResponse{
					class1Response(req.Opcode, buildPurifyItemErrorAck()),
				},
			}, nil
		}
		if req.AmplifyItemResolver == nil {
			return alignedcmd.Result{
				Handled:         true,
				ResponseAllowed: true,
				Operation:       cmd.Operation,
				Reason:          fmt.Sprintf("purify_item blocked: runtime-PVF resolver unavailable: %v", ErrAmplifyItemResolverRequired),
				UpperResponses: []alignedcmd.UpperResponse{
					class1Response(req.Opcode, buildPurifyItemErrorAck()),
				},
			}, nil
		}
		owner, err := NewOwner(req.Repositories)
		if err != nil {
			return ownerBlockedResult(cmd, err), nil
		}
		result, err := owner.PurifyAmplifyItem(ctx, cmd, req.AmplifyItemResolver)
		if err != nil {
			return ownerBlockedResult(cmd, err), nil
		}
		return ownerAppliedPurifyItemResult(req.Opcode, cmd, result), nil
	case dnfenum.CmdPacketInvestItemAmplifyOption:
		parsed, err := DecodeInvestItemAmplifyOptionRequest(req.Body)
		cmd := NewInvestItemAmplifyOptionCommand(req, parsed)
		if err != nil {
			return alignedcmd.Result{
				Handled:         true,
				ResponseAllowed: true,
				Operation:       cmd.Operation,
				Reason:          fmt.Sprintf("invest_item_amplify_option parse failed: %v; returned current-EXE op205 error ACK", err),
				UpperResponses: []alignedcmd.UpperResponse{
					class1Response(req.Opcode, buildInvestItemAmplifyOptionErrorAck(investAmplifyErrorInvalid)),
				},
			}, nil
		}
		if req.AmplifyItemResolver == nil {
			return alignedcmd.Result{
				Handled:         true,
				ResponseAllowed: true,
				Operation:       cmd.Operation,
				Reason:          fmt.Sprintf("invest_item_amplify_option blocked: runtime-PVF resolver unavailable: %v", ErrAmplifyItemResolverRequired),
				UpperResponses: []alignedcmd.UpperResponse{
					class1Response(req.Opcode, buildInvestItemAmplifyOptionErrorAck(investAmplifyErrorInvalid)),
				},
			}, nil
		}
		owner, err := NewOwner(req.Repositories)
		if err != nil {
			return ownerBlockedResult(cmd, err), nil
		}
		result, err := owner.InvestAmplifyOption(ctx, cmd, req.AmplifyItemResolver)
		if err != nil {
			return ownerBlockedResult(cmd, err), nil
		}
		return ownerAppliedInvestAmplifyOptionResult(req.Opcode, cmd, result), nil
	case dnfenum.CmdPacketUnsealRandomOption:
		parsed, err := DecodeUnsealRandomOptionRequest(req.Body)
		cmd := NewUnsealRandomOptionCommand(req, parsed)
		if err != nil || req.RandomOptionResolver == nil {
			return randomOptionFailureResult(req.Opcode, cmd, fmt.Errorf("parse=%v resolver_available=%t", err, req.RandomOptionResolver != nil)), nil
		}
		owner, err := NewOwner(req.Repositories)
		if err != nil {
			return randomOptionFailureResult(req.Opcode, cmd, err), nil
		}
		result, err := owner.UnsealRandomOption(ctx, cmd, req.RandomOptionResolver)
		if err != nil {
			return randomOptionFailureResult(req.Opcode, cmd, err), nil
		}
		return ownerAppliedRandomOptionResult(req.Opcode, cmd, result), nil
	case dnfenum.CmdPacketChangeRandomOption:
		parsed, err := DecodeChangeRandomOptionRequest(req.Body)
		cmd := NewChangeRandomOptionCommand(req, parsed)
		if err != nil || req.RandomOptionResolver == nil {
			return randomOptionFailureResult(req.Opcode, cmd, fmt.Errorf("parse=%v resolver_available=%t", err, req.RandomOptionResolver != nil)), nil
		}
		owner, err := NewOwner(req.Repositories)
		if err != nil {
			return randomOptionFailureResult(req.Opcode, cmd, err), nil
		}
		result, err := owner.ChangeRandomOption(ctx, cmd, req.RandomOptionResolver)
		if err != nil {
			return randomOptionFailureResult(req.Opcode, cmd, err), nil
		}
		return ownerAppliedRandomOptionResult(req.Opcode, cmd, result), nil
	default:
		return alignedcmd.Result{
			Handled:         false,
			ResponseAllowed: false,
			Operation:       "unregistered",
			Reason:          fmt.Sprintf("inventory 模块未登记 opcode %d", req.Opcode),
		}, nil
	}
}

func randomOptionFailureResult(opcode uint16, cmd Command, err error) alignedcmd.Result {
	return alignedcmd.Result{
		Handled:         true,
		ResponseAllowed: true,
		Operation:       cmd.Operation,
		Reason:          fmt.Sprintf("random-option request rejected: %s error=%v", cmd.String(), err),
		UpperResponses: []alignedcmd.UpperResponse{
			class1Response(opcode, buildRandomOptionStatusAck(false)),
		},
	}
}

func ownerAppliedRandomOptionResult(opcode uint16, cmd Command, result RandomOptionMutationResult) alignedcmd.Result {
	if !result.Success {
		return randomOptionFailureResult(opcode, cmd, fmt.Errorf("owner rejected target item=%d", result.TargetItemID))
	}
	return alignedcmd.Result{
		Handled:         true,
		ResponseAllowed: true,
		Operation:       cmd.Operation,
		Reason:          fmt.Sprintf("random-option owner applied: %s item=%d options=%d gold_cost=%d", cmd.String(), result.TargetItemID, len(result.Options), result.GoldCost),
		UpperResponses: []alignedcmd.UpperResponse{
			class0Response(msgItemListUpdate, buildCommonItemListUpdateBody(listTypeMain, []commonItemListEntry{{slot: result.TargetSlotIndex, stack: result.UpdatedStack}})),
			class1Response(opcode, buildRandomOptionStatusAck(true)),
		},
		PostActions: []alignedcmd.PostAction{alignedcmd.PostActionRefreshSelectedItemContainers},
	}
}

func ownerAppliedPurifyItemResult(opcode uint16, cmd Command, result AmplifyMutationResult) alignedcmd.Result {
	if !result.Success {
		return alignedcmd.Result{
			Handled:         true,
			ResponseAllowed: true,
			Operation:       cmd.Operation,
			Reason:          fmt.Sprintf("inventory owner rejected: %s internal_error=%d; returned current-EXE op204 fixed failure ACK", cmd.String(), result.ErrorCode),
			UpperResponses: []alignedcmd.UpperResponse{
				class1Response(opcode, buildPurifyItemErrorAck()),
			},
		}
	}
	return alignedcmd.Result{
		Handled:         true,
		ResponseAllowed: true,
		Operation:       cmd.Operation,
		Reason: fmt.Sprintf(
			"inventory owner applied: %s mode=%s amplify=(%d,%d) materialLeft=%d; returned self-contained current-EXE op204 ACK without unproven item-list replay",
			cmd.String(), result.Mode, result.AmplifyType, result.AmplifyValue, result.MaterialRemainingCount,
		),
		UpperResponses: []alignedcmd.UpperResponse{
			class1Response(opcode, buildPurifyItemSuccessAck(result)),
		},
	}
}

func ownerAppliedInvestAmplifyOptionResult(opcode uint16, cmd Command, result AmplifyMutationResult) alignedcmd.Result {
	if !result.Success {
		return alignedcmd.Result{
			Handled:         true,
			ResponseAllowed: true,
			Operation:       cmd.Operation,
			Reason:          fmt.Sprintf("inventory owner rejected: %s error=%d; returned current-EXE op205 error ACK", cmd.String(), result.ErrorCode),
			UpperResponses: []alignedcmd.UpperResponse{
				class1Response(opcode, buildInvestItemAmplifyOptionErrorAck(result.ErrorCode)),
			},
		}
	}
	return alignedcmd.Result{
		Handled:         true,
		ResponseAllowed: true,
		Operation:       cmd.Operation,
		Reason: fmt.Sprintf(
			"inventory owner applied: %s mode=%s amplify=(%d,%d) level=%d materialLeft=%d; returned self-contained current-EXE op205 ACK without item-list replay",
			cmd.String(), result.Mode, result.AmplifyType, result.AmplifyValue, result.AmplifyLevel, result.MaterialRemainingCount,
		),
		UpperResponses: []alignedcmd.UpperResponse{
			class1Response(opcode, buildInvestItemAmplifyOptionSuccessAck(result)),
		},
	}
}

// upgradeLevelOfSlot reads the current upgrade level of the target equipment
// from the inventory record so the policy resolver can look up the correct
// PVF table row before the atomic transaction begins. The owner rechecks the
// quoted level inside the transaction before spending any assets.
func upgradeLevelOfSlot(repos dnfrepo.Group, ctx context.Context, cmd Command) (byte, error) {
	if repos.Inventory == nil || cmd.SelectedCharacterID == 0 {
		return 0, ErrInventoryNotFound
	}
	characterID := strconv.FormatUint(uint64(cmd.SelectedCharacterID), 10)
	record, found, err := repos.Inventory.Load(ctx, characterID)
	if err != nil {
		return 0, err
	}
	if !found || record.Slots == nil {
		return 0, ErrInventoryNotFound
	}
	key := slotKey(listTypeMain, cmd.TargetSlotIndex)
	stack, ok := record.Slots[key]
	if !ok || stack.ItemID != int64(cmd.TargetItemTemplateID) {
		return 0, fmt.Errorf("upgrade policy target slot or template changed")
	}
	return upgradeLevelOf(stack), nil
}

func upgradeMaterialSlotIsAccountShared(cmd Command) bool {
	if cmd.MaterialSlotIndex < 0 {
		return false
	}
	return dnfrepo.IsAccountSharedInventorySlot(listTypeMain, cmd.MaterialSlotIndex)
}

func ownerAppliedUpgradeResult(ctx context.Context, settings dnfrepo.SettingsRepository, opcode uint16, cmd Command, result UpgradeResult) alignedcmd.Result {
	if !result.Success {
		return alignedcmd.Result{
			Handled:         true,
			ResponseAllowed: true,
			Operation:       cmd.Operation,
			Reason: fmt.Sprintf(
				"inventory owner rejected: %s error=%d; returned current-EXE op50 error ACK",
				cmd.String(),
				result.ErrorCode,
			),
			UpperResponses: []alignedcmd.UpperResponse{
				class1Response(opcode, buildUpgradeItemErrorAck(result.ErrorCode)),
			},
		}
	}

	responses := []alignedcmd.UpperResponse{
		class1Response(opcode, buildUpgradeItemSuccessAck(result)),
	}
	if entries := upgradeItemUpdateEntries(cmd, result); len(entries) > 0 {
		responses = append(responses, class0Response(msgItemListUpdate, buildCommonItemListUpdateBody(listTypeMain, entries)))
	}
	// The native op50 ACK carries material/level fields but no gold balance.
	// Only the new authoritative-fee path adds the committed wallet projection.
	if cmd.UpgradeCostResolver != nil && result.GoldCost > 0 {
		var wallet packetWriter
		wallet.writeByte(listTypeMain)
		wallet.writeUint16(1)
		wallet.writeBytes(makeCurrentItemListEntry(0, 0, result.UpdatedGold))
		responses = append(responses, class0Response(msgItemListUpdate, wallet.bytes()))
	}
	// NOTI 0x0056 upgrade announcement when level meets notice threshold.
	if cmd.UpgradeNoticeLevel >= 0 {
		noticeLevel := result.NewLevel
		if !result.UpgradeSucceeded {
			noticeLevel = result.OldLevel
		}
		if int(noticeLevel) >= cmd.UpgradeNoticeLevel {
			responses = append(responses, class0Response(msgUpgradeNotice, buildUpgradeNoticeBody(
				result.UpgradeSucceeded, cmd.SelectedCharacterID, result.TargetItemTemplateID, noticeLevel)))
		}
	}
	return alignedcmd.Result{
		Handled:         true,
		ResponseAllowed: true,
		Operation:       cmd.Operation,
		Reason: fmt.Sprintf(
			"inventory owner applied: %s mode=%s level=%d->%d materialLeft=%d goldCost=%d changed=%t resultCode=%d success_weight=%d%s; opened 0x0050 ACK + 0x000D main-list refresh in 86JP order",
			cmd.String(),
			result.Mode,
			result.OldLevel,
			result.NewLevel,
			result.MaterialRemainingStackCount,
			result.GoldCost,
			result.Changed,
			result.ResultCode,
			result.UpgradeSuccessWeight,
			upgradeProtectTicketReasonSuffix(result)+upgradePortableReasonSuffix(result)+upgradeRateBuffReasonSuffix(result),
		),
		// 摧毁档补偿落位的事实（并进既有行 / 新开格 / 开关取值）如实交给 dnfbridge，
		// 由它发出可 grep 的事件：领域层没有 logger。见 upgrade_destroy_bonus.go。
		UpgradeDestroyBonusMerge: upgradeDestroyBonusMergePayload(cmd, result),
		// 保护券档位的事实（消耗了哪一行 / 没找到 / 开关取值）同理交给 dnfbridge，
		// 由它发 `game-upgrade-protect-ticket-consumed` / `-absent`。见
		// upgrade_protect_ticket.go。
		UpgradeProtectTicket: upgradeProtectTicketPayload(result, upgradeProtectTicketMode()),
		UpperResponses:       responses,
	}
}

// upgradeProtectTicketReasonSuffix 把保护券判定压进既有 op50 日志行的 reason=
// （ops 复测时 `grep protectTicket=` 就能看出这次是"没走这一档 / 走了但没券 / 消耗了"）。
func upgradeProtectTicketReasonSuffix(result UpgradeResult) string {
	if !result.ProtectTicketScanned {
		return ""
	}
	if !result.ProtectTicketConsumed {
		return fmt.Sprintf(" protectTicket=absent scanned_slots=%d retain_level=%d resolver_error=%q",
			result.ProtectTicketCandidates, result.ProtectTicketRetainLevel, result.ProtectTicketScanError)
	}
	return fmt.Sprintf(" protectTicket=consumed slot=%d item=%d left=%d retain_level=%d pvf_path=%s scanned_slots=%d",
		result.ProtectTicketSlot,
		result.ProtectTicketItemID,
		result.ProtectTicketRemaining,
		result.ProtectTicketRetainLevel,
		result.ProtectTicketPVFPath,
		result.ProtectTicketCandidates)
}

// upgradePortableReasonSuffix 把便携消耗品档压进同一条 reason=（复测时
// `grep portable=` 就能看出这次是不是走了便携档、用的是哪一件、最终成功率是多少）。
func upgradePortableReasonSuffix(result UpgradeResult) string {
	portable := result.PortableConsumable
	if portable == nil {
		return ""
	}
	return fmt.Sprintf(" portable=consumed item=%d mode=%s level_window=%d..%d success_bonus=%d material_slot=%d pvf_path=%s",
		portable.ItemID,
		portable.Mode,
		portable.LevelMin,
		portable.LevelMax,
		result.UpgradeSuccessBonus,
		result.MaterialSlotIndex,
		portable.PVFPath)
}

// upgradeRateBuffReasonSuffix 把"这次掷点用上了成功率 buff 药水"压进同一条 reason=
// （复测时 `grep rate_buff_bonus=` 就能看出加成多少、到什么时候；最终权重在
// reason 的 `success_weight=` 里，无论有没有 buff 都会打）。
// 没有生效时返回空串 —— 这段日志逐字节与改动前相同。
func upgradeRateBuffReasonSuffix(result UpgradeResult) string {
	if result.UpgradeRateBuffBonus <= 0 {
		return ""
	}
	return fmt.Sprintf(" rate_buff_bonus=%d rate_buff_until_unix=%d rate_buff_boost_env=%s",
		result.UpgradeRateBuffBonus,
		result.UpgradeRateBuffUntil,
		UpgradeRateBuffBoostEnv)
}

// upgradeDestroyBonusMergePayload 只在 op50 摧毁档（resultCode==3 且有补偿道具）填值，
// 其余档位一律 nil（bridge 因此不会为普通强化打印任何补偿事件）。
func upgradeDestroyBonusMergePayload(cmd Command, result UpgradeResult) *alignedcmd.UpgradeDestroyBonusMerge {
	if result.ResultCode != upgradeResultCodeDestroy || result.DestroyBonusItemID <= 0 {
		return nil
	}
	return &alignedcmd.UpgradeDestroyBonusMerge{
		ItemID:        int64(result.DestroyBonusItemID),
		GrantedCount:  int64(result.DestroyBonusCount),
		PlacedCount:   result.DestroyBonusPlacedCount,
		Slot:          result.DestroyBonusSlot,
		Slots:         append([]int16(nil), result.DestroyBonusSlots...),
		Merged:        result.DestroyBonusMerged,
		Created:       result.DestroyBonusCreated,
		Identity:      cmd.UpgradeDestroyBonusIdentity,
		Container:     cmd.UpgradeDestroyBonusContainer,
		PlacementMode: upgradeDestroyBonusPlacementMode(),
	}
}

func upgradeItemUpdateEntries(cmd Command, result UpgradeResult) []commonItemListEntry {
	updates := make(map[int16]dnfrepo.ItemStack, 3)
	if result.TargetSlotIndex >= 0 {
		updates[result.TargetSlotIndex] = result.TargetUpdatedStack
	}
	if result.ConversionMaterialUpdated {
		updates[result.ConversionMaterialSlot] = result.MainRefresh[slotKey(listTypeMain, result.ConversionMaterialSlot)]
	}
	if result.MaterialUpdated && result.MaterialSlotIndex >= 0 {
		updates[result.MaterialSlotIndex] = result.MaterialUpdatedStack
	}
	// 被消耗的保护券那一行也要发 op14：厂商参考把 protectTicketUpdate.SlotIndex
	// 加进 MainRefreshSlots（InventoryItemUpgradeService.cs:336-337），否则客户端背包里
	// 那张券的数量不会变 —— 运维验收判据之一就是"券数量减少了"。扣到 0 时发空行
	// （buildCommonItemListUpdateBody 会写成 itemID=0xFFFFFFFF 的整行 0）。
	if result.ProtectTicketConsumed && result.ProtectTicketSlot >= 0 &&
		result.ProtectTicketSlot != result.MaterialSlotIndex {
		if stack, ok := result.MainRefresh[slotKey(listTypeMain, result.ProtectTicketSlot)]; ok {
			updates[result.ProtectTicketSlot] = stack
		} else {
			updates[result.ProtectTicketSlot] = dnfrepo.ItemStack{}
		}
	}
	if result.DestroyBonusItemID > 0 && result.DestroyBonusSlot >= 0 {
		// 补偿道具现在可能同时改多行（并入的既有行 + 封顶后溢出新开的那一格），
		// 所以逐槽发 op14 增量；DestroyBonusSlots 为空时回落到 ACK 报的那一格
		// （回退档与旧结果构造都只填那一格）。
		bonusSlots := result.DestroyBonusSlots
		if len(bonusSlots) == 0 {
			bonusSlots = []int16{result.DestroyBonusSlot}
		}
		for _, bonusSlot := range bonusSlots {
			if bonusSlot < 0 {
				continue
			}
			if stack, ok := result.MainRefresh[slotKey(listTypeMain, bonusSlot)]; ok {
				updates[bonusSlot] = stack
				continue
			}
			// 兜底行（MainRefresh 缺这一格时才可能走到）与真正落库的行同形状：
			// 默认档带全 PVF 堆叠契约键，回退档与各自复现的那一版逐字节相同。
			updates[bonusSlot] = upgradeDestroyBonusEntryStack(cmd, result)
		}
	}
	entries := make([]commonItemListEntry, 0, len(updates))
	for slot, stack := range updates {
		entries = append(entries, commonItemListEntry{slot: slot, stack: stack})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].slot < entries[j].slot })
	return entries
}

func ownerAppliedEnchantResult(ctx context.Context, settings dnfrepo.SettingsRepository, opcode uint16, cmd Command, result EnchantResult) alignedcmd.Result {
	if !result.Success {
		reason := fmt.Sprintf(
			"inventory owner rejected: %s error=%d; returned current-EXE 0x0110 error ACK",
			cmd.String(),
			result.ErrorCode,
		)
		if result.PartRejection != "" {
			// 部位限制拒绝：日志要能一眼看出"哪颗宝珠、什么部位表、目标什么部位"，
			// 复测时用 `reason=bead_part_mismatch` 直接 grep。宝珠与目标都没有被改动
			// （判据在写库之前返回，不消耗、不写库）。
			reason = fmt.Sprintf(
				"inventory owner rejected bead part: %s bead=%d target_list=%d target_slot=%d target_item=%d bead_parts=%v target_part=%q reason=%s error=%d; bead not consumed and no write; returned current-EXE 0x0110 error ACK",
				cmd.String(),
				result.BeadItemID,
				result.TargetListType,
				result.TargetSlotIndex,
				result.TargetItemID,
				result.BeadParts,
				result.TargetPart,
				result.PartRejection,
				result.ErrorCode,
			)
		}
		return alignedcmd.Result{
			Handled:         true,
			ResponseAllowed: true,
			Operation:       cmd.Operation,
			Reason:          reason,
			UpperResponses: []alignedcmd.UpperResponse{
				class1Response(opcode, buildEnchantByBeadErrorAck(result.ErrorCode)),
			},
		}
	}

	state, found, err := dnfrepo.LoadCharacterContainerState(ctx, settings, result.CharacterID)
	if err != nil || !found {
		state = dnfrepo.CharacterContainerState{}
	}
	if result.TargetListType == listTypePet || result.WornPetTarget {
		// 穿戴中宠物（目标 list 3 槽 26）：没有 list 7 行可刷新，改发**穿戴行**
		// 的 op14 行（list 3 槽 26）——客户端补丁的 pet enchant display 钩子
		// 正是覆盖 list7-slots0-139 或 list3-slot26，打完立即能看到宝珠。
		targetUpdate := class0Response(msgItemListUpdate, buildPetItemListUpdateBody([]commonItemListEntry{{slot: result.TargetSlotIndex, stack: result.TargetUpdatedStack}}))
		if result.WornPetTarget {
			targetUpdate = class0Response(msgItemListUpdate, buildCommonItemListUpdateBody(listTypeEquipment, []commonItemListEntry{{slot: currentPetWornEquipmentSlot, stack: result.WornPetUpdatedStack}}))
		}
		return alignedcmd.Result{
			Handled:         true,
			ResponseAllowed: true,
			Operation:       cmd.Operation,
			Reason: fmt.Sprintf(
				"inventory owner applied pet enchant: %s card=%d upgrade=%d beadLeft=%d changed=%t worn_target=%t; sent target list-%d row and bead list-0 op14 rows before current-EXE 0x0110 ACK",
				cmd.String(),
				result.CardItemID,
				result.EnchantUpgradeCount,
				result.BeadRemainingCount,
				result.Changed,
				result.WornPetTarget,
				result.TargetListType,
			),
			UpperResponses: []alignedcmd.UpperResponse{
				targetUpdate,
				class0Response(msgItemListUpdate, buildCommonItemListUpdateBody(listTypeMain, []commonItemListEntry{{slot: result.BeadSlotIndex, stack: result.BeadUpdatedStack}})),
				class1Response(opcode, buildEnchantByBeadSuccessAck(result)),
			},
		}
	}
	if result.WornEquipmentTarget {
		return alignedcmd.Result{
			Handled: true, ResponseAllowed: true, Operation: cmd.Operation,
			Reason:                     "穿戴装备附魔已提交；目标原位更新及材料刷新先于结果回包",
			WornEquipmentSlotRefreshes: []int16{result.TargetSlotIndex},
			UpperResponses: []alignedcmd.UpperResponse{
				class0Response(msgItemListUpdate, buildCommonItemListUpdateBody(listTypeMain, []commonItemListEntry{{slot: result.BeadSlotIndex, stack: result.BeadUpdatedStack}})),
				class1Response(opcode, buildEnchantByBeadSuccessAck(result)),
			},
		}
	}
	// The current EXE's 0x0110 success handler resolves the target slot to an
	// item pointer and publishes that pointer to the result popup. A generic
	// selected-container refresh after the ACK destroys and recreates the
	// item objects while the popup still owns the pointer, so the popup can
	// render an unrelated item that later reuses the same allocation.
	applied := alignedcmd.Result{
		Handled:         true,
		ResponseAllowed: true,
		Operation:       cmd.Operation,
		Reason: fmt.Sprintf(
			"inventory owner applied: %s card=%d upgrade=%d beadLeft=%d changed=%t; opened one 0x000D main-list refresh then 0x0110 ACK without a pointer-invalidating duplicate refresh",
			cmd.String(),
			result.CardItemID,
			result.EnchantUpgradeCount,
			result.BeadRemainingCount,
			result.Changed,
		),
		UpperResponses: []alignedcmd.UpperResponse{
			class0Response(msgItemListRefresh, buildCommonItemListRefreshBodyWithState(listTypeMain, result.MainRefresh, state)),
			class1Response(opcode, buildEnchantByBeadSuccessAck(result)),
		},
	}
	// 成功路径回的是 class0/0x000D list 0 整表刷新；op13 是替换语义，而 0:0 钱包行、
	// 账号共享的 0:354..0:365 与装备行的品级种子/孔位/符文/期限都不在角色快照里，
	// 所以这条刷新必须由桥侧用会话权威投影重建（上面的 Body 只留作领域侧证据，
	// 不再上线）。见 dnfbridge/authoritative_list_refresh.go 与 native-next914/
	// enchant-gold-fix/v2 的 README。
	applied.AuthoritativeListRefreshes = []byte{listTypeMain}
	return applied
}

func ownerAppliedUpgradeTicketResult(ctx context.Context, settings dnfrepo.SettingsRepository, opcode uint16, cmd Command, result UpgradeTicketResult) alignedcmd.Result {
	if !result.Success {
		return alignedcmd.Result{
			Handled:         true,
			ResponseAllowed: true,
			Operation:       cmd.Operation,
			Reason: fmt.Sprintf(
				"inventory owner rejected: %s error=%d; returned 0x0050 error ACK",
				cmd.String(),
				result.ErrorCode,
			),
			UpperResponses: []alignedcmd.UpperResponse{
				class1Response(opcode, buildUpgradeTicketErrorAck(result.ErrorCode)),
			},
		}
	}

	responses := []alignedcmd.UpperResponse{
		class1Response(opcode, buildUpgradeTicketSuccessAck(result)),
	}
	if entries := upgradeTicketUpdateEntries(result); len(entries) > 0 {
		responses = append(responses, class0Response(msgItemListUpdate, buildCommonItemListUpdateBody(listTypeMain, entries)))
	}
	return alignedcmd.Result{
		Handled:         true,
		ResponseAllowed: true,
		Operation:       cmd.Operation,
		Reason: fmt.Sprintf(
			"inventory owner applied: %s mode=%s level=%d->%d roll=%t ticketLeft=%d changed=%t; opened 0x0050 ACK + 0x000D main-list refresh in 86JP order",
			cmd.String(),
			result.Mode,
			result.OldLevel,
			result.NewLevel,
			result.UpgradeSucceeded,
			result.MaterialRemainingStackCount,
			result.Changed,
		),
		UpperResponses: responses,
	}
}

func upgradeTicketUpdateEntries(result UpgradeTicketResult) []commonItemListEntry {
	updates := make(map[int16]dnfrepo.ItemStack, 2)
	if result.TargetSlotIndex >= 0 {
		updates[result.TargetSlotIndex] = result.TargetUpdatedStack
	}
	if result.MaterialUpdated && result.MaterialSlotIndex >= 0 {
		updates[result.MaterialSlotIndex] = result.MaterialUpdatedStack
	}
	entries := make([]commonItemListEntry, 0, len(updates))
	for slot, stack := range updates {
		entries = append(entries, commonItemListEntry{slot: slot, stack: stack})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].slot < entries[j].slot })
	return entries
}

func ownerAppliedMoveResult(ctx context.Context, settings dnfrepo.SettingsRepository, opcode uint16, cmd Command, result MoveResult) alignedcmd.Result {
	moveValue := clampInt32(result.MoveCount)
	if result.Mode == "noop" {
		moveValue = 0
	}
	// P61/P68：金库（个人 list 2 / 账号 list 12）存入时，领域层会把物品自动并入
	// **已有同类堆**，真正改动的落点不是客户端点名的空槽。MoveResult 的端点就是
	// 那对落库端点（与 equip/owner 的 committed endpoint 约定一致），op19 ACK 必须
	// 回它，以消除应答空槽与事务结果不一致。现服客户端即时归并效果仍待验证。
	// 非金库端点、或本次没有发生落点重定向时，ackCommand 与请求端点逐字节相同。
	ackCommand := vaultCommittedMoveAckCommand(cmd, result)
	var authoritativeListRefreshes []byte
	responses := []alignedcmd.UpperResponse{
		class1Response(opcode, buildMoveItemspaceSuccessAck(ackCommand, moveValue)),
	}
	itemSlotRefreshes := make([]alignedcmd.ItemSlotRefresh, 0, 2)
	crossContainerStack := result.Changed &&
		result.Mode == "stack" &&
		isMainPersonalCargoCrossList(cmd.SourceListType, cmd.DestinationListType)
	if crossContainerStack {
		itemSlotRefreshes = append(itemSlotRefreshes,
			alignedcmd.ItemSlotRefresh{ListType: result.SourceListType, SlotIndex: result.SourceSlotIndex},
			alignedcmd.ItemSlotRefresh{ListType: result.DestinationListType, SlotIndex: result.DestinationSlotIndex},
		)
	} else {
		for _, listType := range result.RefreshListTypes {
			slots, ok := result.Refresh[listType]
			if !ok {
				continue
			}
			state, found, err := dnfrepo.LoadCharacterContainerState(ctx, settings, result.CharacterID)
			if err != nil || !found {
				state = dnfrepo.CharacterContainerState{}
			}
			responses = append(responses, class0Response(
				msgItemListRefresh,
				buildCommonItemListRefreshBodyWithState(listType, slots, state),
			))
			if listType == listTypeMain {
				// Replacing the bag from the module snapshot drops its wallet and
				// writes equipment Count over quality. Reuse the login projection
				// at this response's position, with its wallet-read failure guard.
				authoritativeListRefreshes = []byte{listTypeMain}
			}
		}
	}
	if result.Changed && len(itemSlotRefreshes) == 0 && len(result.RefreshListTypes) == 0 &&
		(isVaultContainerList(cmd.SourceListType) || isVaultContainerList(cmd.DestinationListType)) {
		rows := result.ItemSlotRefreshes
		if len(rows) == 0 {
			rows = []alignedcmd.ItemSlotRefresh{{ListType: result.SourceListType, SlotIndex: result.SourceSlotIndex}, {ListType: result.DestinationListType, SlotIndex: result.DestinationSlotIndex}}
		}
		for _, row := range rows {
			if row.ListType == listTypeMain || row.ListType == listTypePersonalCargo || row.ListType == listTypeAvatar {
				itemSlotRefreshes = append(itemSlotRefreshes, row)
			}
		}
	}
	postActions := make([]alignedcmd.PostAction, 0, 1)
	if result.Changed && (cmd.SourceListType == listTypeAccountCargo || cmd.DestinationListType == listTypeAccountCargo) {
		// List 12 is account-owned.  The bridge rebuilds its current-EXE op13
		// snapshot from account metadata + AccountInventory after the op19 ACK;
		// it must not use the per-character common refresh path.
		postActions = append(postActions, alignedcmd.PostActionRefreshSelectedAccountCargo)
	}
	if result.Changed && (cmd.SourceListType == listTypePet || cmd.DestinationListType == listTypePet) {
		postActions = append(postActions, alignedcmd.PostActionRefreshSelectedItemContainers)
	}
	if result.Changed && (cmd.SourceListType == listTypeGuildMedal || cmd.DestinationListType == listTypeGuildMedal) {
		// list 38 与 list 7 一样不能走 list 0/1/2 的 op14 增量行：它有自己 98 格
		// 容器的权威快照构造器（buildCurrentItemListBodyForSession(38)）。勋章/
		// 守护珠的拖拽（背包 → 勋章栏、勋章栏 → 背包）改由这条已经验证过的
		// 「整容器重发」路径收口，客户端才会真的把行搬到目标页。
		postActions = append(postActions, alignedcmd.PostActionRefreshSelectedItemContainers)
	}
	return alignedcmd.Result{
		Handled:         true,
		ResponseAllowed: true,
		Operation:       cmd.Operation,
		Reason: fmt.Sprintf(
			"inventory owner applied: %s moveMode=%s moveCount=%d changed=%t refreshLists=%v incrementalSlots=%v requestEndpoints=(%d,%d)->(%d,%d) ackEndpoints=(%d,%d)->(%d,%d); opened 0x0013 ACK then authoritative refresh for changed counts",
			cmd.String(),
			result.Mode,
			result.MoveCount,
			result.Changed,
			result.RefreshListTypes,
			itemSlotRefreshes,
			cmd.SourceListType, cmd.SourceSlotIndex, cmd.DestinationListType, cmd.DestinationSlotIndex,
			ackCommand.SourceListType, ackCommand.SourceSlotIndex, ackCommand.DestinationListType, ackCommand.DestinationSlotIndex,
		),
		UpperResponses:             responses,
		ItemSlotRefreshes:          itemSlotRefreshes,
		PostActions:                postActions,
		AuthoritativeListRefreshes: authoritativeListRefreshes,
	}
}

// vaultCommittedMoveAckCommand 回答"这条 op19 应答该回哪对端点"。
//
// 金库（个人金库 list 2 / 账号金库 list 12）的存入会由领域层自动并入已有同类堆：
// Owner 把 MoveResult 端点写成**真正改动的落库端点**（与 equip/owner 的 committed
// endpoint 约定一致），于是这里回的 ACK 端点可能与客户端点名的槽不同。
// 候选依据：既有协议实现用 ACK 端点投影搬运；将端点对齐实际合并行，
// 避免应答空槽与事务结果不一致。现服 EXE 的原生效果仍需主代理验证。
//
// 只有金库端点参与、且本次确实发生了落点重定向时才改写；背包内移动、装扮、勋章、
// 宠物以及一切未重定向的移动都返回原 cmd，下行字节逐字节不变。
func vaultCommittedMoveAckCommand(cmd Command, result MoveResult) Command {
	if !result.Changed ||
		(!isVaultContainerList(cmd.SourceListType) && !isVaultContainerList(cmd.DestinationListType)) {
		return cmd
	}
	if result.SourceListType == cmd.SourceListType &&
		result.SourceSlotIndex == cmd.SourceSlotIndex &&
		result.DestinationListType == cmd.DestinationListType &&
		result.DestinationSlotIndex == cmd.DestinationSlotIndex {
		return cmd
	}
	ack := cmd
	ack.SourceListType = result.SourceListType
	ack.SourceSlotIndex = result.SourceSlotIndex
	ack.DestinationListType = result.DestinationListType
	ack.DestinationSlotIndex = result.DestinationSlotIndex
	return ack
}

// isVaultContainerList 回答某个容器端点是否是金库容器。
func isVaultContainerList(listType byte) bool {
	return listType == listTypePersonalCargo || listType == listTypeAccountCargo
}

func ownerAppliedSellResult(opcode uint16, cmd Command, result SellResult) alignedcmd.Result {
	return alignedcmd.Result{
		Handled:         true,
		ResponseAllowed: true,
		Operation:       cmd.Operation,
		Reason: fmt.Sprintf(
			"inventory owner applied: %s sold=%d goldDelta=%d updatedGold=%d changed=%t; opened 0x0016 response in C# order",
			cmd.String(),
			result.Sold.AppliedCount,
			result.GoldDelta,
			result.UpdatedGold,
			result.Changed,
		),
		UpperResponses: []alignedcmd.UpperResponse{
			class1Response(opcode, buildSellItemAck(result.Sold, result.UpdatedGold)),
		},
	}
}

func ownerAppliedSortResult(ctx context.Context, settings dnfrepo.SettingsRepository, opcode uint16, cmd Command, result SortResult) alignedcmd.Result {
	if result.ListType == listTypeAccountCargo {
		return alignedcmd.Result{
			Handled: true, ResponseAllowed: true, Operation: cmd.Operation,
			Reason:         fmt.Sprintf("account cargo owner applied: %s sortMode=%s moved=%d changed=%t", cmd.String(), result.Mode, result.MovedCount, result.Changed),
			UpperResponses: []alignedcmd.UpperResponse{class1Response(opcode, buildSortItemAck(result.ListType))},
			PostActions:    []alignedcmd.PostAction{alignedcmd.PostActionRefreshSelectedAccountCargo},
		}
	}
	refreshBody, ok := buildSortRefreshBody(ctx, settings, result)
	if !ok {
		return ownerAppliedResult(cmd, fmt.Sprintf("sortMode=%s moved=%d range=%d-%d changed=%t; list=%d needs dedicated 0x000D body", result.Mode, result.MovedCount, result.StartSlot, result.EndSlot, result.Changed, result.ListType))
	}
	var authoritativeListRefreshes []byte
	if result.ListType == listTypeMain {
		// Sort uses the same replacing bag body as a cargo merge; its
		// equipment instances also need the authoritative quality projection.
		authoritativeListRefreshes = []byte{listTypeMain}
	}
	return alignedcmd.Result{
		Handled:         true,
		ResponseAllowed: true,
		Operation:       cmd.Operation,
		Reason: fmt.Sprintf(
			"inventory owner applied: %s sortMode=%s moved=%d range=%d-%d changed=%t; opened 0x0014 ACK + 0x000D list refresh in C# order",
			cmd.String(),
			result.Mode,
			result.MovedCount,
			result.StartSlot,
			result.EndSlot,
			result.Changed,
		),
		UpperResponses: []alignedcmd.UpperResponse{
			class1Response(opcode, buildSortItemAck(result.ListType)),
			class0Response(msgItemListRefresh, refreshBody),
		},
		AuthoritativeListRefreshes: authoritativeListRefreshes,
	}
}

func ownerAppliedPetEquipmentMoveResult(opcode uint16, cmd Command, result dnfequip.MoveResult) alignedcmd.Result {
	moveValue := cmd.MoveCount
	if result.Mode == "noop" {
		moveValue = 0
	}
	postActions := []alignedcmd.PostAction(nil)
	// petProjectionFirst 只用于回包后刷新的顺序与回包自述：穿上/换装宠物（槽 26）时
	// 宠物投影必须排在整容器重发**前面**（见下面 default 分支的长注）。
	//
	// 2026-09-28（第二轮，操作者报"穿戴宠物后宠物面板里背包和已装备的宠物都消失，
	// 重新选角才恢复"）：宠物/神器的整容器重发**不再无条件执行**，落到下面的
	// PostActionRefreshSelectedPetMoveSlots；它由 dnfbridge 按
	// DNFBRIDGE_PET_MOVE_CONTAINER_REFRESH 决定发厂商逐槽口径还是旧的五个整容器
	// （默认逐槽）。证据与回退见 dnfbridge/equip_move_refresh.go 的
	// petMoveContainerRefreshEnv 注释。`petProjectionFirst` 只影响同一条
	// PostActions 序列里宠物投影与那个动作的相对位置：默认档逐槽口径下它退化成
	// "宠物投影在前、list 7 逐槽行在后"，回退档（full）则逐字节回到
	// 2026-09-28 第一轮的顺序。
	petProjectionFirst := result.Changed && result.DestinationSlotIndex == 26 &&
		result.Mode != "unequip" && result.Mode != "unequip_swap"
	if result.Changed {
		switch result.Mode {
		case "unequip", "unequip_swap":
			// 脱下方向逐字节保持旧顺序：这条路上根本没有带附魔的槽 26 行
			// （sendSelectedEquippedCreatureItemRefresh 现场以
			// reason=equipment_slot26_absent 跳过），把宠物状态表提前没有任何收益，
			// 所以一个包的位置都不动。
			postActions = append(postActions, alignedcmd.PostActionRefreshSelectedPetMoveSlots)
			if result.DestinationSlotIndex == 26 {
				postActions = append(postActions, alignedcmd.PostActionRefreshSelectedCreatureState)
			}
		default:
			if result.DestinationSlotIndex == 26 {
				// 宠物本体（槽 26）**穿上**方向的回包顺序：先把宠物投影发出去，再发
				// 宠物容器（list 7）的那一行/整容器。
				//
				// 宠物的附魔（宝珠）动态状态**只**由 list 3 槽 26 的 op14 行带给客户端
				// （客户端补丁 pet enchant display 的 scope 正是 list7-slots0-139 或
				// list3-slot26 的 same-template op14；见 dnfbridge/equipped_creature_item_refresh.go
				// 与 dnfbridge/scene_creature_state.go 的 sendSelectedCreatureStateAfterMoveAck）。
				//
				// 现场取证（2026-09-28 操作者报障："给宠物打完宝珠后重新穿上，约 1 秒内宠物面板
				// 里看不到附魔属性，脱下来再看又正常"）：
				//
				//   · 90CN 客户端 trace 同一批 op19 的耗时：穿宠物/神器
				//     class1/op19 ACK → class0/op2 mode1 是 **976–1047 ms**（8/8 次），
				//     而穿普通装备/时装（整容器重发被
				//     DNFBRIDGE_EQUIP_MOVE_CONTAINER_REFRESH=suppressed 取消）只要 **41–94 ms**
				//     （12/12 次）。这一个多出来的 ≈0.95 s 就是
				//     PostActionRefreshSelectedItemContainers（list 0/1/2/7/38，现场 ≈24 KB、
				//     ≈194 行）的构建+下发时间。
				//   · 服务端 packet_log.txt 里宠物 op19 的 post action 顺序是
				//     refresh_selected_item_containers → refresh_selected_creature_state → appearance，
				//     即那条**唯一携带附魔的** op14 行排在整容器**后面**；客户端在 ACK 那一刻
				//     已经乐观地把宠物穿到槽 26（native sub_1D2DE80 搬的是 target-26 对象），
				//     于是一直到 ≈1 s 后那条 op14 行挤到队首，宠物面板里才出现附魔词条。
				//   · 关键机制：dnfbridge/sendCurrentContainerListTypesWithRefresh 是**先把
				//     五个容器 body 全部构建完再逐个下发**（current_item_list_send.go:85-124），
				//     所以宠物行不只是排在五个大包后面，而是排在**整段构建**后面。换序把
				//     宠物行挪到构建之前，它当场就写得出去。
				//
				// 换序后宠物投影紧跟 ACK（同一批包里第 2 个），窗口从 ≈1 s 压到一次网络
				// 往返；包体与包数一个字节都没变（list 3 本来就不在整容器重发范围内，
				// currentSelectedItemListTypes = {0,1,2,7,38}），所以不需要开关回退。
				// 神器（27..29）不吃这条：它们的附魔在宠物本体上，保持原顺序不变。
				// 回归钉子：dnfbridge/pet_equip_enchant_ordering_test.go。
				postActions = append(postActions, alignedcmd.PostActionRefreshSelectedCreatureState)
			}
			postActions = append(postActions, alignedcmd.PostActionRefreshSelectedPetMoveSlots)
		}
		if result.DestinationSlotIndex == 26 {
			postActions = append(postActions, alignedcmd.PostActionRefreshSelectedActorAppearance)
		}
	}
	// Current NoPack op19 keeps positional endpoint semantics (A is the
	// inventory/pet side, B is the worn side), but the owner may redirect A to
	// the next real empty slot while resolving an unequip against stale client
	// inventory state. Report the committed slots while retaining the raw
	// actor-worn endpoint (3 or 17): sub_1D2DE80 has distinct native branches
	// for those endpoint values.
	ackCommand := committedEquipmentMoveAckCommand(cmd, result)
	petProjectionOrder := "containers_then_creature_state"
	if petProjectionFirst {
		petProjectionOrder = "creature_slot26_op14_op105_op102_then_containers"
	}
	return alignedcmd.Result{
		Handled:         true,
		ResponseAllowed: true,
		Operation:       cmd.Operation,
		Reason: fmt.Sprintf(
			"equipment owner applied current-EXE pet move: %s equipMove=%s durableSrc=(%d,%d) durableDst=(%d,%d) item=%d swap=%d changed=%t; opened exact op19 ACK then pet_projection_order=%s, plus appearance when target26 changed",
			cmd.String(),
			result.Mode,
			result.SourceListType,
			result.SourceSlotIndex,
			result.DestinationListType,
			result.DestinationSlotIndex,
			result.ItemID,
			result.SwappedItemID,
			result.Changed,
			petProjectionOrder,
		),
		UpperResponses: []alignedcmd.UpperResponse{
			class1Response(opcode, buildMoveItemspaceSuccessAck(ackCommand, moveValue)),
		},
		PetMoveSlotRefreshes: petMoveBagSlotRefreshes(result),
		PostActions:          postActions,
	}
}

// petMoveBagSlotRefreshes 回答"这次宠物/神器搬运改动了 list 7 的哪些槽"。
//
// 厂商同源口径：`Network\Handlers\InventoryHandler.Move.cs:184-195` 的
// ApplyPetMoveProjection 遍历本次提交的 changed slots，把 `InventoryListType.Pet`
// 的那些槽收进 `InventoryMoveResult.PetCreatureRefreshSlots`
// （`Game\Inventory\InventoryModels.cs:59`），随后由
// `Network\Handlers\Pets\PetInventoryMoveCoordinator.cs:76-77` 用
// `SendUpdateItemList(Pet, PetCreatureRefreshSlots)` 逐槽下发；厂商**从不**在搬运后
// 回放整个宠物容器。我方的等价物就是持久化 MoveResult 的两个端点里落在 list 7 的那一侧
// （`equip/owner.go` 的 equip 保留请求的宠物容器源端点、unequip 把实际落袋槽写回
// DestinationSlotIndex，见 :1109-1119 与 :1194-1218）。
//
// 空槽由桥接层按厂商 `ItemListUpdateBuilder.WriteOnlineUpdateEntry`（:81）的
// `slot + int32(-1)` 口径投影，所以这里只报槽号，不报内容。
func petMoveBagSlotRefreshes(result dnfequip.MoveResult) []alignedcmd.ItemSlotRefresh {
	out := make([]alignedcmd.ItemSlotRefresh, 0, 2)
	seen := make(map[int16]struct{}, 2)
	appendSlot := func(listType byte, slotIndex int16) {
		if listType != listTypePet || slotIndex < 0 {
			return
		}
		if _, duplicate := seen[slotIndex]; duplicate {
			return
		}
		seen[slotIndex] = struct{}{}
		out = append(out, alignedcmd.ItemSlotRefresh{ListType: listTypePet, SlotIndex: slotIndex})
	}
	// 穿上：宠物从 list 7 走了；脱下：宠物回到 list 7；换装（swap）两侧都可能落在 list 7。
	appendSlot(result.SourceListType, result.SourceSlotIndex)
	appendSlot(result.DestinationListType, result.DestinationSlotIndex)
	return out
}

// ownerAppliedEquipmentMoveResult 把穿戴 owner 的一次判定翻成上行应答 + 后置刷新。
//
// loginWearNoopRefresh（= alignedcmd.Request.LoginWearReassertNoopRefresh，默认 on）
// 只影响**一条分支**：命中登录兜底闸门（equip.LoginWearReassertNoopGate）的 noop。
// 那条分支一个字节都不写库（这是兜底闸门本身的口径，未改），但今天的它**只回一条
// moveValue=0 的 ACK**，不发任何权威状态 ⇒ 客户端那份不一致的本地穿戴视图永远得不到
// 纠正，于是在同一会话里 A↔B 来回重发该族 op19（现场 2..9 条），每一次落在窗口外
// 都走 equip/owner.go:1046 的反转兜底变成**真卸下**（`_dist/weapon-unequip-diagnosis.md`
// §3.3 的放大器）。所以这里让 noop 也照发真落库路径那两份后置刷新 —— 包体与发送侧
// 全部复用既有 sender，不新增任何下行格式。
func ownerAppliedEquipmentMoveResult(opcode uint16, cmd Command, result dnfequip.MoveResult, equipAppearanceSlotGateOff bool, loginWearNoopRefresh bool) alignedcmd.Result {
	moveValue := cmd.MoveCount
	if result.Mode == "noop" {
		moveValue = 0
	}
	// noopLoginWearReassert：命中"客户端复述穿戴态"那一族 noop、且开关要求补权威刷新。
	//
	// 2026-09-28 第五轮：判据从"只认登录窗口兜底那一条闸门"扩展成 equip 域的一处真相
	// dnfequip.IsWornClaimNoopGate —— 它同时覆盖方向判定本身的两条具名闸门
	// （worn_slot_reassert = 那一格穿着点名那件；worn_slot_stale_claim = 那一格空着、
	// 客户端在声明一件不在身上的东西）。三条都是"一个字节都没写、但客户端那份视图需要被
	// 权威状态纠正"的 noop，所以回包之后补什么必须一致；旧闸门
	// （login_wear_reassert_noop）的取值、比较语义与下行字节**逐字节不变**。
	// 它不改变闸门判定（那在 equip 域），只改变"回包之后发什么"。
	// 注意：下面 reason 里那段现场标记仍叫 `login_wear_noop_refresh=`（名字未改，
	// 免得动到旧闸门的线上判读串），它的取值 authoritative_worn_state_without_item_write
	// 对新旧闸门都成立。
	noopLoginWearReassert := loginWearNoopRefresh && result.Mode == "noop" &&
		dnfequip.IsWornClaimNoopGate(result.NoopGate)
	postActions := []alignedcmd.PostAction(nil)
	appearanceRefresh := false
	if result.Changed || noopLoginWearReassert {
		// 穿脱专用的整容器动作：旧口径与 PostActionRefreshSelectedItemContainers
		// 逐字节等价（class0/op13 list 0/1/2/7/38 五个整容器，每次 ≈10.1 KB），
		// 但它只由穿脱路径发出，dnfbridge 因此可以单独按开关取消它。默认档不再
		// 重发（厂商 9.16 的 item_slot_refresh_count=0 口径）；证据与回退见
		// dnfbridge/equip_move_refresh.go。登录复述 noop 走同一条（见函数顶注）：
		// 它只是"补一次权威刷新"，仍然一个字节都不写库。
		postActions = append(postActions, alignedcmd.PostActionRefreshSelectedWearContainers)
		// 外观刷新加"外观相关性闸门"（卡顿根因，证据见 _dist/stutter-diagnosis.md §3/§4）：
		// 只有被移动物品的穿戴槽落在 mode0 纸娃娃的 14 个可见槽 0..13（或时装列表 17）
		// 里，这次穿脱才会改变纸娃娃外观；耳环/魔法石/护甲等槽位 ≥14 的装备对
		// mode0 body 贡献为零（dnfbridge/current_actor_mode0_appearance.go:72-75 把它们
		// 丢弃），为它发一次 class0/op2 mode0 只会让客户端白重建一次本地角色。
		// DNFBRIDGE_EQUIP_APPEARANCE_SLOT_GATE=off 一键回退到"每次穿脱都发"。
		appearanceRefresh = equipAppearanceSlotGateOff || equipmentMoveAffectsSelectedActorAppearance(cmd, result)
		if appearanceRefresh {
			postActions = append(postActions, alignedcmd.PostActionRefreshSelectedActorAppearance)
		}
	}
	// Current NoPack op19 keeps positional endpoint semantics (A is the
	// inventory/avatar/cargo side, B is the worn side). The equipment owner
	// preserves that order in MoveResult while replacing A with the actual
	// committed empty slot when an unequip destination was stale. Keep the
	// request's raw actor-worn endpoint because current NoPack distinguishes
	// list 3 from list 17 even though both share the durable equipment owner.
	ackCommand := committedEquipmentMoveAckCommand(cmd, result)
	reason := fmt.Sprintf(
		"equipment owner applied endpoint move: %s equipMove=%s%s requestA=(%d,%d) requestB=(%d,%d) finalA=(%d,%d) finalB=(%d,%d) item=%d swap=%d changed=%t appearance_refresh=%t; opened current-EXE op19 ACK then authoritative item containers and selected actor mode0 appearance refresh",
		cmd.String(),
		result.Mode,
		noopGateSuffix(result),
		cmd.SourceListType,
		cmd.SourceSlotIndex,
		cmd.DestinationListType,
		cmd.DestinationSlotIndex,
		result.SourceListType,
		result.SourceSlotIndex,
		result.DestinationListType,
		result.DestinationSlotIndex,
		result.ItemID,
		result.SwappedItemID,
		result.Changed,
		appearanceRefresh,
	)
	if noopLoginWearReassert {
		// 只在**这一条**分支追加现场标记：其余路径的 Reason 逐字节不变。
		reason += fmt.Sprintf(
			" login_wear_noop_refresh=%s post_actions=%d",
			equipLoginWearNoopRefreshReason,
			len(postActions),
		)
	}
	return alignedcmd.Result{
		Handled:         true,
		ResponseAllowed: true,
		Operation:       cmd.Operation,
		Reason:          reason,
		UpperResponses: []alignedcmd.UpperResponse{
			class1Response(opcode, buildMoveItemspaceSuccessAck(ackCommand, moveValue)),
		},
		PostActions: postActions,
	}
}

// equipLoginWearNoopRefreshReason 是 Reason 里那一段现场标记的取值（只在
// loginWearNoopRefresh 生效且命中登录复述闸门时出现）。它让 op19 的现场日志同时
// 说明"没有写库"和"补了权威刷新"这两件事 —— 只读 Reason 就能区分
// DNFBRIDGE_EQUIP_LOGIN_REASSERT_NOOP_REFRESH 的两档。
const equipLoginWearNoopRefreshReason = "authoritative_worn_state_without_item_write"

// equipmentMoveAffectsSelectedActorAppearance 判定一次已落库的穿/脱/换装是否真的会
// 改变当前 EXE 的 mode0 纸娃娃外观（14 个可见槽 0..13）。它是纯领域判定：不读环境变量、
// 不 import dnfbridge，只复用 dnfbridge 里 currentActorMode0AppearanceSlot /
// currentEXEActorEquipmentSlotForWornSlot 的口径（本函数刻意在域层复述这套槽位语义，
// 证据见 dnfbridge/current_actor_mode0_appearance.go 与 userinfo_current_equipment_rules.go）。
//
// 判定口径：
//   - 时装列表（wire 17，listTypeActorWornAlt）永远算外观相关：时装只会落在可见槽 0..10；
//   - 普通装备列表（wire 3，listTypeEquipment）只看穿戴槽是否落在可见的 0..13：
//     0..10 是装扮、12 是武器、13 是称号，14..25 是护甲/首饰、27..29 神器、
//     30 名牌、32 勋章——这些都 ≥14，对 mode0 body 贡献为零；
//   - 宠物列表（wire 7，listTypePet）永远不算外观相关（宠物走宠物投影，不进 mode0 纸娃娃）。
//
// 每个端点单独判定，源/目的任一落在"可见穿戴槽"即返回 true；因此 equip 看目的端、
// unequip 看源端、swap 看两端（哪一侧可见都算）。
func equipmentMoveAffectsSelectedActorAppearance(cmd Command, move dnfequip.MoveResult) bool {
	return equipmentMoveEndpointAffectsAppearance(cmd.SourceListType, move.SourceSlotIndex) ||
		equipmentMoveEndpointAffectsAppearance(cmd.DestinationListType, move.DestinationSlotIndex)
}

func equipmentMoveEndpointAffectsAppearance(listType byte, slot int16) bool {
	switch listType {
	case listTypeActorWornAlt: // 17：时装/装扮穿戴端点，总是可见
		return true
	case listTypeEquipment: // 3：普通装备穿戴端点，只看槽位
		return wornEquipmentSlotVisibleInMode0(slot)
	case listTypePet: // 7：宠物容器，从不进 mode0 纸娃娃
		return false
	default: // 背包/仓库/勋章等非穿戴端点
		return false
	}
}

// wornEquipmentSlotVisibleInMode0 回答普通装备穿戴槽是否落在 mode0 的 14 个可见槽内。
// 现代运行时口径里穿戴槽 == 外观槽（0..13），见 dnfbridge/currentActorMode0AppearanceSlot
// 的 `0 <= SlotIndex < 14` 直接分支；旧式 PVF 初始记录的武器槽 11 也会映射到 12，同样落
// 在 0..13 内，所以直接按 [0,13] 判定即可覆盖两种口径。
func wornEquipmentSlotVisibleInMode0(slot int16) bool {
	return slot >= 0 && slot <= 13
}

func isProvenPetEquipmentMove(cmd Command) bool {
	if cmd.SourceListType != listTypePet || !isActorWornEndpoint(cmd.DestinationListType) {
		return false
	}
	if cmd.DestinationSlotIndex == 26 {
		return cmd.SourceSlotIndex >= 0 && cmd.SourceSlotIndex <= 139
	}
	return cmd.DestinationSlotIndex >= 27 && cmd.DestinationSlotIndex <= 29 &&
		cmd.SourceSlotIndex >= 140 && cmd.SourceSlotIndex <= 188
}

func isActorWornEndpoint(listType byte) bool {
	return listType == listTypeEquipment || listType == listTypeActorWornAlt
}

func petMoveTouchesWornCreatureSlot(cmd Command, result dnfequip.MoveResult) bool {
	return isWornCreatureSlot(cmd.SourceListType, cmd.SourceSlotIndex) ||
		isWornCreatureSlot(cmd.DestinationListType, cmd.DestinationSlotIndex) ||
		isWornCreatureSlot(result.SourceListType, result.SourceSlotIndex) ||
		isWornCreatureSlot(result.DestinationListType, result.DestinationSlotIndex)
}

func isWornCreatureSlot(listType byte, slotIndex int16) bool {
	return isActorWornEndpoint(listType) && slotIndex == 26
}

func committedEquipmentMoveAckCommand(cmd Command, result dnfequip.MoveResult) Command {
	ack := cmd
	ack.SourceListType = result.SourceListType
	ack.SourceSlotIndex = result.SourceSlotIndex
	ack.DestinationListType = result.DestinationListType
	ack.DestinationSlotIndex = result.DestinationSlotIndex
	if isActorWornEndpoint(cmd.SourceListType) && isActorWornEndpoint(result.SourceListType) {
		ack.SourceListType = cmd.SourceListType
	}
	if isActorWornEndpoint(cmd.DestinationListType) && isActorWornEndpoint(result.DestinationListType) {
		ack.DestinationListType = cmd.DestinationListType
	}
	return ack
}

func validateProvenPetEquipmentMove(cmd Command) error {
	// 尾字节（`body[26]/body[27]`）自 2026-09-29 起被客户端用作**玩家请求标志位**，
	// 因此本校验不再看它（默认修复档）。作用域、证据与开关见 op19_trailing_flag.go：
	//   - 本函数只被"宠物来源"的 op19 调用（`isProvenPetEquipmentMove` 要求源 list = 7），
	//     装备路径永远到不了这里 ⇒ 标志位对装备包本来就没有约束；
	//   - 但客户端会对**所有** op19 打标（含宠物），若不放开这一条，合法宠物包会被判非法。
	// 其余常量（MoveCount / DestinationStack / ActorIndex）**一个字不放**。
	if cmd.MoveCount != 1 || cmd.DestinationStack != 0 ||
		(!op19TrailingFlagTolerated() && (cmd.TrailingState0 != 0 || cmd.TrailingState1 != 0)) {
		return fmt.Errorf("invalid current-EXE pet op19 constants: count=%d destination_stack=%d tail=(%d,%d)", cmd.MoveCount, cmd.DestinationStack, cmd.TrailingState0, cmd.TrailingState1)
	}
	if cmd.ActorIndex < -1 || cmd.ActorIndex > 2 {
		return fmt.Errorf("invalid current-EXE pet op19 actor index: %d", cmd.ActorIndex)
	}
	return nil
}

func equipmentMoveCommand(cmd Command) dnfequip.MoveCommand {
	return dnfequip.MoveCommand{
		SelectedCharacterID:      cmd.SelectedCharacterID,
		SourceListType:           cmd.SourceListType,
		SourceSlotIndex:          cmd.SourceSlotIndex,
		SourceInstanceValue:      cmd.SourceInstanceValue,
		MoveCount:                cmd.MoveCount,
		DestinationListType:      cmd.DestinationListType,
		DestinationSlotIndex:     cmd.DestinationSlotIndex,
		DestinationInstanceValue: cmd.DestinationInstance,
	}
}

func normalizedEquipmentMoveCommand(cmd Command) dnfequip.MoveCommand {
	ownerCommand := equipmentMoveCommand(cmd)
	if isActorWornEndpoint(ownerCommand.SourceListType) {
		ownerCommand.SourceListType = listTypeEquipment
	}
	if isActorWornEndpoint(ownerCommand.DestinationListType) {
		ownerCommand.DestinationListType = listTypeEquipment
	}
	return ownerCommand
}

// noopGateSuffix 只在 noop 且闸门具名时补一段现场可读的标记，
// 让 `equipMove=noop` 能区分是厂商同源的复述闸门还是登录窗口兜底。
func noopGateSuffix(result dnfequip.MoveResult) string {
	if result.Mode != "noop" || strings.TrimSpace(result.NoopGate) == "" {
		return ""
	}
	return " noopGate=" + strings.TrimSpace(result.NoopGate)
}

func buildSortRefreshBody(ctx context.Context, settings dnfrepo.SettingsRepository, result SortResult) ([]byte, bool) {
	switch result.ListType {
	case listTypeMain, listTypePersonalCargo, listTypeAvatar:
		state, found, err := dnfrepo.LoadCharacterContainerState(ctx, settings, result.CharacterID)
		if err != nil || !found {
			state = dnfrepo.CharacterContainerState{}
		}
		return buildCommonItemListRefreshBodyWithState(result.ListType, result.Refresh, state), true
	case listTypePet:
		return buildPetItemListRefreshBody(result.Refresh), true
	default:
		return nil, false
	}
}

func ownerAppliedRepairResult(opcode uint16, cmd Command, result RepairResult) alignedcmd.Result {
	return alignedcmd.Result{
		Handled:         true,
		ResponseAllowed: true,
		Operation:       cmd.Operation,
		Reason: fmt.Sprintf(
			"inventory owner applied: %s list=%d slot=%d durability=%d->%d cost=%d gold=%d changed=%t; opened 0x0017 response in C# order",
			cmd.String(),
			result.ListType,
			result.SlotIndex,
			result.OldDurability,
			result.NewDurability,
			result.Cost,
			result.UpdatedGold,
			result.Changed,
		),
		UpperResponses: []alignedcmd.UpperResponse{
			class1Response(opcode, buildRepairEquipmentAck(cmd.SourceListType, result.SlotIndex, result.UpdatedGold)),
		},
	}
}

func ownerAppliedRepairAllResult(opcode uint16, cmd Command, result dnfequip.RepairResult) alignedcmd.Result {
	return alignedcmd.Result{
		Handled:         true,
		ResponseAllowed: true,
		Operation:       cmd.Operation,
		Reason: fmt.Sprintf(
			"equipment owner applied repair-all: %s repaired=%d cost=%d free_repair=%t gold=%d changed=%t; opened 0x0017 slot=0xFFFF response in 86JP order",
			cmd.String(),
			result.RepairedCount,
			result.Cost,
			result.FreeRepair,
			result.UpdatedGold,
			result.Changed,
		),
		UpperResponses: []alignedcmd.UpperResponse{
			class1Response(opcode, buildRepairEquipmentAck(cmd.SourceListType, -1, result.UpdatedGold)),
		},
	}
}

func ownerAppliedEquippedRepairResult(opcode uint16, cmd Command, result dnfequip.RepairResult) alignedcmd.Result {
	return alignedcmd.Result{
		Handled:         true,
		ResponseAllowed: true,
		Operation:       cmd.Operation,
		Reason: fmt.Sprintf(
			"equipment owner applied: %s equipSlot=%d item=%d durability=%d->%d cost=%d free_repair=%t gold=%d changed=%t; opened 0x0017 response in C# order",
			cmd.String(),
			result.SlotIndex,
			result.ItemID,
			result.OldDurability,
			result.NewDurability,
			result.Cost,
			result.FreeRepair,
			result.UpdatedGold,
			result.Changed,
		),
		UpperResponses: []alignedcmd.UpperResponse{
			class1Response(opcode, buildRepairEquipmentAck(cmd.SourceListType, result.SlotIndex, result.UpdatedGold)),
		},
	}
}

// applyUpgradeRateBuffResolution 在 op44/op251 真正扣道具之前认出「强化/增幅成功率
// buff 药水」，把它的模式/加成/持续时间挂到 cmd 上（见 upgrade_rate_buff.go）。
//
// 解析器缺失 = 不记账（既有行为：药水只被消耗、成功率不变）；解析器报错 = **整笔拒绝**
// （fail-closed：读不到 PVF 就不猜加成，也绝不"扣了药水却没记上"）。
func applyUpgradeRateBuffResolution(req alignedcmd.Request, cmd *Command) error {
	if req.UpgradeRateBuffResolver == nil || cmd == nil {
		return nil
	}
	resolution, err := req.UpgradeRateBuffResolver(int64(cmd.ItemTemplateID))
	if err != nil {
		return fmt.Errorf("%w: item=%d: %v", ErrPremiumContractResolveFailed, cmd.ItemTemplateID, err)
	}
	if resolution.Mode == "" || resolution.DurationMillis <= 0 || resolution.BonusWeight <= 0 {
		return nil
	}
	cmd.UseUpgradeBuff = &resolution
	return nil
}

func ownerAppliedUseStackableResult(opcode uint16, cmd Command, result UseStackableResult) alignedcmd.Result {
	ack := buildUseStackableSuccessAck(result)
	if opcode == uint16(dnfenum.CmdPacketUseSharedEffectItem) {
		// Native sub_1FC9830 consumes only u16 source slot and u8 list
		// after the success marker. It releases the pending item-use lock.
		ack = ack[:4]
	}
	responses := []alignedcmd.UpperResponse{
		class1Response(opcode, ack),
	}
	if opcode == uint16(dnfenum.CmdPacketUseSharedEffectItem) && result.HasRandomRewardEffect {
		// Native NOTI219/sub_2012AD0: actor u16, source item u32,
		// selected PVF [chn random image percent] index u8.
		var effect packetWriter
		effect.writeInt16(int16(cmd.SelectedCharacterID))
		effect.writeInt32(int32(result.ItemID))
		effect.writeByte(result.RandomRewardEffectIndex)
		responses = append(responses, class0Response(219, effect.bytes()))
	}
	postActions := make([]alignedcmd.PostAction, 0, 1)
	itemSlotRefreshes := []alignedcmd.ItemSlotRefresh{{
		ListType:  result.ListType,
		SlotIndex: result.SlotIndex,
	}}
	for _, slot := range result.RandomRewardSlots {
		// 奖励可能落在主背包(0)也可能落在宠物背包消耗品段(7，[feed]/[creature] 的
		// PVF 归属)。list 7 的增量行在 dnfbridge 里会被换成整容器下发，
		// 这里只如实带上容器类型；零值仍是主背包，op44 既有行为不变。
		itemSlotRefreshes = append(itemSlotRefreshes, alignedcmd.ItemSlotRefresh{
			ListType:  result.RandomRewardListType,
			SlotIndex: slot,
		})
	}
	reason := fmt.Sprintf(
		"inventory owner applied: %s item=%d remaining=%d pvf_path=%q stackable_type=%q changed=%t; opened exact current-EXE class1/op44 success response + one-row op14 refresh",
		cmd.String(),
		result.ItemID,
		result.RemainingCount,
		result.PVFPath,
		result.StackableType,
		result.Changed,
	)
	if result.PremiumActivated {
		// Current EXE class0/op66 (sub_1D61460) reads u16 sub-op + u8 type +
		// raw[8] expiry; sub-op 2 is the activate/set path (86JP
		// PremiumService.ActivateAndNotify body matches it exactly). The 0x0312
		// service-data blob is old-client-only (current 786 = skill quick slot
		// sort) and is never pushed. Internal devil slot types 580..587 cannot
		// fit this body and are deliberately suppressed.
		if premium.CanNotifyActivation(result.PremiumType) {
			responses = append(responses, class0Response(premiumActivatedNotifyMsgID, premium.BuildActivatedBody(result.PremiumType, result.PremiumRemainingSeconds)))
		}
		reason = fmt.Sprintf(
			"inventory owner applied: %s contract item=%d remaining=%d premium_type=%d premium_remaining=%ds changed=%t; opened op44 ACK + NOTI 0x0042",
			cmd.String(),
			result.ItemID,
			result.RemainingCount,
			result.PremiumType,
			result.PremiumRemainingSeconds,
			result.Changed,
		)
		if result.PremiumType == premium.TypeCrystal {
			postActions = append(postActions, alignedcmd.PostActionRefreshCrystalContractState)
		}
		if result.PremiumType == premium.TypeBlackDiamond {
			postActions = append(postActions, alignedcmd.PostActionRefreshFatigueState)
		}
	}
	if result.ReviveCoinWalletUpdated {
		itemSlotRefreshes = append(itemSlotRefreshes, alignedcmd.ItemSlotRefresh{
			ListType:  dnfrepo.MainInventoryListType,
			SlotIndex: dnfrevivecoin.WalletSlot,
		})
		reason = fmt.Sprintf(
			"inventory owner applied: %s revive-coin consumable item=%d remaining=%d wallet_total=%d changed=%t; atomic source decrement + slot1 wallet increment, then exact op44 ACK + two-row op14 refresh",
			cmd.String(),
			result.ItemID,
			result.RemainingCount,
			result.ReviveCoinWalletTotal,
			result.Changed,
		)
	}
	if result.RandomRewardItemID > 0 {
		reason = fmt.Sprintf(
			"inventory owner applied: %s random reward item=%d remaining=%d reward=%d slots=%v; opened exact op44 ACK then refreshed source and reward rows",
			cmd.String(),
			result.ItemID,
			result.RemainingCount,
			result.RandomRewardItemID,
			result.RandomRewardSlots,
		)
	}
	// `[unlimited waste]` 族：把"这次没有扣数"写进同一条日志行的 reason=
	// （复测时 `grep unlimited_use=true` 就能看出这一族真的走了"用而不耗"分支）。
	// 同样用 += 追加，不覆盖上面任何一档的既有文案。
	if result.UnlimitedUse {
		reason += " unlimited_use=true"
	}
	// 成功率 buff 药水：把这次记账写进同一条日志行的 reason=（复测时
	// `grep rate_buff=` 就能看出"这次到底记上了没有、加成多少、到什么时候"）。
	// 用 += 追加，因此不会覆盖上面任何一档的既有文案。
	if result.UpgradeRateBuffMode != "" {
		reason += fmt.Sprintf(" rate_buff=%s bonus=%d until_unix=%d boost_env=%s",
			result.UpgradeRateBuffMode,
			result.UpgradeRateBuffBonus,
			result.UpgradeRateBuffUntil,
			UpgradeRateBuffBoostEnv)
	}
	applied := alignedcmd.Result{
		Handled:           true,
		ResponseAllowed:   true,
		Operation:         cmd.Operation,
		Reason:            reason,
		UpperResponses:    responses,
		ItemSlotRefreshes: itemSlotRefreshes,
		PostActions:       postActions,
	}
	if result.Changed && result.RemainingCount >= 0 && result.PreviousCount > 0 &&
		result.PreviousCount-result.RemainingCount == 1 {
		applied.StackableConsumption = &alignedcmd.StackableConsumption{
			CharacterID: result.CharacterID, ListType: result.ListType,
			SlotIndex: result.SlotIndex, ItemID: result.ItemID,
			PreviousCount: result.PreviousCount, RemainingCount: result.RemainingCount,
		}
	}
	return applied
}

func ownerAppliedDamageFontUnlockResult(opcode uint16, cmd Command, result DamageFontUnlockResult) alignedcmd.Result {
	return alignedcmd.Result{
		Handled:         true,
		ResponseAllowed: true,
		Operation:       cmd.Operation,
		Reason: fmt.Sprintf(
			"damage-font owner applied: slot=%d item=%d font=%d expires_at=%d remaining=%d pvf_path=%q; op515 ACK then op14 slot refresh then NOTI 1239",
			result.SourceSlotIndex, result.ItemID, result.FontIndex, result.ExpiresAt, result.RemainingCount, result.PVFPath,
		),
		UpperResponses: []alignedcmd.UpperResponse{
			class1Response(opcode, buildUseStackableActionSuccessAck(result)),
		},
		ItemSlotRefreshes: []alignedcmd.ItemSlotRefresh{{
			ListType:  listTypeMain,
			SlotIndex: result.SourceSlotIndex,
		}},
		PostActions: []alignedcmd.PostAction{alignedcmd.PostActionRefreshSelectedDamageFontState},
	}
}

func ownerAppliedDamageFontSelectionResult(opcode uint16, cmd Command, result DamageFontSelectionResult) alignedcmd.Result {
	body := buildSelectDamageFontErrorAck(damageFontSelectionErrorUnavailable)
	reason := fmt.Sprintf("damage font %d is not owned or has expired; returned current-EXE error 17", result.FontIndex)
	if result.Success {
		body = buildSelectDamageFontSuccessAck(result.FontIndex)
		reason = fmt.Sprintf("selected damage font %d persisted; returned current-EXE op1288 success ACK", result.FontIndex)
	}
	return alignedcmd.Result{
		Handled:         true,
		ResponseAllowed: true,
		Operation:       cmd.Operation,
		Reason:          reason,
		UpperResponses: []alignedcmd.UpperResponse{
			class1Response(opcode, body),
		},
	}
}

func ownerBlockedResult(cmd Command, err error) alignedcmd.Result {
	return alignedcmd.Result{
		Handled:         true,
		ResponseAllowed: false,
		Operation:       cmd.Operation,
		Reason:          fmt.Sprintf("已解析 %s：%s；inventory owner 未落库：%v；禁止回成功包", cmd.Operation, cmd.String(), err),
	}
}

func ownerAppliedResult(cmd Command, detail string) alignedcmd.Result {
	return alignedcmd.Result{
		Handled:         true,
		ResponseAllowed: false,
		Operation:       cmd.Operation,
		Reason:          fmt.Sprintf("已写入 inventory owner：%s %s；缺少 mutation id 与当前 EXE 的 %s/背包刷新/USERINFO 顺序证据，暂不回成功包", cmd.String(), detail, ackEvidenceLabel(cmd.Operation)),
	}
}

func ownerAppliedDeleteResult(opcode uint16, cmd Command, result DeleteResult) alignedcmd.Result {
	responses := make([]alignedcmd.UpperResponse, 0, 1)
	if len(result.Removed) > 0 {
		responses = append(responses, class1Response(opcode, buildDeleteItemAck(result.Removed)))
	}
	return alignedcmd.Result{
		Handled:         true,
		ResponseAllowed: len(responses) > 0,
		Operation:       cmd.Operation,
		Reason: fmt.Sprintf(
			"inventory owner applied: %s removed=%d changed=%t; opened 0x0012 response in C# order",
			cmd.String(),
			len(result.Removed),
			result.Changed,
		),
		UpperResponses: responses,
	}
}

// buildSortLockAck 是 713/714 的 class1 成功体：u8 1 + u8 list + u16 slot。
// 形状来自客户端 handler 0x1faee80/0x1faeee0（先读 success 字节，再读 list/slot），
// 与旧 C# SortItemLockBuilder.BuildLock 同序；旧 C# 的 714/715 方向在本客户端会反相，不采用。
func buildSortLockAck(listType byte, slotIndex int16) []byte {
	var writer packetWriter
	writer.writeByte(1)
	writer.writeByte(listType)
	writer.writeInt16(slotIndex)
	return writer.bytes()
}

// ownerAppliedSortLockResult 只在 durable owner 返回后开成功回包；幂等命中（Changed=false）
// 同样回成功，因为客户端要的状态已经成立。
//
// 除了客户端 0x1faee80/0x1faeee0 要的 class1 `1 + list + u16 slot` ACK，
// 同一 Result 里再排一条 class0/0x000D 整容器刷新：当前下发客户端的入站准入
// 位图没有 713/714 位，ACK 会在派发前被丢掉，只有整容器刷新（与登录整表同一条
// 权威投影）能让蓝锁图标立即出现，且不会退回会抹金币/降品级的模块快照。
// 依据与边界见 sort_lock.go 的 sortLockContainerRefresh 长注。
func ownerAppliedSortLockResult(opcode uint16, cmd Command, result SortLockResult) alignedcmd.Result {
	responses := []alignedcmd.UpperResponse{
		class1Response(opcode, buildSortLockAck(result.ListType, result.SlotIndex)),
	}
	var authoritativeListRefreshes []byte
	refreshNote := "no container refresh for this list"
	if placeholder, ok := sortLockContainerRefresh(result.ListType); ok {
		responses = append(responses, class0Response(msgItemListRefresh, placeholder))
		authoritativeListRefreshes = []byte{result.ListType}
		refreshNote = fmt.Sprintf("authoritative class0/%d container refresh follows the ACK", msgItemListRefresh)
	}
	return alignedcmd.Result{
		Handled:         true,
		ResponseAllowed: true,
		Operation:       cmd.Operation,
		Reason: fmt.Sprintf(
			"inventory owner applied: %s locked=%t changed=%t; opened class1/%d list+slot ACK in current-EXE order; %s",
			cmd.String(), result.Locked, result.Changed, opcode, refreshNote,
		),
		UpperResponses:             responses,
		AuthoritativeListRefreshes: authoritativeListRefreshes,
	}
}

func class1Response(opcode uint16, body []byte) alignedcmd.UpperResponse {
	return alignedcmd.UpperResponse{
		MsgID:          opcode,
		Body:           body,
		Classification: dnfproto.DefaultChannelClassification,
		AllowCodec:     true,
	}
}

func class0Response(msgID uint16, body []byte) alignedcmd.UpperResponse {
	return alignedcmd.UpperResponse{
		MsgID:          msgID,
		Body:           body,
		Classification: 0,
		AllowCodec:     true,
	}
}

func ackEvidenceLabel(operation string) string {
	switch operation {
	case "delete_item":
		return "0x0012"
	case "sell_item":
		return "0x0016"
	case "move_itemspace":
		return "0x0013"
	case "sort_item":
		return "0x0014"
	case "request_item_sort_lock":
		return "0x02C9 (713)"
	case "request_item_sort_unlock":
		return "0x02CA (714)"
	case "repair_equipment":
		return "0x0017"
	default:
		return "对应 ACK"
	}
}

func blockedParsedResult(operation string, summary string, err error) alignedcmd.Result {
	if err != nil {
		return alignedcmd.Result{
			Handled:         true,
			ResponseAllowed: false,
			Operation:       operation,
			Reason:          fmt.Sprintf("%s 请求体解析失败：%v；禁止回成功包", operation, err),
		}
	}
	return alignedcmd.Result{
		Handled:         true,
		ResponseAllowed: false,
		Operation:       operation,
		Reason:          fmt.Sprintf("已解析 %s：%s；资产写路径需接入 durable owner、事务、幂等和当前 EXE S2C 刷新顺序后才能回包", operation, summary),
	}
}

func ownerRejectedMoveResult(opcode uint16, cmd Command, err error) alignedcmd.Result {
	cargo := cmd.SourceListType == 2 || cmd.SourceListType == 12 || cmd.DestinationListType == 2 || cmd.DestinationListType == 12
	known := errors.Is(err, ErrMoveSlotLocked) || errors.Is(err, ErrMoveItemPlacement) || errors.Is(err, ErrCargoIncompatibleStack) || errors.Is(err, ErrMoveCannotStack) || errors.Is(err, ErrMoveCannotSwap) || errors.Is(err, ErrCargoInsertRejected)
	if cmd.SelectedCharacterID == 0 || (!cargo && !known) || errors.Is(err, ErrUnsupportedList) || errors.Is(err, ErrAccountRequired) {
		return ownerBlockedResult(cmd, err)
	}
	return alignedcmd.Result{
		Handled: true, ResponseAllowed: true, Operation: cmd.Operation,
		Reason:         fmt.Sprintf("物品移动已拒绝，资产保持: %v", err),
		UpperResponses: []alignedcmd.UpperResponse{class1Response(opcode, []byte{0, 17, cmd.SourceListType, cmd.DestinationListType})},
	}
}
