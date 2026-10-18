package itemtrade

import (
	"context"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"math"
	"sort"
	"strings"
)

// TradableCrystalSlot only recognizes the six PVF free-trade cube pieces;
// account-bound souls never gain eligibility from sharing the same container.
func TradableCrystalSlot(itemID int64) (int16, bool) {
	slot, ok := repo.AccountWarehouseSlotForItem(itemID)
	return slot, ok && slot >= repo.CrystalWarehouseFirstSlot && slot <= repo.CrystalWarehouseLastSlot
}

type crystalTradeState struct {
	owners   map[string]string
	accounts map[string]repo.AccountInventoryRecord
	dirty    map[string]bool
}

func (o *Owner) withTradeRepositories(ctx context.Context, p []Participant, apply func(repo.CharacterRepository, repo.InventoryRepository, *crystalTradeState) error) error {
	shared := false
	for _, participant := range p {
		for _, offer := range participant.Offers {
			shared = shared || repo.IsAccountSharedInventorySlot(offer.SourceList, offer.SourceSlot)
		}
	}
	if !shared {
		return o.repositories.CharacterTrade.WithinCharacterTrade(ctx, p[0].CharacterID, p[1].CharacterID, func(c repo.CharacterRepository, i repo.InventoryRepository) error { return apply(c, i, nil) })
	}
	u, ok := o.repositories.CharacterTrade.(repo.CharacterWarehouseTradeUnitOfWork)
	if !ok || o.repositories.Character == nil {
		return ErrRepositoryUnavailable
	}
	owners := map[string]string{}
	unique := map[string]bool{}
	for _, participant := range p {
		character, found, err := o.repositories.Character.Load(ctx, participant.CharacterID)
		if err != nil {
			return err
		}
		account := strings.TrimSpace(character.AccountID)
		if !found || account == "" {
			return ErrInvalidParticipants
		}
		if participant.AccountID != "" && strings.TrimSpace(participant.AccountID) != account {
			return ErrInvalidParticipants
		}
		owners[participant.CharacterID] = account
		unique[account] = true
	}
	accounts := make([]string, 0, len(unique))
	for id := range unique {
		accounts = append(accounts, id)
	}
	sort.Strings(accounts)
	return u.WithinCharacterWarehouseTrade(ctx, []string{p[0].CharacterID, p[1].CharacterID}, accounts, func(c repo.CharacterRepository, i repo.InventoryRepository, a repo.AccountInventoryRepository) error {
		state := &crystalTradeState{owners: owners, accounts: map[string]repo.AccountInventoryRecord{}, dirty: map[string]bool{}}
		// Stable account-first locking agrees with account/character asset actions.
		for _, id := range accounts {
			record, found, err := a.Load(ctx, id)
			if err != nil {
				return err
			}
			if found && record.AccountID != id {
				return ErrInvalidParticipants
			}
			record.AccountID = id
			if record.Slots == nil {
				record.Slots = map[string]repo.ItemStack{}
			}
			state.accounts[id] = record
		}
		for _, participant := range p {
			record, found, err := c.Load(ctx, participant.CharacterID)
			if err != nil {
				return err
			}
			if !found || strings.TrimSpace(record.AccountID) != owners[participant.CharacterID] {
				return ErrInvalidParticipants
			}
		}
		if err := apply(c, i, state); err != nil {
			return err
		}
		for _, id := range accounts {
			if state.dirty[id] {
				record := state.accounts[id]
				record.UpdatedAt = o.clock().UTC()
				state.accounts[id] = record
				if err := a.Save(ctx, state.accounts[id]); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func (s *crystalTradeState) view(characterID string, record repo.InventoryRecord) repo.InventoryRecord {
	if s == nil {
		return record
	}
	return repo.MergeAccountSharedInventory(record, s.accounts[s.owners[characterID]])
}

func (s *crystalTradeState) remove(characterID string, row stagedOffer) (bool, error) {
	if s == nil || !repo.IsAccountSharedInventorySlot(row.offer.SourceList, row.offer.SourceSlot) {
		return false, nil
	}
	accountID := s.owners[characterID]
	record := s.accounts[accountID]
	stack, found := record.Slots[row.sourceKey]
	if !found || stack.ItemID != row.stack.ItemID || stack.Count < row.offer.Count {
		return true, ErrItemUnavailable
	}
	if stack.Count == row.offer.Count {
		delete(record.Slots, row.sourceKey)
	} else {
		stack.Count -= row.offer.Count
		record.Slots[row.sourceKey] = stack
	}
	s.accounts[accountID] = record
	s.dirty[accountID] = true
	return true, nil
}

func (s *crystalTradeState) receive(from, to string, row stagedOffer) (Transfer, bool, error) {
	if s == nil {
		return Transfer{}, false, nil
	}
	slot, ok := TradableCrystalSlot(row.stack.ItemID)
	if !ok {
		return Transfer{}, false, nil
	}
	accountID := s.owners[to]
	record := s.accounts[accountID]
	key := repo.AccountSharedInventorySlotKey(slot)
	incoming := cloneStack(row.stack)
	incoming.Count = row.offer.Count
	stack, exists := record.Slots[key]
	if exists {
		if stack.Count <= 0 || !repo.CanMergeItemStack(stack, incoming) {
			return Transfer{}, true, ErrItemNotTradeable
		}
		if incoming.Count > math.MaxInt32-stack.Count {
			return Transfer{}, true, ErrInventoryFull
		}
		stack = cloneStack(stack)
		stack.Count += incoming.Count
	} else {
		if incoming.Count > math.MaxInt32 {
			return Transfer{}, true, ErrInventoryFull
		}
		stack = incoming
	}
	record.Slots[key] = stack
	s.accounts[accountID] = record
	s.dirty[accountID] = true
	return Transfer{FromCharacterID: from, ToCharacterID: to, TradeSlot: row.offer.TradeSlot, DestinationList: repo.MainInventoryListType, DestinationSlot: slot, Stack: cloneStack(stack)}, true, nil
}
