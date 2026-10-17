package skillcmd

import (
	"context"
	"errors"
	"fmt"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
)

type handler struct{}

func NewHandler() alignedcmd.Handler {
	return handler{}
}

func (handler) Domain() dnfenum.AlignedDomain {
	return dnfenum.AlignedDomainSkill
}

func (handler) Handle(ctx context.Context, req alignedcmd.Request) (alignedcmd.Result, error) {
	switch dnfenum.CmdPacket(req.Opcode) {
	case dnfenum.CmdPacketChangeSkillslot:
		parsed, err := DecodeChangeSkillSlotRequest(req.Body)
		cmd := NewSlotCommand(req, parsed)
		return applySlotOwner(ctx, req, cmd, err), nil
	case dnfenum.CmdPacketBuySkill:
		parsed, err := DecodeBuySkillRequest(req.Body)
		cmd := NewBuyCommand(req, parsed)
		return applyBuyOwner(ctx, req, cmd, err), nil
	case dnfenum.CmdPacketSkillInit:
		parsed, err := DecodeSkillInitRequest(req.Body)
		cmd := NewResetCommand(req, parsed)
		return applyResetOwner(ctx, req, cmd, err), nil
	case dnfenum.CmdPacketChangeAnotherSkillTree:
		parsed, err := DecodeChangeAnotherSkillTreeRequest(req.Body)
		cmd := NewTreeCommand(req, parsed)
		return applyTreeOwner(ctx, req, cmd, err), nil
	case dnfenum.CmdPacketSkillCommandCustomizing:
		parsed, err := DecodeSkillCommandRequest(req.Body)
		cmd := NewSkillCommand(req, parsed)
		return applySkillCommandOwner(ctx, req, cmd, err), nil
	case dnfenum.CmdPacketSkillCommandAllDefault:
		cmd := NewSkillCommandDefaultsCommand(req)
		if len(req.Body) != 0 {
			return blockedParsedResult(cmd.Operation, cmd.String(), fmt.Errorf("invalid body length: got %d want 0", len(req.Body))), nil
		}
		return applySkillCommandDefaultsOwner(ctx, req, cmd), nil
	default:
		return alignedcmd.Result{
			Handled:         false,
			ResponseAllowed: false,
			Operation:       "unregistered",
			Reason:          fmt.Sprintf("skill module has no opcode %d handler", req.Opcode),
		}, nil
	}
}

func applyTreeOwner(ctx context.Context, req alignedcmd.Request, cmd Command, parseErr error) alignedcmd.Result {
	if parseErr != nil {
		return treeSwitchFailureResult(req, cmd, parseErr)
	}
	owner, err := NewOwner(req.Repositories, OwnerOptions{
		Catalog: req.SkillCatalog, InitialLevels: req.InitialSkillLevels, PointBaseline: req.SkillPointBaseline,
	})
	if err != nil {
		return treeSwitchFailureResult(req, cmd, err)
	}
	result, err := owner.ApplyTreeSwitch(ctx, cmd)
	if err != nil {
		return treeSwitchFailureResult(req, cmd, planError(cmd.Operation, err))
	}
	body, err := BuildChangeAnotherSkillTreeSuccess(result)
	if err != nil {
		return treeSwitchFailureResult(req, cmd, planError(cmd.Operation, err))
	}
	return alignedcmd.Result{
		Handled:         true,
		ResponseAllowed: true,
		Operation:       cmd.Operation,
		Reason: fmt.Sprintf(
			"applied %s: char=%s current=%d target=%d; persisted before current EXE op260 success",
			cmd.Operation, result.CharacterID, result.Current, result.Target,
		),
		PostActions: []alignedcmd.PostAction{alignedcmd.PostActionRefreshSelectedActorSkills},
		UpperResponses: []alignedcmd.UpperResponse{{
			MsgID:          req.Opcode,
			Body:           body,
			Classification: dnfproto.DefaultChannelClassification,
			AllowCodec:     true,
		}},
	}
}

func treeSwitchFailureResult(req alignedcmd.Request, cmd Command, err error) alignedcmd.Result {
	return alignedcmd.Result{
		Handled:         true,
		ResponseAllowed: true,
		Operation:       cmd.Operation,
		Reason:          fmt.Sprintf("rejected %s: %v; current EXE op260 failure 19 returned", cmd.Operation, err),
		UpperResponses: []alignedcmd.UpperResponse{{
			MsgID:          req.Opcode,
			Body:           BuildChangeAnotherSkillTreeFailure(),
			Classification: dnfproto.DefaultChannelClassification,
			AllowCodec:     true,
		}},
	}
}

