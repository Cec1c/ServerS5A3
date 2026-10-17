package dnfbridge

import (
	"context"
	"fmt"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	dnfinventory "longheng.io/server/internal/modules/dnf/inventory"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrevivecoin "longheng.io/server/internal/modules/dnf/revivecoin"
)

// 当前客户端右键42实际发送op30，仅携带主背包源格。
// 86JP MR27 也是在此入口转入复活币发放；90版复用自身钱包事务和op14布局。
func (s *Service) handleCurrentReviveCoinItemUse(ctx context.Context, repositories dnfrepo.Group, session *gameSession, slot int16) error {
	const itemID = dnfrevivecoin.ConsumableItemID
	reject := func(reason string, err error) error {
		return s.answerRejectedExperienceItem(session, slot, itemID, reason, err)
	}
	catalog, err := s.currentPVFItemCatalog()
	if err != nil || catalog == nil {
		return reject("revive_coin_pvf_unavailable", err)
	}
	definition, err := catalog.ResolveItem(uint32(itemID))
	if err != nil {
		return reject("revive_coin_definition_unavailable", err)
	}
	if definition.Kind != dungeonDropItemStackable {
		return reject("revive_coin_identity_mismatch", fmt.Errorf("复活币PVF身份不是堆叠物品"))
	}
	owner, err := dnfinventory.NewOwner(repositories)
	if err != nil {
		return reject("revive_coin_owner_unavailable", err)
	}
	result, err := owner.UseReviveCoinItem(ctx, dnfinventory.ReviveCoinUseCommand{
		AccountID: s.accountIDForSession(session), CharacterID: session.selectedCharacterID, SlotIndex: slot,
		Definition: alignedcmd.StackableUseProvenanceResolution{ItemID: itemID, Found: true,
			PVFPath: definition.PVFPath, StackableType: definition.StackableType},
		Now: s.gameplayNow().UTC(),
	})
	if err != nil {
		return reject("revive_coin_domain_rejected", err)
	}
	s.logGameEvent(session, "game-increase-status-revive-coin-committed",
		"message", "使用一件复活币道具，钱包增加一枚，事务已提交",
		"slot_index", slot, "item_id", result.ItemID, "remaining", result.RemainingCount,
		"wallet_total", result.ReviveCoinWalletTotal,
		"sequence", "op30_ack_then_op14_source_and_wallet")
	if err := s.sendGameUpperSuccess(session, 30, successExperienceItemBody(session.selectedCharacterID)); err != nil {
		return err
	}
	return s.sendSelectedIncrementalItemSlotRefreshes(session, "increase_status_revive_coin_use", []alignedcmd.ItemSlotRefresh{
		{ListType: dnfrepo.MainInventoryListType, SlotIndex: slot},
		{ListType: dnfrepo.MainInventoryListType, SlotIndex: dnfrevivecoin.WalletSlot},
	})
}
