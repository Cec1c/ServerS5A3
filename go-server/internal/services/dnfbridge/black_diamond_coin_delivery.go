package dnfbridge

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"longheng.io/server/internal/modules/dnf/blackdiamond"
	"longheng.io/server/internal/modules/dnf/premium"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// Coin eligibility commits with the real fatigue debit. Delivery is a bounded,
// non-blocking selected-owner event; a full queue/mailbox leaves the durable
// pending ledger for the next room or town bootstrap.
func (s *Service) queueCurrentBlackDiamondCoins(session *gameSession, character dnfrepo.CharacterRecord) {
	if s == nil || session == nil || character.CharacterID != strconv.Itoa(int(session.selectedCharacterID)) || character.AccountID != s.accountIDForSession(session) {
		return
	}
	if character.Stats[premium.BlackDiamondFatigueCreditedTotalKey] <= character.Stats[premium.BlackDiamondFatigueDeliveredTotalKey] {
		return
	}
	if !session.blackDiamondCoinQueued.CompareAndSwap(false, true) {
		return
	}
	id, generation, err := gameSessionCharacterEventIdentity(session, session.selectedCharacterID)
	if err != nil {
		session.blackDiamondCoinQueued.Store(false)
		return
	}
	accountID := character.AccountID
	discard := func() { session.blackDiamondCoinQueued.Store(false) }
	err = s.tryPostGameSessionCharacterEventWithDiscard(session, "black-diamond-fatigue-coin", id, generation, func() error {
		defer discard()
		ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
		defer cancel()
		repos, ok := s.repositoryGroup()
		if !ok {
			return nil
		}
		coin, err := s.currentBlackDiamondCoinTemplate()
		if err != nil {
			s.logGameEvent(session, "black-diamond-coin-template-unavailable", "error", err)
			return nil
		}
		delivered, err := blackdiamond.DeliverBlackDiamondCoins(ctx, repos, accountID, character.CharacterID, s.gameplayNow(), coin)
		if err != nil {
			s.logGameEvent(session, "black-diamond-coin-delivery-pending", "error", err)
			return nil
		}
		if !delivered {
			return nil
		}
		return s.sendMailboxAlarmForSession(session, id, "black_diamond_fatigue_coin_committed")
	}, discard)
	if err != nil {
		discard()
		s.logGameEvent(session, "black-diamond-coin-owner-queue-full", "error", err)
	}
}

func (s *Service) currentBlackDiamondCoinTemplate() (dnfrepo.ItemStack, error) {
	items, err := s.currentPVFItemCatalog()
	if err != nil {
		return dnfrepo.ItemStack{}, err
	}
	definition, err := items.ResolveItem(uint32(premium.BlackDiamondCoinItemID))
	if err != nil {
		return dnfrepo.ItemStack{}, err
	}
	if definition.Kind != dungeonDropItemStackable || strings.Trim(definition.StackableType, "[] ") != "material" || definition.StackLimit < blackdiamond.CoinsPerMail {
		return dnfrepo.ItemStack{}, fmt.Errorf("black diamond coin PVF definition is invalid")
	}
	definition, err = currentPVFItemDefinitionForGrantAt(definition, s.gameplayNow())
	if err != nil {
		return dnfrepo.ItemStack{}, err
	}
	return dnfrepo.ItemStack{ItemID: premium.BlackDiamondCoinItemID, Count: 1, ExpireAt: definition.ExpirationDate,
		Extra: map[string]string{"item_kind": "stackable", "stackable_type": "[material]", "pvf_path": definition.PVFPath}}, nil
}