func applyResetOwner(ctx context.Context, req alignedcmd.Request, cmd Command, parseErr error) alignedcmd.Result {
	if parseErr != nil {
		return blockedParsedResult(cmd.Operation, cmd.String(), parseErr)
	}
	owner, err := NewOwner(req.Repositories, OwnerOptions{
		Catalog:       req.SkillCatalog,
		InitialLevels: req.InitialSkillLevels,
		PointBaseline: req.SkillPointBaseline,
	})
	if err != nil {
		return ownerBlockedResult(cmd, err)
	}
	result, err := owner.ApplyReset(ctx, cmd)
	if err != nil {
		return ownerBlockedResult(cmd, planError(cmd.Operation, err))
	}
	body, err := BuildSkillInitSuccess(result)
	if err != nil {
		return ownerBlockedResult(cmd, planError(cmd.Operation, err))
	}
	return alignedcmd.Result{
		Handled:         true,
		ResponseAllowed: true,
		Operation:       cmd.Operation,
		Reason: fmt.Sprintf(
			"applied %s: char=%s tree=%d mode=%d initial_skills=%d remainSP=%d remainTP=%d; current EXE op491 response opened",
			cmd.Operation, result.CharacterID, result.SkillTree, result.Mode, result.SkillCount,
			result.Points.RemainingSP, result.Points.RemainingTP,
		),
		UpperResponses: []alignedcmd.UpperResponse{{
			MsgID:          req.Opcode,
			Body:           body,
			Classification: dnfproto.DefaultChannelClassification,
			AllowCodec:     true,
		}},
		PostActions: []alignedcmd.PostAction{alignedcmd.PostActionRefreshSelectedActorSkills},
	}
}

func applySlotOwner(ctx context.Context, req alignedcmd.Request, cmd Command, parseErr error) alignedcmd.Result {
	if parseErr != nil {
		return blockedParsedResult(cmd.Operation, cmd.String(), parseErr)
	}
	owner, err := NewOwner(req.Repositories, OwnerOptions{
		Catalog:       req.SkillCatalog,
		InitialLevels: req.InitialSkillLevels,
	})
	if err != nil {
		return ownerBlockedResult(cmd, err)
	}
	result, err := owner.ApplySlot(ctx, cmd)
	if err != nil {
		return ownerBlockedResult(cmd, planError(cmd.Operation, err))
	}
	body, err := BuildChangeSkillSlotSuccess(result)
	if err != nil {
		return ownerBlockedResult(cmd, planError(cmd.Operation, err))
	}
	return alignedcmd.Result{
		Handled:         true,
		ResponseAllowed: true,
		Operation:       cmd.Operation,
		Reason: fmt.Sprintf(
			"applied %s: char=%s tree=%d from=%d to=%d source_skill=%d destination_skill=%d destination_occupied=%t; current EXE op28 response opened",
			cmd.Operation,
			result.CharacterID,
			result.SkillTree,
			result.From,
			result.To,
			result.FromSkillID,
			result.ToSkillID,
			result.ToOccupied,
		),
		UpperResponses: []alignedcmd.UpperResponse{{
			MsgID:          req.Opcode,
			Body:           body,
			Classification: dnfproto.DefaultChannelClassification,
			AllowCodec:     true,
		}},
	}
}

