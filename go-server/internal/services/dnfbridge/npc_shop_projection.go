package dnfbridge

import (
	"context"
	"errors"
	"fmt"
	dnfitemlock "longheng.io/server/internal/modules/dnf/itemlock"
	"math"
	"strconv"
	"strings"

	dnfnpcshop "longheng.io/server/internal/modules/dnf/npcshop"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

var (
	errCurrentNPCShopOwnerUnavailable     = errors.New("dnf current NPC shop owner is unavailable")
	errCurrentNPCShopGoldInsufficient     = errors.New("dnf current NPC shop gold is insufficient")
	errCurrentNPCShopMaterialInsufficient = errors.New("dnf current NPC shop material is insufficient")
	errCurrentNPCShopItemLocked           = errors.New("dnf current NPC shop item is equipment locked")
	errCurrentNPCShopGoldOverflow         = errors.New("dnf current NPC shop gold exceeds the current client wallet")
)

func (s *Service) commitCurrentNPCShopBuy(ctx context.Context, session *gameSession, shop *currentNPCShopCatalog, items *pvfDungeonDropCatalog, request currentNPCShopBuyRequest) (currentNPCShopBuyResult, error) {
	if s == nil || session == nil || session.selectedCharacterID == 0 || shop == nil || items == nil {
		return currentNPCShopBuyResult{}, errCurrentNPCShopOwnerUnavailable
	}
	pricing, err := resolveCurrentNPCShopActorPricing(shop, items, request.ItemID, request.ActorContext)
	if err != nil {
		return currentNPCShopBuyResult{}, err
	}
	if !pricing.Buyable {
		return currentNPCShopBuyResult{}, fmt.Errorf("%w: item=%d listed=%t buy_gold=%d material_exchange=%t", errCurrentNPCShopProductUnavailable, request.ItemID, currentNPCShopContainsItem(shop, request.ItemID), pricing.BuyGold, pricing.MaterialExchange)
	}
	count, err := normalizeCurrentNPCShopBuyCount(pricing.Definition, request.Count)
	if err != nil {
		return currentNPCShopBuyResult{}, err
	}
	request.Count = count
	unlock, err := s.lockCurrentFiendWarMerchantPurchase(session, shop, request)
	if err != nil {
		return currentNPCShopBuyResult{}, err
	}
	defer unlock()
	if pricing.Definition.DailyDeleteItem {
		pricing.Definition, err = currentPVFItemDefinitionForGrantAt(pricing.Definition, s.gameplayNow())
		if err != nil {
			return currentNPCShopBuyResult{}, err
		}
	}
	if pricing.Definition.Kind == dungeonDropItemStackable && pricing.Definition.StackLimit > 0 && int64(request.Count) > pricing.Definition.StackLimit {
		return currentNPCShopBuyResult{}, fmt.Errorf("%w: item=%d count=%d stack_limit=%d", errCurrentNPCShopProductUnavailable, request.ItemID, request.Count, pricing.Definition.StackLimit)
	}
	if pricing.MaterialExchange {
		return s.commitCurrentNPCShopMaterialExchange(ctx, session, pricing, request)
	}
	if pricing.BuyGold > math.MaxInt64/int64(request.Count) {
		return currentNPCShopBuyResult{}, errCurrentNPCShopGoldOverflow
	}
	totalCost := pricing.BuyGold * int64(request.Count)
	accountID, characterID, repositories, owner, err := s.currentNPCShopMutationOwner(session)
	if err != nil {
		return currentNPCShopBuyResult{}, err
	}
	var result currentNPCShopBuyResult
	err = owner.Mutate(ctx, dnfnpcshop.Command{
		AccountID:     accountID,
		CharacterID:   characterID,
		UpdatedAt:     s.gameplayNow(),
		PurchaseLimit: currentNPCShopPurchaseLimit(pricing, request.Count),
		Project: func(assets *dnfnpcshop.Assets) (dnfnpcshop.Changes, error) {
			character := assets.Character
			inventory := assets.Inventory
			if character.Stats == nil {
				return dnfnpcshop.Changes{}, errCurrentNPCShopOwnerUnavailable
			}
			gold := character.Stats["gold"]
			if gold < 0 || gold > math.MaxInt32 {
				return dnfnpcshop.Changes{}, errCurrentNPCShopGoldOverflow
			}
			if gold < totalCost {
				return dnfnpcshop.Changes{}, fmt.Errorf("%w: need=%d have=%d", errCurrentNPCShopGoldInsufficient, totalCost, gold)
			}
			slots, grantErr := s.grantCurrentNPCShopProduct(inventory, pricing.Definition, request.Count)
			if grantErr != nil {
				return dnfnpcshop.Changes{}, grantErr
			}
			if len(slots) != 1 {
				return dnfnpcshop.Changes{}, fmt.Errorf("%w: item=%d changed_slots=%d", errCurrentNPCShopProductUnavailable, request.ItemID, len(slots))
			}
			slot := int16(slots[0])
			productList := currentNPCShopProductList(pricing.Definition)
			key := currentCeraShopInventorySlotKey(productList, slot)
			stack, found := inventory.Slots[key]
			if !found || stack.ItemID != int64(request.ItemID) || stack.Count <= 0 {
				return dnfnpcshop.Changes{}, errCurrentNPCShopProductUnavailable
			}
			if stack.Extra == nil {
				stack.Extra = make(map[string]string, 6)
			}
			stack.Extra["last_grant_source"] = "npc_shop"
			stack.Extra["last_npc_shop_context"] = strconv.FormatUint(uint64(request.ShopContext), 10)
			stack.Extra["last_npc_shop_actor_context"] = strconv.FormatUint(uint64(request.ActorContext), 10)
			itemEntry := currentItemListEntryFromStack(productList, slot, stack)
			stack.RawEntry = append([]byte(nil), itemEntry.data[:]...)
			inventory.Slots[key] = stack
			goldAfter := gold - totalCost
			character.Stats["gold"] = goldAfter
			updates := []currentItemListEntry{currentGoldWalletItemListEntry(goldAfter)}
			var medalUpdates []currentItemListEntry
			if productList == currentGuildMedalInventoryListType {
				medalUpdates = []currentItemListEntry{itemEntry}
			} else {
				updates = append(updates, itemEntry)
			}
			sortCurrentItemListEntries(updates)
			result = currentNPCShopBuyResult{
				GoldAfter:         goldAfter,
				SPAfter:           character.Stats["sp"],
				Item:              currentNPCShopBuyAckEntry(itemEntry, pricing.Definition, request.Count),
				Updates:           updates,
				GuildMedalUpdates: medalUpdates,
				Slot:              slot,
				ItemID:            request.ItemID,
				Count:             request.Count,
			}
			return dnfnpcshop.Changes{Character: true, Inventory: true}, nil
		},
	})
	if err != nil {
		return currentNPCShopBuyResult{}, currentNPCShopMutationError(err)
	}
	// Cera is the account-shared pool; the NPC shop ACK only displays it and
	// never debits, so a read-only post-commit load is sufficient.
	result.CoinAfter = s.loadCurrentAccountCera(ctx, repositories, session)
	return result, nil
}

// commitCurrentNPCShopMaterialExchange buys a [need material] shop item: it
// consumes the material cost (account cube-fragment cell for cube fragments,
// first matching character stack otherwise) and grants the purchase in the
// same account+inventory transaction. Gold is not charged: the client shop UI
// shows only the material cost for these goods. Cube-fragment purchases land
// on their fixed account warehouse slots.
func (s *Service) commitCurrentNPCShopMaterialExchange(ctx context.Context, session *gameSession, pricing currentNPCShopPricing, request currentNPCShopBuyRequest) (currentNPCShopBuyResult, error) {
	if s == nil || session == nil || session.selectedCharacterID == 0 {
		return currentNPCShopBuyResult{}, errCurrentNPCShopOwnerUnavailable
	}
	switch pricing.Definition.Kind {
	case dungeonDropItemEquipment:
		if request.Count != 1 {
			return currentNPCShopBuyResult{}, fmt.Errorf("%w: exchange equipment=%d count=%d", errCurrentNPCShopProductUnavailable, request.ItemID, request.Count)
		}
	case dungeonDropItemStackable:
		if request.Count == 0 {
			return currentNPCShopBuyResult{}, fmt.Errorf("%w: exchange stackable=%d count=0", errCurrentNPCShopProductUnavailable, request.ItemID)
		}
	default:
		return currentNPCShopBuyResult{}, fmt.Errorf("%w: exchange item=%d kind=%s", errCurrentNPCShopProductUnavailable, request.ItemID, pricing.Definition.Kind)
	}
	if pricing.NeedMaterialItem == 0 || pricing.NeedMaterialCount <= 0 || pricing.NeedMaterialCount > math.MaxInt64/int64(request.Count) {
		return currentNPCShopBuyResult{}, fmt.Errorf("%w: exchange item=%d cost=%d x%d", errCurrentNPCShopProductUnavailable, request.ItemID, pricing.NeedMaterialItem, pricing.NeedMaterialCount)
	}
	totalMaterial := pricing.NeedMaterialCount * int64(request.Count)
	if pricing.ExchangeGold < 0 || pricing.ExchangeGold > math.MaxInt64/int64(request.Count) {
		return currentNPCShopBuyResult{}, errCurrentNPCShopGoldOverflow
	}
	totalGold := pricing.ExchangeGold * int64(request.Count)
	accountID, characterID, repositories, owner, err := s.currentNPCShopMutationOwner(session)
	if err != nil {
		return currentNPCShopBuyResult{}, err
	}
	var result currentNPCShopBuyResult
	err = owner.Mutate(ctx, dnfnpcshop.Command{
		AccountID:     accountID,
		CharacterID:   characterID,
		UpdatedAt:     s.gameplayNow(),
		PurchaseLimit: currentNPCShopPurchaseLimit(pricing, request.Count),
		Project: func(assets *dnfnpcshop.Assets) (dnfnpcshop.Changes, error) {
			character := assets.Character
			inventory := assets.Inventory
			account := assets.AccountInventory
			if character.Stats == nil {
				return dnfnpcshop.Changes{}, errCurrentNPCShopOwnerUnavailable
			}
			gold := character.Stats["gold"]
			if gold < totalGold {
				return dnfnpcshop.Changes{}, errCurrentNPCShopGoldInsufficient
			}
			gold -= totalGold
			updates := make([]currentItemListEntry, 0, 4)

			// Consume the material cost.
			accountDirty := false
			if materialSlot, warehouseMaterial := currentDisjointWarehouseFixedSlots[pricing.NeedMaterialItem]; warehouseMaterial {
				key := dnfrepo.AccountSharedInventorySlotKey(materialSlot)
				stack, exists := account.Slots[key]
				if !exists || stack.ItemID != int64(pricing.NeedMaterialItem) || stack.Count < totalMaterial {
					return dnfnpcshop.Changes{}, fmt.Errorf("%w: item=%d need=%d have=%d", errCurrentNPCShopMaterialInsufficient, pricing.NeedMaterialItem, totalMaterial, stack.Count)
				}
				stack.Count -= totalMaterial
				entry := currentItemListEntryFromStack(dnfrepo.MainInventoryListType, materialSlot, stack)
				stack.RawEntry = append([]byte(nil), entry.data[:]...)
				account.Slots[key] = stack
				accountDirty = true
				result.CostItemID = pricing.NeedMaterialItem
				result.CostItemNewCount = stack.Count
				updates = append(updates, entry)
			} else {
				materialSlot := int16(-1)
				var materialKey string
				var materialStack dnfrepo.ItemStack
				for slot := int16(0); slot <= dnfrepo.SoulWarehouseLastSlot; slot++ {
					if dnfrepo.IsAccountSharedInventorySlot(dnfrepo.MainInventoryListType, slot) {
						continue
					}
					key := currentCeraShopInventorySlotKey(dnfrepo.MainInventoryListType, slot)
					stack, exists := inventory.Slots[key]
					if exists && stack.ItemID == int64(pricing.NeedMaterialItem) && stack.Count >= totalMaterial {
						materialSlot, materialKey, materialStack = slot, key, stack
						break
					}
				}
				if materialSlot < 0 {
					return dnfnpcshop.Changes{}, fmt.Errorf("%w: item=%d need=%d", errCurrentNPCShopMaterialInsufficient, pricing.NeedMaterialItem, totalMaterial)
				}
				materialStack.Count -= totalMaterial
				if materialStack.Count <= 0 {
					delete(inventory.Slots, materialKey)
					var removed currentItemListEntry
					removed.patchCore(materialSlot, math.MaxUint32, 0)
					updates = append(updates, removed)
				} else {
					entry := currentItemListEntryFromStack(dnfrepo.MainInventoryListType, materialSlot, materialStack)
					materialStack.RawEntry = append([]byte(nil), entry.data[:]...)
					inventory.Slots[materialKey] = materialStack
					updates = append(updates, entry)
				}
				result.CostItemID = pricing.NeedMaterialItem
				result.CostItemNewCount = materialStack.Count
			}

			// Grant the purchase.
			grantSlots := make([]int16, 0, 1)
			if warehouseSlot, warehouseItem := currentDisjointWarehouseFixedSlots[request.ItemID]; warehouseItem {
				left, grantErr := grantCurrentDisjointWarehouseStack(account, pricing.Definition, warehouseSlot, int64(request.Count))
				if grantErr != nil {
					return dnfnpcshop.Changes{}, grantErr
				}
				if left < int64(request.Count) {
					accountDirty = true
					grantSlots = append(grantSlots, warehouseSlot)
				}
				if left > 0 {
					overflowSlots, overflowErr := grantCurrentCeraShopProductAt(inventory, pricing.Definition, uint32(left), s.gameplayNow())
					if overflowErr != nil {
						return dnfnpcshop.Changes{}, overflowErr
					}
					for _, slot := range overflowSlots {
						grantSlots = append(grantSlots, int16(slot))
					}
				}
			} else {
				slots, grantErr := s.grantCurrentNPCShopProduct(inventory, pricing.Definition, request.Count)
				if grantErr != nil {
					return dnfnpcshop.Changes{}, grantErr
				}
				if len(slots) != 1 {
					return dnfnpcshop.Changes{}, fmt.Errorf("%w: item=%d changed_slots=%d", errCurrentNPCShopProductUnavailable, request.ItemID, len(slots))
				}
				grantSlots = append(grantSlots, int16(slots[0]))
			}
			var itemEntry currentItemListEntry
			productList := currentNPCShopProductList(pricing.Definition)
			for index, slot := range grantSlots {
				key := currentCeraShopInventorySlotKey(productList, slot)
				stack, found := account.Slots[key]
				if !found {
					stack, found = inventory.Slots[key]
				}
				if !found || stack.ItemID != int64(request.ItemID) || stack.Count <= 0 {
					return dnfnpcshop.Changes{}, errCurrentNPCShopProductUnavailable
				}
				if stack.Extra == nil {
					stack.Extra = make(map[string]string, 4)
				}
				stack.Extra["last_grant_source"] = "npc_shop"
				stack.Extra["last_npc_shop_context"] = strconv.FormatUint(uint64(request.ShopContext), 10)
				stack.Extra["last_npc_shop_actor_context"] = strconv.FormatUint(uint64(request.ActorContext), 10)
				entry := currentItemListEntryFromStack(productList, slot, stack)
				stack.RawEntry = append([]byte(nil), entry.data[:]...)
				if _, isAccount := account.Slots[key]; isAccount {
					account.Slots[key] = stack
					accountDirty = true
				} else {
					inventory.Slots[key] = stack
				}
				if index == 0 {
					itemEntry = entry
				}
				if productList == currentGuildMedalInventoryListType {
					result.GuildMedalUpdates = append(result.GuildMedalUpdates, entry)
				} else {
					updates = append(updates, entry)
				}
			}
			updates = append(updates, currentGoldWalletItemListEntry(gold))
			sortCurrentItemListEntries(updates)
			result.GoldAfter = gold
			result.SPAfter = character.Stats["sp"]
			result.Item = currentNPCShopBuyAckEntry(itemEntry, pricing.Definition, request.Count)
			result.Updates = updates
			result.Slot = grantSlots[0]
			result.ItemID = request.ItemID
			result.Count = request.Count
			character.Stats["gold"] = gold
			return dnfnpcshop.Changes{AccountInventory: accountDirty, Inventory: true, Character: totalGold != 0}, nil
		},
	})
	if err != nil {
		return currentNPCShopBuyResult{}, currentNPCShopMutationError(err)
	}
	result.CoinAfter = s.loadCurrentAccountCera(ctx, repositories, session)
	return result, nil
}

func (s *Service) commitCurrentNPCShopSell(ctx context.Context, session *gameSession, shop *currentNPCShopCatalog, items *pvfDungeonDropCatalog, request currentNPCShopSellRequest) (currentNPCShopSellResult, error) {
	if s == nil || session == nil || session.selectedCharacterID == 0 || shop == nil || items == nil {
		return currentNPCShopSellResult{}, errCurrentNPCShopOwnerUnavailable
	}
	accountID, characterID, _, owner, err := s.currentNPCShopMutationOwner(session)
	if err != nil {
		return currentNPCShopSellResult{}, err
	}
	var result currentNPCShopSellResult
	err = owner.Mutate(ctx, dnfnpcshop.Command{
		AccountID:   accountID,
		CharacterID: characterID,
		Project: func(assets *dnfnpcshop.Assets) (dnfnpcshop.Changes, error) {
			character := assets.Character
			inventory := assets.Inventory
			if character.Stats == nil {
				return dnfnpcshop.Changes{}, errCurrentNPCShopOwnerUnavailable
			}
			key := currentCeraShopInventorySlotKey(request.ListType, request.Slot)
			stack, found := inventory.Slots[key]
			if !found || stack.ItemID <= 0 || stack.ItemID > math.MaxUint32 || stack.Count <= 0 {
				return dnfnpcshop.Changes{}, fmt.Errorf("%w: list=%d slot=%d", errCurrentNPCShopProductUnavailable, request.ListType, request.Slot)
			}
			if currentNPCShopItemLocked(stack) {
				return dnfnpcshop.Changes{}, fmt.Errorf("%w: list=%d slot=%d", errCurrentNPCShopItemLocked, request.ListType, request.Slot)
			}
			pricing, priceErr := resolveCurrentNPCShopActorPricing(shop, items, uint32(stack.ItemID), request.ActorContext)
			if priceErr != nil {
				return dnfnpcshop.Changes{}, priceErr
			}
			applied := int64(request.Count)
			if applied > stack.Count {
				applied = stack.Count
			}
			if pricing.Definition.Kind == dungeonDropItemEquipment {
				applied = 1
			}
			if applied <= 0 || applied > math.MaxUint32 || pricing.SellGold > 0 && applied > math.MaxInt64/pricing.SellGold {
				return dnfnpcshop.Changes{}, errCurrentNPCShopGoldOverflow
			}
			gold := character.Stats["gold"]
			if gold < 0 || gold > math.MaxInt32 {
				return dnfnpcshop.Changes{}, errCurrentNPCShopGoldOverflow
			}
			goldDelta := pricing.SellGold * applied
			if goldDelta > math.MaxInt32-gold {
				return dnfnpcshop.Changes{}, errCurrentNPCShopGoldOverflow
			}
			remaining := stack.Count - applied
			var itemUpdate currentItemListEntry
			if remaining <= 0 {
				delete(inventory.Slots, key)
				itemUpdate.patchCore(request.Slot, math.MaxUint32, 0)
			} else {
				stack.Count = remaining
				itemUpdate = currentItemListEntryFromStack(request.ListType, request.Slot, stack)
				stack.RawEntry = append([]byte(nil), itemUpdate.data[:]...)
				inventory.Slots[key] = stack
			}
			goldAfter := gold + goldDelta
			character.Stats["gold"] = goldAfter
			updates := []currentItemListEntry{currentGoldWalletItemListEntry(goldAfter), itemUpdate}
			sortCurrentItemListEntries(updates)
			result = currentNPCShopSellResult{
				GoldAfter: goldAfter,
				ListType:  request.ListType,
				Slot:      request.Slot,
				Applied:   uint32(applied),
				ItemID:    uint32(stack.ItemID),
				Updates:   updates,
			}
			return dnfnpcshop.Changes{Character: true, Inventory: true}, nil
		},
	})
	if err != nil {
		return currentNPCShopSellResult{}, currentNPCShopMutationError(err)
	}
	return result, nil
}

func currentNPCShopItemLocked(stack dnfrepo.ItemStack) bool {
	return dnfitemlock.IsLocked(stack)
}

func (s *Service) currentNPCShopMutationOwner(session *gameSession) (string, string, dnfrepo.Group, *dnfnpcshop.Owner, error) {
	if s == nil || session == nil || session.selectedCharacterID == 0 {
		return "", "", dnfrepo.Group{}, nil, errCurrentNPCShopOwnerUnavailable
	}
	repositories, ok := s.repositoryGroup()
	if !ok || repositories.CharacterSettlement == nil {
		return "", "", dnfrepo.Group{}, nil, errCurrentNPCShopOwnerUnavailable
	}
	accountID := strings.TrimSpace(s.accountIDForSession(session))
	if accountID == "" {
		return "", "", dnfrepo.Group{}, nil, errCurrentNPCShopOwnerUnavailable
	}
	owner, err := dnfnpcshop.NewOwner(repositories)
	if err != nil {
		return "", "", dnfrepo.Group{}, nil, errCurrentNPCShopOwnerUnavailable
	}
	characterID := strconv.FormatUint(uint64(session.selectedCharacterID), 10)
	return accountID, characterID, repositories, owner, nil
}

func currentNPCShopMutationError(err error) error {
	switch {
	case errors.Is(err, dnfnpcshop.ErrOwnerUnavailable),
		errors.Is(err, dnfnpcshop.ErrAccountRequired),
		errors.Is(err, dnfnpcshop.ErrCharacterRequired),
		errors.Is(err, dnfnpcshop.ErrCharacterNotFound),
		errors.Is(err, dnfnpcshop.ErrAccountMismatch),
		errors.Is(err, dnfnpcshop.ErrInventoryNotFound):
		return errors.Join(errCurrentNPCShopOwnerUnavailable, err)
	default:
		return err
	}
}

// NPC purchases use the same PVF medal/gem pages as other grants. Keep the
// existing main-bag path for all other products and account-shared materials.
func currentNPCShopProductList(definition dungeonDropItemDefinition) byte {
	container := currentPVFItemTargetContainer(definition)
	if container.GuildMedal || container.GuardianGem {
		return currentGuildMedalInventoryListType
	}
	return dnfrepo.MainInventoryListType
}

func (s *Service) grantCurrentNPCShopProduct(inventory *dnfrepo.InventoryRecord, definition dungeonDropItemDefinition, count uint32) ([]uint16, error) {
	container := currentPVFItemTargetContainer(definition)
	if !container.GuildMedal && !container.GuardianGem {
		return grantCurrentCeraShopProductAt(inventory, definition, count, s.gameplayNow())
	}
	// Preserve the existing NPC grant expiration policy when selecting list38.
	if !definition.DailyDeleteItem && !definition.ExpirationDate.IsZero() {
		definition.ExpirationDate = currentPVFRenewedRetiredExpirationDate(definition.ExpirationDate, s.gameplayNow())
	}
	slots, err := grantCurrentGuildMedalPageItem(inventory, definition, container, count)
	if err != nil {
		return nil, err
	}
	result := make([]uint16, len(slots))
	for i, slot := range slots {
		result[i] = uint16(slot)
	}
	return result, nil
}
