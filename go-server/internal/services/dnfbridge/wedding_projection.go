package dnfbridge

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	r "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/wedding"
)

func (s *Service) publishCurrentWeddingPair(result wedding.PairResult) error {
	var published error
	for _, character := range []r.CharacterRecord{result.Actor, result.Partner} {
		id, err := wedding.NumericID(character.CharacterID)
		if err != nil {
			published = errors.Join(published, err)
			continue
		}
		identity, online := s.onlineGameSessionCharacterSnapshot(uint16(id))
		if !online || character.AccountID != s.accountIDForSession(identity.session) {
			continue
		}
		refreshes := []alignedcmd.ItemSlotRefresh{}
		for _, key := range result.ChangedSlots[character.CharacterID] {
			listText, slotText, valid := strings.Cut(key, ":")
			list, e1 := strconv.ParseInt(listText, 10, 8)
			slot, e2 := strconv.ParseInt(slotText, 10, 16)
			if !valid || e1 != nil || e2 != nil || list != int64(r.MainInventoryListType) {
				published = errors.Join(published, wedding.ErrItem)
				continue
			}
			refreshes = append(refreshes, alignedcmd.ItemSlotRefresh{ListType: r.MainInventoryListType, SlotIndex: int16(slot)})
		}
		if len(refreshes) > 0 {
			published = errors.Join(published, s.sendSelectedIncrementalItemSlotRefreshes(identity.session, "wedding_committed_pair", refreshes))
		}
		// Actor/inventory rebuilds can retire native marriage abilities. Keep
		// authoritative wedding and room state after the last item update,
		// including ceremony rewards, tree items and capsule operations.
		published = errors.Join(published, s.sendCurrentWeddingInfoAndPartnerLocked(identity.session, character))
		if result.Room != nil {
			published = errors.Join(published, s.writeCurrentWeddingRoom(identity.session, *result.Room))
		} else if state, err := wedding.Read(character); err == nil && state.Phase == wedding.Married {
			published = errors.Join(published, s.sendCurrentWeddingRoomSnapshot(identity.session, character))
		}
		published = errors.Join(published, s.broadcastCurrentWeddingPeerInfo(identity, character))
	}
	return published
}

func (s *Service) refreshCurrentWeddingQuests(actors ...boundGameSessionCharacter) error {
	var published error
	for _, actor := range actors {
		if !s.boundGameSessionCharacterCurrent(actor) {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), currentRepositorySnapshotTimeout)
		body, ok := s.buildCurrentAcceptableQuestListBodyForSession(ctx, actor.session)
		cancel()
		if !ok {
			continue
		}
		published = errors.Join(published, s.sendGameUpperRawClass(actor.session, 21, body, 0))
	}
	return published
}

func (s *Service) afterCurrentWeddingReservation(a, b boundGameSessionCharacter, result wedding.PairResult) error {
	// Publish the durable timer even if either socket has already disconnected.
	return errors.Join(s.publishCurrentWeddingPair(result), s.publishCurrentWeddingCeremonyReservationLocked(a, b, result), s.sendCarryGoldWallet(a.session, result.Actor.Stats["gold"]), s.refreshCurrentWeddingQuests(a, b))
}