func applyBuyOwner(ctx context.Context, req alignedcmd.Request, cmd Command, parseErr error) alignedcmd.Result {
	if parseErr != nil {
		return buySkillFailureResult(req, cmd, parseErr)
	}
	if err := validateBuySkillTree(cmd.SkillTree); err != nil {
		return buySkillFailureResult(req, cmd, err)
	}
	owner, err := NewOwner(req.Repositories, OwnerOptions{
		Catalog:       req.SkillCatalog,
		InitialLevels: req.InitialSkillLevels,
		PointBaseline: req.SkillPointBaseline,
	})
	if err != nil {
		return buySkillFailureResult(req, cmd, err)
	}
	result, err := owner.ApplyBuy(ctx, cmd)
	if err != nil {
		return buySkillFailureResult(req, cmd, planError(cmd.Operation, err))
	}
	body, err := BuildBuySkillSuccess(result)
	if err != nil {
		return buySkillFailureResult(req, cmd, planError(cmd.Operation, err))
	}
	postActions := []alignedcmd.PostAction{}
	if result.ConsumedRefundItem {
		// The 遗忘河之水 stack changed in the same transaction; the main list
		// must refresh or the client keeps showing the old count (86JP sends
		// SendUpdateItemList for the consumed water slot).
		postActions = append(postActions, alignedcmd.PostActionRefreshSelectedItemContainers)
	}
	if result.ExpiredContractSkillsReset {
		// The 达人契约 expiry sweep removed skills before this batch; the buy
		// ACK only carries batch entries, so schedule the full skill refresh.
		postActions = append(postActions, alignedcmd.PostActionRefreshSelectedActorSkills)
	}
	if result.AppliedRefund {
		// 降级（refund 条目）把等级写低后也必须重画技能页：op29 的 ACK 只带本
		// 批次条目，客户端要按持久化等级重建技能列表、被动加成和 SP 显示，否则
		// 面板继续显示旧等级（现场：改成 18 后点学习又变回 20）。厂商 9.16 在同
		// 一位置发 op19（D:\DNF90-test runtime packet_log.txt:14670/14697
		// post_action=refresh_selected_actor_skills，随后
		// source=aligned_buy_skill_after_ack 的 game-main-skill-info-send）。
		// 纯升级批次不带该 action，升级路径逐字节不变。
		postActions = append(postActions, alignedcmd.PostActionRefreshSelectedActorSkills)
	}
	return alignedcmd.Result{
		Handled:         true,
		ResponseAllowed: true,
		Operation:       cmd.Operation,
		Reason: fmt.Sprintf(
			"applied %s: char=%s tree=%d entries=%d remainSP=%d remainTP=%d refund_water=%t; current EXE op29 response opened",
			cmd.Operation,
			result.CharacterID,
			result.SkillTree,
			len(result.Entries),
			result.Points.RemainingSP,
			result.Points.RemainingTP,
			result.ConsumedRefundItem,
		),
		UpperResponses: []alignedcmd.UpperResponse{{
			MsgID:          req.Opcode,
			Body:           body,
			Classification: dnfproto.DefaultChannelClassification,
			AllowCodec:     true,
		}},
		PostActions: postActions,
	}
}

// applySkillCommandOwner persists the op331 skill-command vector and answers
// the pending request. The current EXE reader sub_1D0CEE0 is ACK-only, and the
// 86JP/A21 reference answers `0x01 + 原样回显请求体`. The ACK body must echo the
// semantic body exactly as received, so it is rebuilt from the raw request.
func applySkillCommandOwner(ctx context.Context, req alignedcmd.Request, cmd Command, parseErr error) alignedcmd.Result {
	if parseErr != nil {
		return blockedParsedResult(cmd.Operation, cmd.String(), parseErr)
	}
	owner, err := NewOwner(req.Repositories)
	if err != nil {
		return ownerBlockedResult(cmd, err)
	}
	result, err := owner.ApplySkillCommands(ctx, cmd)
	if err != nil {
		return ownerBlockedResult(cmd, planError(cmd.Operation, err))
	}
	return alignedcmd.Result{
		Handled:         true,
		ResponseAllowed: true,
		Operation:       cmd.Operation,
		Reason: fmt.Sprintf(
			"applied %s: char=%s records=%d stored=%d; current EXE op331 ACK echoes the request body",
			cmd.Operation, result.CharacterID, len(result.Applied), result.Total,
		),
		UpperResponses: []alignedcmd.UpperResponse{{
			MsgID:          req.Opcode,
			Body:           buildSkillCommandAck(req.Body),
			Classification: dnfproto.DefaultChannelClassification,
			AllowCodec:     true,
		}},
	}
}

// applySkillCommandDefaultsOwner clears every persisted per-skill command
// (op332 "全部默认") and answers with the reference's one-byte success body.
func applySkillCommandDefaultsOwner(ctx context.Context, req alignedcmd.Request, cmd Command) alignedcmd.Result {
	owner, err := NewOwner(req.Repositories)
	if err != nil {
		return ownerBlockedResult(cmd, err)
	}
	result, err := owner.ClearSkillCommands(ctx, cmd)
	if err != nil {
		return ownerBlockedResult(cmd, planError(cmd.Operation, err))
	}
	return alignedcmd.Result{
		Handled:         true,
		ResponseAllowed: true,
		Operation:       cmd.Operation,
		Reason: fmt.Sprintf(
			"applied %s: char=%s cleared=%d; current EXE op332 ACK",
			cmd.Operation, result.CharacterID, result.Cleared,
		),
		UpperResponses: []alignedcmd.UpperResponse{{
			MsgID:          req.Opcode,
			Body:           []byte{1},
			Classification: dnfproto.DefaultChannelClassification,
			AllowCodec:     true,
		}},
	}
}

// buildSkillCommandAck matches 86JP SkillHandler.Handle_CHANGE_SKILL_COMMAND:
// `ack[0] = 0x01` followed by the untouched request body.
func buildSkillCommandAck(body []byte) []byte {
	ack := make([]byte, 1, 1+len(body))
	ack[0] = 1
	return append(ack, body...)
}

