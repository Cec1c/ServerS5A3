package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"strconv"

	r "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/wedding"
)

func (s *Service) sendCurrentWeddingLovePoints(session *gameSession, points uint32) error {
	identity, ok := s.boundGameSessionCharacterSnapshot(session)
	if !ok {
		return wedding.ErrOwnership
	}
	b := make([]byte, 8)
	binary.LittleEndian.PutUint32(b, uint32(identity.character))
	binary.LittleEndian.PutUint32(b[4:], points)
	return s.sendGameUpperRawClass(session, 1003, b, 0)
}

func (s *Service) commitCurrentWeddingFurnitureBuy(ctx context.Context, session *gameSession, p currentNPCShopPricing, q currentNPCShopBuyRequest) (currentNPCShopBuyResult, error) {
	if q.Count != 1 || !p.MaterialExchange || p.NeedMaterialItem != uint32(wedding.LovePointItemID) {
		return currentNPCShopBuyResult{}, wedding.ErrItem
	}
	actor, ok := s.boundGameSessionCharacterSnapshot(session)
	if !ok {
		return currentNPCShopBuyResult{}, wedding.ErrOwnership
	}
	s.wedding.mu.Lock()
	defer s.wedding.mu.Unlock()
	req, actors, err := s.currentWeddingPartnerRequest(ctx, actor)
	if err != nil {
		return currentNPCShopBuyResult{}, err
	}
	if !s.currentWeddingRoomScene(actor, req.Partner.CharacterID) {
		return currentNPCShopBuyResult{}, wedding.ErrState
	}
	current := req.Revalidate
	req.Revalidate = func() bool { return current() && s.currentWeddingRoomScene(actor, req.Partner.CharacterID) }
	release, err := s.reserveCurrentGuildParticipants(ctx, actors)
	if err != nil {
		return currentNPCShopBuyResult{}, err
	}
	defer release()
	owner, err := s.currentWeddingOwner()
	if err != nil {
		return currentNPCShopBuyResult{}, err
	}
	furniture, known := owner.Rules.Furniture[int64(q.ItemID)]
	if !known || furniture.Points != p.NeedMaterialCount || furniture.Gold != p.ExchangeGold || p.Definition.DailyDeleteItem || p.Definition.UsablePeriodDays != 0 || !p.Definition.ExpirationDate.IsZero() {
		return currentNPCShopBuyResult{}, wedding.ErrItem
	}
	stack := currentPVFGrantIdentity(p.Definition)
	stack.Count = 1
	result, err := owner.BuyFurniture(ctx, req, stack)
	if err != nil {
		return currentNPCShopBuyResult{}, err
	}
	state, err := wedding.Read(result.Actor)
	if err != nil {
		return currentNPCShopBuyResult{}, err
	}
	entry := currentItemListEntryFromStack(r.MainInventoryListType, int16(result.PurchasedSlot), stack)
	return currentNPCShopBuyResult{WeddingRoom: result.Room, GoldAfter: result.Actor.Stats["gold"], SPAfter: result.Actor.Stats["sp"], CoinAfter: s.loadCurrentAccountCera(ctx, owner.Repositories, session), Item: currentNPCShopBuyAckEntry(entry, p.Definition, 1), Updates: []currentItemListEntry{currentGoldWalletItemListEntry(result.Actor.Stats["gold"])}, Slot: int16(result.PurchasedSlot), ItemID: q.ItemID, Count: 1, CostItemID: uint32(wedding.LovePointItemID), CostItemNewCount: state.LovePoints}, nil
}

func (s *Service) publishCurrentWeddingFurnitureRoom(session *gameSession) error {
	actor, bound := s.boundGameSessionCharacterSnapshot(session)
	if !bound {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), currentRepositorySnapshotTimeout)
	defer cancel()
	s.wedding.mu.Lock()
	defer s.wedding.mu.Unlock()
	owner, err := s.currentWeddingOwner()
	if err != nil {
		return err
	}
	req, _, err := s.currentWeddingPartnerRequest(ctx, actor)
	if err != nil {
		return err
	}
	// Re-read under the same publication lock: another spouse may have bought
	// or placed furniture while the normal shop ACK was being written.
	a, foundA, err := owner.Repositories.Character.Load(ctx, req.Actor.CharacterID)
	if err != nil {
		return err
	}
	b, foundB, err := owner.Repositories.Character.Load(ctx, req.Partner.CharacterID)
	if err != nil || !foundA || !foundB {
		return errors.Join(err, wedding.ErrState)
	}
	room, err := owner.ReadRoom(ctx, a, b)
	if err != nil {
		return err
	}
	for _, id := range []string{req.Actor.CharacterID, req.Partner.CharacterID} {
		numeric, parseErr := strconv.ParseUint(id, 10, 16)
		if parseErr != nil {
			return parseErr
		}
		peer, online := s.onlineGameSessionCharacterSnapshot(uint16(numeric))
		if online {
			err = errors.Join(err, s.writeCurrentWeddingRoom(peer.session, room))
		}
	}
	return err
}
