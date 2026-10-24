package dnfbridge

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
	"strconv"

	dnfinventory "longheng.io/server/internal/modules/dnf/inventory"
	dnfpet "longheng.io/server/internal/modules/dnf/pet"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

func (s *Service) handleCurrentPetFeed(session *gameSession, body []byte) (bool, error) {
	request, err := dnfinventory.DecodeUseStackableRequest(body)
	if err != nil || request.ListType != 7 {
		return false, nil
	}
	if session == nil || session.selectedCharacterID == 0 {
		return false, nil
	}
	reject := func(err error) (bool, error) {
		return true, s.answerRejectedUseStackable(session, body, "pet_feed: "+err.Error())
	}
	catalog, err := s.currentPetPVFCatalog()
	if err != nil {
		return reject(err)
	}
	// List 7 never falls through to generic waste consumption. The catalog and
	// owner must prove [feed], the exact persisted source and equipped creature.
	repositories, ok := s.repositoryGroup()
	if !ok {
		return reject(dnfpet.ErrPetTransactionUnavailable)
	}
	owner, err := dnfpet.NewFeedOwner(repositories, catalog)
	if err != nil {
		return reject(err)
	}
	session.petGrowth.mu.Lock()
	now := s.gameplayNow()
	err = s.settleCurrentPetGrowthClockLocked(session, now, "before_pet_feed")
	var result dnfpet.FeedResult
	if err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
		result, err = owner.Feed(ctx, dnfpet.FeedCommand{AccountID: s.accountIDForSession(session), CharacterID: strconv.Itoa(int(session.selectedCharacterID)), ListType: request.ListType, Slot: request.SlotIndex, ItemID: int64(request.ItemCode), Now: now, Expired: currentDungeonDiscardStackExpired, SetCount: currentPetFeedSetCount})
		cancel()
	}
	session.petGrowth.mu.Unlock()
	if err != nil {
		return reject(err)
	}
	// v153 class1/op44 consumes slot/list/instance/template, invokes the native
	// item-use callback and decrements the existing source. ACK must precede
	// the absolute satiety correction and authoritative remaining source row.
	if err := s.sendGameUpperRawClass(session, 44, buildCurrentExpertJobRecipeLearningSuccessBody(request.SlotIndex, request.ListType, request.InstanceValue, request.ItemCode), 1); err != nil {
		return true, err
	}
	if err := s.sendGameUpperRawClass(session, 103, buildCurrentPetSatietyBody(result.PetKey, result.After.Satiety), 0); err != nil {
		return true, err
	}
	row := currentItemListEntryFromStack(7, request.SlotIndex, result.RemainingStack)
	if err := s.sendCurrentSceneItemList(session, 14, buildCurrentItemUpdateBody(7, []currentItemListEntry{row})); err != nil {
		return true, err
	}
	s.logGameEvent(session, "game-pet-feed-committed", "char_id", session.selectedCharacterID, "pet_key", result.PetKey, "item_id", request.ItemCode, "slot", request.SlotIndex, "remaining", result.Remaining, "before", result.Before.Satiety, "after", result.After.Satiety, "before_micros", result.Before.SatietyMicros, "after_micros", result.After.SatietyMicros)
	return true, nil
}

// The notification registry is populated by 2524320 -> 2001F60 and selected by
// the class0 dispatcher 251E340. 1FE9490 reads key/u32 gauge with no ACK flag.
// Its 28CAE40 setter changes only the timestamp and gauge, retaining pet state.
func buildCurrentPetSatietyBody(key uint32, satiety int) []byte {
	body := make([]byte, 8)
	binary.LittleEndian.PutUint32(body[0:4], key)
	binary.LittleEndian.PutUint32(body[4:8], uint32(satiety))
	return body
}

func currentPetFeedSetCount(stack repo.ItemStack, count int64) (repo.ItemStack, error) {
	if count <= 0 || count > math.MaxUint32 {
		return repo.ItemStack{}, dnfpet.ErrPetFeedInvalid
	}
	stack = repo.CloneInventory(repo.InventoryRecord{Slots: map[string]repo.ItemStack{"x": stack}}).Slots["x"]
	stack.Count = count
	if len(stack.RawEntry) != 0 {
		if len(stack.RawEntry) != 119 {
			return repo.ItemStack{}, fmt.Errorf("pet feed raw row length %d", len(stack.RawEntry))
		}
		binary.LittleEndian.PutUint32(stack.RawEntry[6:10], uint32(count))
	}
	for _, key := range []string{"raw_entry_hex", "current_raw_entry_77", "raw_entry_77"} {
		if stack.Extra[key] == "" {
			continue
		}
		b, err := aikenExtraBytes(stack.Extra[key])
		if err != nil || len(b) != 119 {
			return repo.ItemStack{}, dnfpet.ErrPetFeedInvalid
		}
		binary.LittleEndian.PutUint32(b[6:10], uint32(count))
		stack.Extra[key] = hex.EncodeToString(b)
	}
	for _, key := range []string{"amount_or_count", "amount", "count", "stack", "quantity"} {
		if _, ok := stack.Extra[key]; ok {
			stack.Extra[key] = strconv.FormatInt(count, 10)
		}
	}
	return stack, nil
}
