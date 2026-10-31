package dnfbridge

import (
	"context"
	"errors"
	"strconv"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/guild"
	r "longheng.io/server/internal/modules/dnf/repository"
)

func (s *Service) prepareCurrentGuildHongbaoMutation(ctx context.Context, session *gameSession, request gameplayRequest) (*guild.HongbaoOwner, guild.HongbaoRequest, func(), error) {
	if !s.options.guildHongbaoEnabled || request.Sequence == nil {
		return nil, guild.HongbaoRequest{}, nil, guild.ErrHongbaoRequest
	}
	identity, ok := s.boundGameSessionCharacterSnapshot(session)
	if !ok || identity.generation == 0 {
		return nil, guild.HongbaoRequest{}, nil, guild.ErrCharacterNotFound
	}
	operation, err := session.guild.purchaseSequence.identity(*request.Sequence)
	if err != nil {
		return nil, guild.HongbaoRequest{}, nil, err
	}
	owner, err := s.currentGuildHongbaoOwner()
	if err != nil {
		return nil, guild.HongbaoRequest{}, nil, err
	}
	repos, ok := s.repositoryGroup()
	if !ok {
		return nil, guild.HongbaoRequest{}, nil, guild.ErrRepositoryUnavailable
	}
	actor := r.GuildOwner{AccountID: s.accountIDForSession(session), CharacterID: strconv.Itoa(int(identity.character))}
	reg, err := guild.Load(ctx, repos.Settings)
	if err != nil {
		return nil, guild.HongbaoRequest{}, nil, err
	}
	g, _, found := reg.GuildForOwner(actor)
	if !found {
		return nil, guild.HongbaoRequest{}, nil, guild.ErrNotMember
	}
	release, err := s.reserveCurrentGuildParticipants(ctx, []boundGameSessionCharacter{identity})
	if err != nil {
		return nil, guild.HongbaoRequest{}, nil, err
	}
	return owner, guild.HongbaoRequest{Owner: actor, GuildID: g.ID, OperationID: operation, Revalidate: func() bool { return s.boundGameSessionCharacterCurrent(identity) }}, release, nil
}

func (s *Service) handleCurrentGuildHongbaoIssue(session *gameSession, request gameplayRequest) error {
	fail := func(err error) error {
		s.logGameEvent(session, "game-guild-hongbao-issue-rejected", "error", err)
		if errors.Is(err, guild.ErrHongbaoListFull) {
			body := buildCurrentGuildHongbaoIssueBody(0, 0)
			body[0] = 4
			return s.sendGameUpperSuccess(session, currentGuildHongbaoIssueOpcode, body)
		}
		return s.sendCurrentGuildHongbaoFailure(session, currentGuildHongbaoIssueOpcode)
	}
	if request.Classification != 1 {
		return fail(guild.ErrHongbaoRequest)
	}
	itemID, portions, err := parseCurrentGuildHongbaoIssue(request.Body)
	if err != nil {
		return fail(err)
	}
	ctx, cancel := s.currentGuildContext()
	defer cancel()
	owner, cmd, release, err := s.prepareCurrentGuildHongbaoMutation(ctx, session, request)
	if err != nil {
		return fail(err)
	}
	defer release()
	result, err := owner.Issue(ctx, guild.HongbaoIssueRequest{HongbaoRequest: cmd, ItemID: int64(itemID), Portions: portions}, s.gameplayNow())
	if err != nil {
		return fail(err)
	}
	if !cmd.Revalidate() {
		return nil
	}
	if err := s.sendGameUpperSuccess(session, currentGuildHongbaoIssueOpcode, buildCurrentGuildHongbaoIssueBody(uint32(result.ItemID), result.RecordID)); err != nil {
		return err
	}
	return s.refreshCurrentGuildHongbaoSlot(session, result.ConsumedSlot)
}

func (s *Service) handleCurrentGuildHongbaoClaim(session *gameSession, request gameplayRequest) error {
	fail := func(err error) error {
		s.logGameEvent(session, "game-guild-hongbao-claim-rejected", "error", err)
		return s.sendCurrentGuildHongbaoFailure(session, currentGuildHongbaoClaimOpcode)
	}
	if request.Classification != 1 {
		return fail(guild.ErrHongbaoRequest)
	}
	id, err := parseCurrentGuildHongbaoID(request.Body)
	if err != nil {
		return fail(err)
	}
	ctx, cancel := s.currentGuildContext()
	defer cancel()
	owner, cmd, release, err := s.prepareCurrentGuildHongbaoMutation(ctx, session, request)
	if err != nil {
		return fail(err)
	}
	defer release()
	result, err := owner.Claim(ctx, guild.HongbaoClaimRequest{HongbaoRequest: cmd, RecordID: id}, s.gameplayNow())
	if err != nil {
		return fail(err)
	}
	if !cmd.Revalidate() {
		return nil
	}
	if err := s.sendGameUpperSuccess(session, currentGuildHongbaoClaimOpcode, buildCurrentGuildHongbaoClaimBody(result.Amount)); err != nil {
		return err
	}
	return s.sendCurrentAccountWallet(session)
}

func (s *Service) refreshCurrentGuildHongbaoSlot(session *gameSession, key string) error {
	list, slot, ok := parseSceneInventorySlotKey(key)
	if !ok || list != r.MainInventoryListType || slot < 0 {
		return guild.ErrHongbaoItem
	}
	return s.sendSelectedIncrementalItemSlotRefreshes(session, "guild_hongbao", []alignedcmd.ItemSlotRefresh{{ListType: list, SlotIndex: slot}})
}

