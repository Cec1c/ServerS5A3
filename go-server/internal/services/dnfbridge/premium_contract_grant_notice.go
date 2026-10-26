package dnfbridge

import (
	"fmt"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/premium"
)

// sendAlignedPremiumContractActivations 补发"契约物品在**发放期**就直接叠加账号
// 时长"的客户端通知。
//
// 背景（2026-09-25 线上取证）：赛丽亚的幸运/礼盒一发契约就当普通可堆叠道具入包，
// 背包满时被 packageitem 的 overflow 分支转成「背包已满：礼盒奖励」邮件，时长根本
// 没有叠加。修法是让发放事务在任何槽位规划之前把契约分流成账号时长
// （premium.ActivateContractGrant），代价是**没有背包行**——所以激活通知必须由
// bridge 在 ACK 之后自己补，和 op14 拦截路径
// （premium_inventory_activation.go 的 sendCurrentPremiumInventoryActivations）
// 用同一套当前 EXE 报文：u8 范围内的类型一发一条 class0 激活通知
// （NOTI 0x42，body = u16 2 + u8 type + i64LE 剩余秒），晶之契约（97）额外再补一次
// class1/op898 状态，让客户端立刻绑定晶体选择。
func (s *Service) sendAlignedPremiumContractActivations(
	session *gameSession,
	operation string,
	activations []alignedcmd.PremiumContractActivation,
) error {
	if s == nil || session == nil || len(activations) == 0 {
		return nil
	}
	nowUnix := s.gameplayNow().UTC().Unix()
	refreshCrystal := false
	for _, activation := range activations {
		remaining := activation.ExpireAt - nowUnix
		if remaining < 0 {
			remaining = 0
		}
		if premium.CanNotifyActivation(activation.PremiumType) {
			if err := s.sendGameUpperRawClassCodec(
				session,
				currentPremiumActivatedMsgID,
				buildCurrentPremiumActivatedBody(activation.PremiumType, remaining),
				0,
				true,
			); err != nil {
				return err
			}
		}
		if activation.PremiumType == premium.TypeCrystal {
			refreshCrystal = true
		}
	}
	s.logGameEvent(session, "game-aligned-premium-contract-activated",
		"source", "grant_time_contract_item_before_slot_planning",
		"operation", operation,
		"char_id", session.selectedCharacterID,
		"activation_count", len(activations),
		"activations", fmt.Sprint(activations))
	if refreshCrystal {
		s.requestCurrentCrystalContractTimerRefresh(session)
		return s.sendCurrentCrystalContractState(session, "aligned_"+operation+"_after_type97_grant_activation")
	}
	return nil
}