func applyOwner(ctx context.Context, req alignedcmd.Request, cmd Command, parseErr error) alignedcmd.Result {
	if parseErr != nil {
		return blockedParsedResult(cmd.Operation, cmd.String(), parseErr)
	}
	owner, err := NewOwner(req.Repositories, OwnerOptions{
		Catalog:       req.SkillCatalog,
		InitialLevels: req.InitialSkillLevels,
	})
	if err != nil {
		return ownerBlockedResult(cmd, err)
	}
	result, err := owner.Plan(ctx, cmd)
	if err != nil {
		return ownerBlockedResult(cmd, planError(cmd.Operation, err))
	}
	return ownerPlannedResult(cmd, result)
}

// buySkillFailureCode maps a skill-owner rejection to the current client's op29
// resource/error code. A21's BuySkillAckBuilder writes [0x00, errorCode] for
// every rejection and its BuySkillService uses:
//
//	1  = no free skill slot
//	2  = not enough SP/TP
//	3  = no 遗忘河之水, or the alternate skill tree is locked
//	18 = required level or prerequisite not satisfied
func buySkillFailureCode(err error) byte {
	switch {
	case err == nil:
		return 0
	case errors.Is(err, ErrSkillPoints):
		return 2
	case errors.Is(err, ErrSkillSlot), errors.Is(err, ErrSkillSlotContext), errors.Is(err, ErrSkillSlotMode):
		return 1
	case errors.Is(err, ErrSkillLevel), errors.Is(err, ErrSkillGrowType),
		errors.Is(err, ErrSkillPrerequisite), errors.Is(err, ErrSkillDelta):
		return 18
	default:
		return 3
	}
}

// buySkillFailureResult answers the pending class1/op29 with the client's real
// failure body instead of silence. A blocked ack leaves the current EXE's skill
// window waiting forever — live evidence (2026-09-24): clicking 学习 on 圣洁之光
// as grow_type 0 produced only
// "skill grow type rule rejected mutation: skill=16 grow=0; success ACK is
// blocked" in the packet log, and the client's skill panel froze until it was
// closed by hand. The mutation is still not committed; only the refusal is now
// answered.
func buySkillFailureResult(req alignedcmd.Request, cmd Command, err error) alignedcmd.Result {
	code := buySkillFailureCode(err)
	return alignedcmd.Result{
		Handled:         true,
		ResponseAllowed: true,
		Operation:       cmd.Operation,
		Reason: fmt.Sprintf(
			"rejected %s: %v; current EXE op29 failure code %d returned, mutation not committed",
			cmd.Operation, err, code,
		),
		UpperResponses: []alignedcmd.UpperResponse{{
			MsgID:          req.Opcode,
			Body:           BuildBuySkillFailure(code),
			Classification: dnfproto.DefaultChannelClassification,
			AllowCodec:     true,
		}},
	}
}

func blockedParsedResult(operation string, summary string, err error) alignedcmd.Result {
	if err != nil {
		return alignedcmd.Result{
			Handled:         true,
			ResponseAllowed: false,
			Operation:       operation,
			Reason:          fmt.Sprintf("%s body parse failed: %v; success ACK is blocked", operation, err),
		}
	}
	return alignedcmd.Result{
		Handled:         true,
		ResponseAllowed: false,
		Operation:       operation,
		Reason:          fmt.Sprintf("parsed %s: %s; skill state, SP/TP, and NOTI 0x13 refresh are not closed, success ACK is blocked", operation, summary),
	}
}

func ownerBlockedResult(cmd Command, err error) alignedcmd.Result {
	return alignedcmd.Result{
		Handled:         true,
		ResponseAllowed: false,
		Operation:       cmd.Operation,
		Reason:          fmt.Sprintf("parsed %s: %s; skill owner preflight failed: %v; success ACK is blocked", cmd.Operation, cmd.String(), err),
	}
}

func ownerPlannedResult(cmd Command, result PlanResult) alignedcmd.Result {
	return alignedcmd.Result{
		Handled:         true,
		ResponseAllowed: false,
		Operation:       cmd.Operation,
		Reason: fmt.Sprintf(
			"parsed %s: %s; skill owner verified account=%s char=%s known=%t skillCount=%d cooldownCount=%d requested=%v refunds=%d; SP/TP persistence and raw 0x13 refresh are not closed, success ACK is blocked",
			cmd.Operation,
			cmd.String(),
			result.AccountID,
			result.CharacterID,
			result.Known,
			result.SkillCount,
			result.CooldownCount,
			result.RequestedSkillIDs,
			result.RefundCount,
		),
	}
}
