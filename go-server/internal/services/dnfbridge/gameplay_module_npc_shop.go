package dnfbridge

import (
	"context"
	"errors"
	"strconv"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfnpcshop "longheng.io/server/internal/modules/dnf/npcshop"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func npcShopGameplayModule() gameplayModuleDefinition {
	buyOpcode := uint16(dnfenum.CmdPacketBuyItem)
	purchaseCountOpcode := uint16(dnfenum.CmdPacketShopPurchaseCount)
	sellOpcode := uint16(dnfenum.CmdPacketSellItem)
	return gameplayModuleDefinition{
		Name: "npc-shop",
		LegacyHandlers: map[uint16]gameplayHandler{
			buyOpcode: func(service *Service, session *gameSession, request gameplayRequest) error {
				return service.handleCurrentNPCShopBuy(session, request.Body)
			},
			purchaseCountOpcode: func(service *Service, session *gameSession, request gameplayRequest) error {
				return service.handleCurrentNPCShopPurchaseCount(session, request.Body)
			},
			sellOpcode: func(service *Service, session *gameSession, request gameplayRequest) error {
				return service.handleCurrentNPCShopSell(session, request.Body)
			},
		},
		UpperHandlers: map[uint16]gameplayHandler{
			buyOpcode: func(service *Service, session *gameSession, request gameplayRequest) error {
				if !requireDefaultGameplayClassification(service, session, request, "game-npc-shop-buy-blocked", "current_exe_op21_command_class_mismatch") {
					return nil
				}
				return service.handleCurrentNPCShopBuy(session, request.Body)
			},
			purchaseCountOpcode: func(service *Service, session *gameSession, request gameplayRequest) error {
				if !requireDefaultGameplayClassification(service, session, request, "game-npc-shop-purchase-count-blocked", "current_exe_op715_command_class_mismatch") {
					return nil
				}
				return service.handleCurrentNPCShopPurchaseCount(session, request.Body)
			},
			sellOpcode: func(service *Service, session *gameSession, request gameplayRequest) error {
				if !requireDefaultGameplayClassification(service, session, request, "game-npc-shop-sell-blocked", "current_exe_op22_command_class_mismatch") {
					return nil
				}
				return service.handleCurrentNPCShopSell(session, request.Body)
			},
		},
		LegacyNormalizers: map[uint16]gameplayLegacyNormalizer{
			buyOpcode: func(body []byte) []byte {
				return stripLegacyTransportTrailer(body, currentNPCShopBuyRequestWireSize)
			},
			purchaseCountOpcode: func(body []byte) []byte {
				return stripLegacyTransportTrailer(body, currentNPCShopPurchaseCountRequestWireSize)
			},
			sellOpcode: func(body []byte) []byte {
				return stripLegacyTransportTrailer(body, currentNPCShopSellRequestWireSize)
			},
		},
	}
}

func (s *Service) handleCurrentNPCShopBuy(session *gameSession, body []byte) error {
	request, err := parseCurrentNPCShopBuyRequest(body)
	if err != nil {
		s.logGameEvent(session, "game-npc-shop-buy-rejected", "body_len", len(body), "reason", err)
		return s.sendCurrentNPCShopFailure(session, uint16(dnfenum.CmdPacketBuyItem), currentNPCShopBuyErrorCode)
	}
	shop, err := s.currentNPCShopCatalog()
	if err != nil {
		s.logGameEvent(session, "game-npc-shop-buy-rejected", "item_id", request.ItemID, "count", request.Count, "reason", err)
		return s.sendCurrentNPCShopFailure(session, uint16(dnfenum.CmdPacketBuyItem), currentNPCShopBuyErrorCode)
	}
	items, err := s.currentPVFItemCatalog()
	if err != nil {
		s.logGameEvent(session, "game-npc-shop-buy-rejected", "item_id", request.ItemID, "count", request.Count, "reason", err)
		return s.sendCurrentNPCShopFailure(session, uint16(dnfenum.CmdPacketBuyItem), currentNPCShopBuyErrorCode)
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	result, err := s.commitCurrentNPCShopBuy(ctx, session, shop, items, request)
	if err != nil {
		s.logGameEvent(session, "game-npc-shop-buy-rejected", "item_id", request.ItemID, "count", request.Count, "shop_context", request.ShopContext, "actor_context", request.ActorContext, "reason", err)
		if errors.Is(err, dnfnpcshop.ErrPurchaseLimit) {
			return s.sendCurrentNPCShopFailure(session, uint16(dnfenum.CmdPacketBuyItem), currentNPCShopPurchaseLimitErrorCode)
		}
		return s.sendCurrentNPCShopFailure(session, uint16(dnfenum.CmdPacketBuyItem), currentNPCShopBuyErrorCode)
	}
	if err := s.sendGameUpperRawClass(session, uint16(dnfenum.CmdPacketBuyItem), buildCurrentNPCShopBuySuccessBody(result), dnfproto.DefaultChannelClassification); err != nil {
		return err
	}
	if err := s.sendGameUpperRawClass(session, uint16(dnfenum.CmdPacketWalkoutPartyMember), buildCurrentItemUpdateBody(dnfrepo.MainInventoryListType, result.Updates), 0); err != nil {
		return err
	}
	s.logGameEvent(session, "game-npc-shop-buy-committed", "item_id", result.ItemID, "count", result.Count, "slot", result.Slot, "gold_after", result.GoldAfter, "incremental_entries", len(result.Updates), "ack_body", "current_exe_sub_1D27590", "item_update_body", "current_exe_class0_op14_raw77")
	return nil
}

func (s *Service) handleCurrentNPCShopPurchaseCount(session *gameSession, body []byte) error {
	shopID, err := parseCurrentNPCShopPurchaseCountRequest(body)
	if err != nil {
		s.logGameEvent(session, "game-npc-shop-purchase-count-rejected", "body_len", len(body), "reason", err)
		return s.sendCurrentNPCShopFailure(session, uint16(dnfenum.CmdPacketShopPurchaseCount), currentNPCShopBuyErrorCode)
	}
	s.logGameEvent(session, "game-npc-shop-purchase-count", "shop_id", shopID, "body_len", len(body))
	counts := make(map[uint32]uint32)
	if session != nil && session.selectedCharacterID != 0 {
		shop, loadErr := s.currentNPCShopCatalog()
		repos, ok := s.repositoryGroup()
		if loadErr != nil || !ok || repos.Character == nil || repos.Account == nil {
			return s.sendCurrentNPCShopFailure(session, uint16(dnfenum.CmdPacketShopPurchaseCount), currentNPCShopBuyErrorCode)
		}
		ctx, cancel := context.WithTimeout(context.Background(), currentRepositorySnapshotTimeout)
		defer cancel()
		character, found, err := repos.Character.Load(ctx, strconv.FormatUint(uint64(session.selectedCharacterID), 10))
		if err != nil || !found || character.AccountID != s.accountIDForSession(session) {
			return s.sendCurrentNPCShopFailure(session, uint16(dnfenum.CmdPacketShopPurchaseCount), currentNPCShopBuyErrorCode)
		}
		account, _, err := repos.Account.Load(ctx, character.AccountID)
		if err != nil {
			return err
		}
		items, err := s.currentPVFItemCatalog()
		if err != nil {
			return err
		}
		now := s.gameplayNow().UTC()
		for itemID := range shop.npcItems[shopID] {
			pricing, err := resolveCurrentNPCShopPricing(shop, items, itemID)
			if err != nil {
				continue
			}
			if policy := currentNPCShopPurchaseLimit(pricing, 1); policy != nil {
				count := dnfnpcshop.PurchaseCount(account, character, *policy, now)
				if count > 0 {
					counts[itemID] = uint32(count)
				}
			}
		}
	}
	// Native 190E810 lists 0/2 both call 15BB4F0 (the same item counter).
	// Normal stackable/equipment BuyItem uses 15BB530 -> 15B9150, also that
	// counter. Account/character scope is resolved by the server ledger.
	return s.sendGameUpperRawClass(session, uint16(dnfenum.CmdPacketShopPurchaseCount), buildCurrentNPCShopPurchaseCountBody(counts), dnfproto.DefaultChannelClassification)
}

func (s *Service) handleCurrentNPCShopSell(session *gameSession, body []byte) error {
	request, err := parseCurrentNPCShopSellRequest(body)
	if err != nil {
		s.logGameEvent(session, "game-npc-shop-sell-rejected", "body_len", len(body), "reason", err)
		return s.sendCurrentNPCShopFailure(session, uint16(dnfenum.CmdPacketSellItem), currentNPCShopSellErrorCode)
	}
	shop, err := s.currentNPCShopCatalog()
	if err != nil {
		s.logGameEvent(session, "game-npc-shop-sell-rejected", "list_type", request.ListType, "slot", request.Slot, "reason", err)
		return s.sendCurrentNPCShopFailure(session, uint16(dnfenum.CmdPacketSellItem), currentNPCShopSellErrorCode)
	}
	items, err := s.currentPVFItemCatalog()
	if err != nil {
		s.logGameEvent(session, "game-npc-shop-sell-rejected", "list_type", request.ListType, "slot", request.Slot, "reason", err)
		return s.sendCurrentNPCShopFailure(session, uint16(dnfenum.CmdPacketSellItem), currentNPCShopSellErrorCode)
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	result, err := s.commitCurrentNPCShopSell(ctx, session, shop, items, request)
	if err != nil {
		s.logGameEvent(session, "game-npc-shop-sell-rejected", "list_type", request.ListType, "slot", request.Slot, "count", request.Count, "actor_context", request.ActorContext, "reason", err)
		return s.sendCurrentNPCShopFailure(session, uint16(dnfenum.CmdPacketSellItem), currentNPCShopSellErrorCode)
	}
	if err := s.sendGameUpperRawClass(session, uint16(dnfenum.CmdPacketSellItem), buildCurrentNPCShopSellSuccessBody(result), dnfproto.DefaultChannelClassification); err != nil {
		return err
	}
	if err := s.sendGameUpperRawClass(session, uint16(dnfenum.CmdPacketWalkoutPartyMember), buildCurrentItemUpdateBody(result.ListType, result.Updates), 0); err != nil {
		return err
	}
	s.logGameEvent(session, "game-npc-shop-sell-committed", "item_id", result.ItemID, "list_type", result.ListType, "slot", result.Slot, "applied", result.Applied, "gold_after", result.GoldAfter, "incremental_entries", len(result.Updates), "ack_body", "current_exe_sub_1D130A0", "item_update_body", "current_exe_class0_op14_raw77_delete_minus_one")
	return nil
}

func (s *Service) sendCurrentNPCShopFailure(session *gameSession, opcode uint16, errorCode byte) error {
	return s.sendGameUpperRawClass(session, opcode, buildCurrentNPCShopFailureBody(errorCode), dnfproto.DefaultChannelClassification)
}
