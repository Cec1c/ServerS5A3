package dnfbridge

import (
	"context"
	"strconv"
	"strings"

	"longheng.io/server/internal/modules/dnf/inventory"
	"longheng.io/server/internal/modules/dnf/itemtrade"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

func (s *Service) onlineWarehouseTradeOwnersReady(trade *onlineItemTrade) bool {
	shared := false
	for _, offers := range trade.offers {
		for _, offer := range offers {
			shared = shared || repo.IsAccountSharedInventorySlot(offer.sourceList, offer.sourceSlot)
		}
	}
	if !shared {
		return true
	}
	for _, id := range trade.participants {
		session, online := s.onlineGameSession(id)
		if !online || session.selectedCharacterID != id || !session.loginPacketSeen || !session.accountFromToken || trade.accounts[id] == "" || strings.TrimSpace(session.accountID) != trade.accounts[id] {
			return false
		}
	}
	return true
}

func (s *Service) loadOnlineItemTradeOfferStack(ctx context.Context, repositories repo.Group, session *gameSession, request inventory.MoveItemspaceRequest) (repo.ItemStack, bool, error) {
	key := repo.AccountSharedInventorySlotKey(request.SourceSlotIndex)
	characterID := strconv.Itoa(int(session.selectedCharacterID))
	if !repo.IsAccountSharedInventorySlot(request.SourceListType, request.SourceSlotIndex) {
		record, found, err := repositories.Inventory.Load(ctx, characterID)
		if err != nil || !found {
			return repo.ItemStack{}, false, err
		}
		stack, found := record.Slots[strconv.Itoa(int(request.SourceListType))+":"+strconv.Itoa(int(request.SourceSlotIndex))]
		return stack, found, nil
	}
	// A warehouse offer requires its real authenticated account owner. Never
	// trade a stale character copy or use the shared-account fallback.
	accountID := strings.TrimSpace(session.accountID)
	if s.options.currentClientVersion != "official-24.9" || !session.loginPacketSeen || !session.accountFromToken || accountID == "" || repositories.Character == nil || repositories.AccountInventory == nil {
		return repo.ItemStack{}, false, nil
	}
	if _, ok := repositories.CharacterTrade.(repo.CharacterWarehouseTradeUnitOfWork); !ok {
		return repo.ItemStack{}, false, nil
	}
	character, found, err := repositories.Character.Load(ctx, characterID)
	if err != nil || !found || strings.TrimSpace(character.AccountID) != accountID {
		return repo.ItemStack{}, false, err
	}
	account, found, err := repositories.AccountInventory.Load(ctx, accountID)
	if err != nil || !found || account.AccountID != accountID {
		return repo.ItemStack{}, false, err
	}
	stack, found := account.Slots[key]
	if !found {
		return repo.ItemStack{}, false, nil
	}
	if slot, ok := itemtrade.TradableCrystalSlot(stack.ItemID); !ok || slot != request.SourceSlotIndex {
		return repo.ItemStack{}, false, nil
	}
	catalog, err := s.currentPVFItemCatalog()
	if err != nil {
		return repo.ItemStack{}, false, err
	}
	definition, err := catalog.ResolveItem(uint32(stack.ItemID))
	if err != nil || !currentDungeonDiscardTradeable(definition) {
		return repo.ItemStack{}, false, err
	}
	return stack, true, nil
}
