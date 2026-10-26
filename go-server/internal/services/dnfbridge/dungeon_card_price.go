package dnfbridge

import (
	"context"
	"fmt"
	"math"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
)

// 86参考按副本等级查找二元价格表，未列出的等级取向下最近一行，
// 低于首行时取首行。价格与奖励一并冻结，不用角色等级或奖励金币推算。
func currentDungeonPaidCardCost(source dnfpvf.Source, level int64) (int64, error) {
	if source == nil {
		return 0, errDungeonCardPVFSourceRequired
	}
	if level <= 0 {
		return 0, fmt.Errorf("%w: 翻牌副本等级=%d", errDungeonCardPVFSectionShape, level)
	}
	document, err := parseDungeonCardPVFDocument(source, currentDungeonClearRewardRulePath)
	if err != nil {
		return 0, err
	}
	if tokens, found := document.Section("gold card cost table"); found {
		for _, token := range tokens {
			if token.Kind != dnfpvf.TokenInt {
				return 0, fmt.Errorf("%w: 翻牌价格表包含非整数", errDungeonCardPVFSectionShape)
			}
		}
	}
	values, err := dungeonCardPVFSectionInts(document, "gold card cost table")
	if err != nil {
		return 0, err
	}
	if len(values)%2 != 0 {
		return 0, fmt.Errorf("%w: 翻牌价格表字段数=%d", errDungeonCardPVFSectionShape, len(values))
	}
	var cost, previous int64
	for offset := 0; offset < len(values); offset += 2 {
		rowLevel, rowCost := values[offset], values[offset+1]
		if rowLevel <= 0 || rowLevel <= previous || rowCost < 0 || rowCost > math.MaxUint32 {
			return 0, fmt.Errorf("%w: 翻牌价格表行=%d 等级=%d 金币=%d", errDungeonCardPVFSectionShape, offset/2, rowLevel, rowCost)
		}
		if offset == 0 || rowLevel <= level {
			cost = rowCost
		}
		previous = rowLevel
	}
	return cost, nil
}

// 付费牌先提交收费与发奖，再发送成功牌面。保留本局资产回执，
// 使成功牌面或资产通知写失败后可以续传，而不再次收费或发奖。
func (s *Service) grantCurrentDungeonSelectedCardAssetsLocked(
	session *gameSession, runtime *runtimeDungeonState, side dungeonCardSide, memberIndex byte,
) (dungeonCardAssetGrantResult, dungeonCardSelectionResult, bool, error) {
	if side == dungeonCardSidePaid && runtime.settlementPaidCardGrant != nil {
		return *runtime.settlementPaidCardGrant, dungeonCardSelectionReplay, true, nil
	}
	reservation, result, err := runtime.settlementCardRewardState.reserveSelection(side, memberIndex)
	if err != nil {
		return dungeonCardAssetGrantResult{}, result, false, err
	}
	if !reservation.grant {
		return dungeonCardAssetGrantResult{}, result, false, nil
	}
	repositories, ok := s.repositoryGroup()
	if !ok || repositories.CharacterAssets == nil {
		runtime.settlementCardRewardState.finishDelivery(reservation, false)
		return dungeonCardAssetGrantResult{}, result, false, errDungeonCardAssetOwnerUnavailable
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	grant, err := deliverDungeonCardReservation(ctx, runtime.settlementCardRewardState, reservation, repositories, 0, s.gameplayNow().UTC())
	if err != nil {
		return dungeonCardAssetGrantResult{}, result, false, err
	}
	if side == dungeonCardSidePaid {
		runtime.settlementPaidCardGrant = &grant
	}
	return grant, result, true, nil
}

func (s *Service) rejectCurrentDungeonPaidCardSelectionLocked(session *gameSession, runtime *runtimeDungeonState, memberIndex byte) error {
	state := runtime.settlementCardRewardState
	state.mu.Lock()
	if !state.delivered[dungeonCardSidePaid] && !state.deliveryBusy[dungeonCardSidePaid] {
		state.selectedMember[dungeonCardSidePaid] = -1
	}
	state.mu.Unlock()
	if instance := runtime.partyInstance; instance != nil {
		instance.mu.Lock()
		if instance.cardReservations[dungeonCardSidePaid][memberIndex] == session.selectedCharacterID &&
			!instance.cardFlipKnown[dungeonCardSidePaid][memberIndex] {
			instance.cardReservations[dungeonCardSidePaid][memberIndex] = 0
		}
		instance.mu.Unlock()
	}
	runtime.settlementCardSideSelectionKnown[dungeonCardSidePaid] = false
	runtime.settlementCardSideSelectionSent[dungeonCardSidePaid] = false
	runtime.refreshCurrentDungeonFreeCardReadiness()
	// 86拒付时重新投影未选择牌面；使用当前已证明的完整8槽读者，
	// 保留免费行和队友已完成的牌面，不猜金币不足的专用错误编号。
	slots, _ := currentDungeonSelectedCardSlots(runtime, currentDungeonCardFlipRowStateValue(session))
	body, err := buildCurrentDungeonOp71SuccessBody(slots)
	if err != nil {
		return err
	}
	return s.sendGameUpperRawClass(session, uint16(dnfenum.CmdPacketSelectCard), body, currentDungeonCardResponseClass)
}
