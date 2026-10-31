// 本文件是 op438「锻造 / 别升级（separate upgrade）」在 inventory handler 侧的
// 收口：请求体已由 dnfbridge/forging_codec.go 解出（协议形状归桥接层），这里只
// 把字段交给领域 owner，并把结果翻成回包描述。
//
// 回包体本身同样由 dnfbridge 组装（见 dnfbridge/forging_codec.go）：
//
//	校验类拒绝 → class1 失败体 0 + u8 error；
//	成功/掷点失败 → class1 成功体 13 字节（outcome 0/1）。
//
// 三条路径都会结束 UI：客户端失败回调 0x2483b60 的第一件事就是无条件调 0x12030a0
// 复位锻造 UI（先于读错误码/查表），成功回调则按回包体里的 targetList/targetSlot/
// newLevel 就地更新。因此不会出现"客户端永远等回包、窗口关不掉"的卡死。
package inventory

import (
	"context"
	"fmt"
	"math"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
)

// handlerSeparateUpgrade 处理当前 EXE op438。
func handlerSeparateUpgrade(ctx context.Context, req alignedcmd.Request) (alignedcmd.Result, error) {
	cmd := NewSeparateUpgradeCommand(req, req.Forging)
	if req.Forging == nil {
		// 桥侧的 438 解码失败（畸形帧）也走这条：回失败体让 UI 结束，
		// 而不是留一条永远等不到回包的窗口。
		return separateUpgradeRejectionResult(cmd, forgingErrorInvalidTarget, "forging_request_not_decoded"), nil
	}
	if req.ForgingResolver == nil {
		return separateUpgradeRejectionResult(cmd, forgingErrorUnsupported, "forging_resolver_missing"), nil
	}
	owner, err := NewOwner(req.Repositories)
	if err != nil {
		return separateUpgradeRejectionResult(cmd, forgingErrorMaterialCommit, "inventory_owner_unavailable"), nil
	}
	result, err := owner.SeparateUpgrade(ctx, cmd, req.ForgingResolver)
	if err != nil {
		// 事务已经回滚（资产未动），但客户端那条窗口必须被关掉：回失败体并记具名原因。
		// 这条与 op50 不同是有意的——op438 的现场缺陷就是"窗口关不掉"，
		// 只有回失败 ACK（0 + u8 error）才能让回调 0x2483b60 复位锻造 UI。
		return separateUpgradeRejectionResult(cmd, forgingErrorMaterialCommit, "storage_error: "+err.Error()), nil
	}
	return ownerAppliedSeparateUpgradeResult(cmd, result), nil
}

// separateUpgradeRejectionResult 组装"校验类拒绝/存储失败"的回包描述：
// 领域层没有 logger，所以具名原因压进 reason=（复测时直接 grep）。
func separateUpgradeRejectionResult(cmd Command, errorCode byte, reason string) alignedcmd.Result {
	return alignedcmd.Result{
		Handled:         true,
		ResponseAllowed: true,
		Operation:       cmd.Operation,
		Reason: fmt.Sprintf(
			"inventory owner rejected separate upgrade: %s error=%d reason=%s; returned current-EXE op438 failure body 0+u8 error",
			cmd.String(),
			errorCode,
			reason,
		),
		Forging: &alignedcmd.ForgingOutcome{
			Rejected:     true,
			ErrorCode:    errorCode,
			TargetList:   cmd.TargetListType,
			TargetSlot:   cmd.TargetSlotIndex,
			MaterialSlot: cmd.MaterialSlotIndex,
		},
	}
}

// ownerAppliedSeparateUpgradeResult 把领域结果翻成 class1 438 的描述 + 改动行增量。
//
// 逐槽 op14 增量只覆盖**主背包 list 0**：材料槽恒在 list 0，目标在主背包时也在
// list 0。穿戴目标（list 3）不在那条通道里（dnfbridge 对 list-3 单行 op14 的既有
// 结论是"不可用/已抑制"），它由 ACK 自带 targetList/targetSlot/newLevel 让客户端
// 就地更新。
func ownerAppliedSeparateUpgradeResult(cmd Command, result SeparateUpgradeResult) alignedcmd.Result {
	outcome := &alignedcmd.ForgingOutcome{
		Rejected:          result.Rejected,
		ErrorCode:         result.ErrorCode,
		TargetList:        result.TargetListType,
		TargetSlot:        result.TargetSlotIndex,
		OldLevel:          result.OldLevel,
		Succeeded:         result.Succeeded,
		NewLevel:          result.NewLevel,
		MaterialSlot:      result.MaterialSlotIndex,
		MaterialRemaining: clampUint32(result.MaterialRemainingStackCount),
		MaterialConsumed:  result.MaterialCost,
		PortableFurnace:   result.PortableFurnace,
		TicketApplied:     result.TicketApplied,
		LevelChanged:      result.LevelChanged,
	}
	response := alignedcmd.Result{
		Handled:         true,
		ResponseAllowed: true,
		Operation:       cmd.Operation,
		Reason: fmt.Sprintf(
			"inventory owner applied separate upgrade: %s %s; returned current-EXE op438 class1 body",
			cmd.String(),
			forgingResultReason(result),
		),
		Forging: outcome,
	}
	if !result.Success {
		return response
	}
	// 86普通锻造达到5级后成功/失败均播报；券仅成功达到5级才播报。
	level := result.NewLevel
	if !result.Succeeded {
		level = result.OldLevel
	}
	if level >= 5 && (!result.TicketApplied || result.Succeeded) {
		if body, err := buildEquipmentUpgradeAnnouncementBody(14, result.Succeeded, cmd.SelectedCharacterID,
			int32(result.TargetItemID), upgradeLevelOf(result.TargetUpdatedStack), result.NewLevel, result.TargetUpdatedStack); err == nil {
			response.UpperResponses = append(response.UpperResponses, class0Response(msgUpgradeNotice, body))
			response.EquipmentUpgradeNotice = &alignedcmd.EquipmentUpgradeNotice{Kind: "forging", ItemID: result.TargetItemID, Level: level, Succeeded: result.Succeeded, Body: body}
		}
	}
	if result.MaterialUpdated && result.MaterialSlotIndex >= 0 {
		response.ItemSlotRefreshes = append(response.ItemSlotRefreshes, alignedcmd.ItemSlotRefresh{
			ListType:  listTypeMain,
			SlotIndex: result.MaterialSlotIndex,
		})
	}
	if result.LevelChanged && result.TargetListType == listTypeMain && result.TargetSlotIndex >= 0 {
		response.ItemSlotRefreshes = append(response.ItemSlotRefreshes, alignedcmd.ItemSlotRefresh{
			ListType:  listTypeMain,
			SlotIndex: result.TargetSlotIndex,
		})
	}
	return response
}

// clampUint32 把材料剩余数量夹进回包字段（当前客户端读 u32）能表达的范围。
func clampUint32(value int64) uint32 {
	if value <= 0 {
		return 0
	}
	if value > math.MaxUint32 {
		return math.MaxUint32
	}
	return uint32(value)
}