func (s *Service) handleCurrentGuildHongbaoHistory(session *gameSession, body []byte) error {
	return s.handleCurrentGuildHongbaoLiveQuery(session, currentGuildHongbaoHistoryOpcode, body)
}

func (s *Service) handleCurrentGuildHongbaoLiveQuery(session *gameSession, op uint16, body []byte) error {
	fail := func() error { return s.sendCurrentGuildHongbaoFailure(session, op) }
	if !s.options.guildHongbaoEnabled {
		return fail()
	}
	var recordID uint32
	if op == currentGuildHongbaoHistoryOpcode {
		var err error
		recordID, err = parseCurrentGuildHongbaoID(body)
		if err != nil {
			return fail()
		}
	} else if op != currentGuildHongbaoListOpcode || len(body) != 0 {
		return fail()
	}
	identity, ok := s.boundGameSessionCharacterSnapshot(session)
	if !ok {
		return fail()
	}
	ctx, cancel := s.currentGuildContext()
	defer cancel()
	release, err := s.reserveCurrentGuildParticipants(ctx, []boundGameSessionCharacter{identity})
	if err != nil {
		return fail()
	}
	defer release()
	owner, err := s.currentGuildHongbaoOwner()
	if err != nil {
		return fail()
	}
	actor := r.GuildOwner{AccountID: s.accountIDForSession(session), CharacterID: strconv.Itoa(int(identity.character))}
	var response []byte
	if op == currentGuildHongbaoHistoryOpcode {
		history, err := owner.History(ctx, actor, recordID, s.gameplayNow())
		if err != nil {
			return fail()
		}
		rows := make([]currentGuildHongbaoClaimRow, 0, len(history))
		for _, entry := range history {
			rows = append(rows, currentGuildHongbaoClaimRow{Level: entry.Level, Name: entry.Name, Amount: entry.Amount})
		}
		response, err = buildCurrentGuildHongbaoHistoryBody(rows)
		if err != nil {
			return fail()
		}
	} else {
		now := s.gameplayNow()
		view, err := owner.View(ctx, actor, now)
		if err != nil || view.MemberCount > 65535 {
			return fail()
		}
		rows := make([]currentGuildHongbaoRow, 0, len(view.Records))
		for _, record := range view.Records {
			done := record.RemainingPortions() == 0 || !now.Before(record.ExpiresAt) || record.Settlement != nil
			claimed := false
			for _, claim := range record.Claims {
				claimed = claimed || claim.Owner.AccountID == actor.AccountID
			}
			rows = append(rows, currentGuildHongbaoRow{ID: record.ID, ItemID: uint32(record.Product.ItemID), Name: record.SenderName, Portions: record.RemainingPortions(), Completed: done, Claimed: claimed})
		}
		response, err = buildCurrentGuildHongbaoListBody(uint16(view.MemberCount), view.PurchasedToday, view.TakenToday, rows)
		if err != nil {
			return fail()
		}
	}
	if !s.boundGameSessionCharacterCurrent(identity) {
		return nil
	}
	return s.sendGameUpperSuccess(session, op, response)
}

func isCurrentGuildHongbaoCommodity(id uint32) bool { return id >= 103938 && id <= 103941 }

func (s *Service) handleCurrentGuildHongbaoPurchase(session *gameSession, cart currentCeraShopPurchaseRequest, raw []byte, sequence *uint64) error {
	fail := func(err error) error {
		s.logGameEvent(session, "game-guild-hongbao-purchase-rejected", "error", err)
		return s.sendCurrentCeraShopPurchaseFailure(session)
	}
	if len(cart.Items) != 1 || cart.PaymentMode != currentCeraShopPaymentModeCera || len(raw) != currentCeraShopRequestHeaderSize+currentCeraShopRequestItemStride {
		return fail(guild.ErrHongbaoRequest)
	}
	ctx, cancel := s.currentGuildContext()
	defer cancel()
	owner, cmd, release, err := s.prepareCurrentGuildHongbaoMutation(ctx, session, gameplayRequest{Sequence: sequence, Body: raw})
	if err != nil {
		return fail(err)
	}
	defer release()
	shop, err := s.currentCeraShopCatalog()
	if err != nil {
		return fail(err)
	}
	product, found := shop.Product(cart.Items[0].CommodityID)
	if !found || product.Count != 1 || len(shop.bonusByCommodity[product.CommodityID]) != 0 {
		return fail(guild.ErrHongbaoCatalog)
	}
	// Both catalogs must describe the same real single-item, Cera-only purchase.
	items, err := s.currentPVFItemCatalog()
	if err != nil {
		return fail(err)
	}
	catalog, err := guild.LoadHongbaoCatalog(items.source)
	if err != nil {
		return fail(err)
	}
	hongbao, found := catalog.ProductByCommodity(product.CommodityID)
	if !found || int64(product.ItemID) != hongbao.ItemID || product.CeraPrice != hongbao.Price {
		return fail(guild.ErrHongbaoCatalog)
	}
	result, err := owner.Purchase(ctx, guild.HongbaoPurchaseRequest{HongbaoRequest: cmd, CommodityID: product.CommodityID}, s.gameplayNow())
	if err != nil {
		return fail(err)
	}
	if !cmd.Revalidate() {
		return nil
	}
	if err := s.sendGameUpperRawClass(session, 64, buildCurrentCeraShopPurchaseSuccessBodyWithCount(product.CommodityID, 1), 1); err != nil {
		return err
	}
	if err := s.refreshCurrentGuildHongbaoSlot(session, result.Slot); err != nil {
		return err
	}
	return s.sendCurrentAccountWallet(session)
}
