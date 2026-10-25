package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"strconv"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dungeon"
	"longheng.io/server/internal/modules/dnf/guild"
	r "longheng.io/server/internal/modules/dnf/repository"
)

const currentGuildMerchantPurchaseOpcode uint16 = 1354

func (s *Service) handleCurrentGuildMerchantPurchase(session *gameSession, request gameplayRequest) error {
	if !requireDefaultGameplayClassification(s, session, request, "game-guild-merchant-class-rejected", "command_class_mismatch") {
		return nil
	}
	fail := func(err error) error {
		code := byte(3)
		if errors.Is(err, guild.ErrMerchantAP) {
			code = 22
		}
		s.logGameEvent(session, "game-guild-merchant-purchase-rejected", "error", err)
		return s.sendGameUpperFailure(session, currentGuildMerchantPurchaseOpcode, code)
	}
	if !s.options.guildEssenceEnabled || request.Sequence == nil || len(request.Body) != 16 {
		return fail(guild.ErrMerchantInteraction)
	}
	itemID, quantity := binary.LittleEndian.Uint32(request.Body), binary.LittleEndian.Uint32(request.Body[4:])
	shopID, npcID := binary.LittleEndian.Uint32(request.Body[8:]), binary.LittleEndian.Uint32(request.Body[12:])
	identity, ok := s.boundGameSessionCharacterSnapshot(session)
	if !ok || identity.generation == 0 {
		return fail(guild.ErrCharacterNotFound)
	}
	requestID, err := session.guild.purchaseSequence.identity(*request.Sequence)
	if err != nil {
		return fail(err)
	}
	items, err := s.currentPVFItemCatalog()
	if err != nil {
		return fail(err)
	}
	catalog, err := s.currentGuildMerchantPurchaseCatalog(items)
	if err != nil {
		return fail(err)
	}
	shop, found := catalog.Shop(shopID)
	if !found || shop.NPCID != npcID {
		return fail(guild.ErrMerchantShop)
	}
	ctx, cancel := s.currentGuildContext()
	defer cancel()
	repos, ok := s.repositoryGroup()
	if !ok {
		return fail(guild.ErrRepositoryUnavailable)
	}
	owner := r.GuildOwner{AccountID: s.accountIDForSession(session), CharacterID: strconv.Itoa(int(identity.character))}
	reg, err := guild.Load(ctx, repos.Settings)
	if err != nil {
		return fail(err)
	}
	g, _, found := reg.GuildForOwner(owner)
	if !found {
		return fail(guild.ErrNotMember)
	}
	release, err := s.reserveCurrentGuildParticipants(ctx, []boundGameSessionCharacter{identity})
	if err != nil {
		return nil
	}
	defer release()
	if !session.townMu.TryLock() {
		return fail(guild.ErrMerchantInteraction)
	}
	now := s.gameplayNow()
	result, err := func() (guild.MerchantPurchaseReceipt, error) {
		defer session.townMu.Unlock()
		p := session.townPositionSnapshot
		area, found := s.townArea(int64(p.TownID), int64(p.AreaID))
		if !found || !townMapPathLooksLikeGuildHall(area.MapPath) {
			return guild.MerchantPurchaseReceipt{}, guild.ErrMerchantInteraction
		}
		scene, err := loadGuildMerchantNPC(items.source, area.MapPath, shop.BaseNPCID)
		if err != nil {
			return guild.MerchantPurchaseReceipt{}, err
		}
		return guild.ApplyMerchantPurchase(ctx, repos.Guild, guild.MerchantPurchaseRequest{
			Owner: owner, GuildID: g.ID, RequestID: requestID, ItemID: itemID, Quantity: quantity, ShopID: shopID, NPCID: npcID,
			Revalidate: func() bool {
				if !s.boundGameSessionCharacterCurrent(identity) || session.townSceneReadyCharacterID != identity.character || session.townPositionSnapshot != p || p.CharacterID != identity.character || p.TownID != 8 || !p.PositionValid {
					return false
				}
				if !session.dungeon.mu.TryLock() {
					return false
				}
				inDungeon := session.dungeon.runtime != nil
				session.dungeon.mu.Unlock()
				return !inDungeon
			},
			CanInteract: func(actor r.CharacterRecord, _ *guild.Guild, _ bool) bool {
				dx, dy := int(p.PositionX)-scene.x, int(p.PositionY)-scene.y
				return actor.Stats["town_id"] == int64(p.TownID) && actor.Stats["area_id"] == int64(p.AreaID) && dx >= -scene.dx && dx <= scene.dx && dy >= -scene.dy && dy <= scene.dy
			},
		}, catalog, guildMerchantGrant(items, now), now)
	}()
	if err != nil {
		return fail(err)
	}
	if err = s.sendGameUpperSuccess(session, currentGuildMerchantPurchaseOpcode, nil); err != nil {
		return err
	}
	refreshes := make([]alignedcmd.ItemSlotRefresh, 0, len(result.Slots))
	for _, key := range result.Slots {
		list, slot, ok := parseSceneInventorySlotKey(key)
		if !ok || list != r.MainInventoryListType || slot < 0 {
			return guild.ErrMerchantInventory
		}
		refreshes = append(refreshes, alignedcmd.ItemSlotRefresh{ListType: list, SlotIndex: slot})
	}
	if err = s.sendSelectedIncrementalItemSlotRefreshes(session, "guild_merchant", refreshes); err != nil {
		return err
	}
	if err = s.writeCurrentGuildEssenceSnapshot(session); err != nil {
		return err
	}
	s.queueCurrentGuildEssenceRefresh(result.GuildID, identity.character)
	return nil
}

func guildMerchantGrant(items *pvfDungeonDropCatalog, now time.Time) guild.MerchantPurchaseGrant {
	return func(_ context.Context, _ r.Group, actor *r.CharacterRecord, inventory *r.InventoryRecord, product guild.MerchantProduct, quantity uint32, expires time.Time) ([]string, error) {
		if actor == nil || inventory == nil || inventory.CharacterID != actor.CharacterID {
			return nil, guild.ErrMerchantInventory
		}
		item, err := currentDungeonCardItemReward(newCurrentDungeonDropLCG(1), items, product.ItemID, now)
		if err != nil {
			return nil, err
		}
		if !item.Stackable || item.StackLimit != int64(product.StackLimit) || !item.ExpireAt.Equal(expires) {
			return nil, guild.ErrMerchantCatalog
		}
		item.Extra["source"] = "guild_merchant_purchase"
		reward := dungeon.CardItemReward{Stack: r.ItemStack{ItemID: item.ItemID, Count: int64(quantity), Bind: item.Bind, RawEntry: append([]byte(nil), item.RawEntry...), Extra: cloneDungeonCardStringMap(item.Extra)}, Stackable: true, SlotStart: item.SlotStart, SlotEnd: item.SlotEnd, StackLimit: item.StackLimit, ExpireAt: expires}
		slot, err := dungeon.AddResolvedRewardToInventory(inventory, reward, func(stack r.ItemStack, deadline time.Time) (r.ItemStack, error) {
			stack, _ = applyCurrentStackableExpirationAt(stack, deadline, now)
			return stack, nil
		})
		if err != nil {
			return nil, err
		}
		return []string{fmt.Sprintf("0:%d", slot)}, nil
	}
}
