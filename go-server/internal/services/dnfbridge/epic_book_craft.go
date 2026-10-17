package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"strconv"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/epicbook"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

type currentEpicBookCraftResult struct {
	Craft   epicbook.CraftResult
	Updates []alignedcmd.ItemSlotRefresh
}

func (s *Service) commitCurrentEpicBookCraft(ctx context.Context, session *gameSession, catalog *epicbook.Catalog, items *pvfDungeonDropCatalog, output uint32, now time.Time) (currentEpicBookCraftResult, error) {
	if s == nil || session == nil || session.selectedCharacterID == 0 || items == nil || s.options.currentClientVersion != "official-24.9" || !s.epicPieceBook().enabled() {
		return currentEpicBookCraftResult{}, epicbook.ErrRecipe
	}
	if !s.epicPiecePush().snapshotEnabled() || s.epicPiecePush().forceEmptySnapshot() {
		return currentEpicBookCraftResult{}, epicbook.ErrRecipe
	}
	g, ok := s.repositoryGroup()
	if !ok {
		return currentEpicBookCraftResult{}, repo.ErrEpicBookTransactionUnavailable
	}
	assets, ok := g.AccountAssets.(repo.EpicBookAssetUnitOfWork)
	if !ok {
		return currentEpicBookCraftResult{}, repo.ErrEpicBookTransactionUnavailable
	}
	accountID := s.accountIDForSession(session)
	characterID := strconv.FormatUint(uint64(session.selectedCharacterID), 10)
	definition, err := items.ResolveItem(output)
	if err != nil {
		return currentEpicBookCraftResult{}, err
	}
	if definition.Kind != dungeonDropItemEquipment {
		return currentEpicBookCraftResult{}, epicbook.ErrRecipe
	}
	var updates []alignedcmd.ItemSlotRefresh
	result, err := epicbook.Craft(ctx, assets, catalog, accountID, characterID, output, now, func(ctx context.Context, _ repo.CharacterRecord, inventory *repo.InventoryRecord, outputID uint32) error {
		if outputID != definition.ItemID {
			return epicbook.ErrRecipe
		}
		if err := repo.BindInventoryContainerState(ctx, g.Settings, inventory); err != nil {
			return err
		}
		slots, err := grantCurrentCeraShopProductAt(inventory, definition, 1, now)
		if err != nil {
			return err
		}
		if len(slots) != 1 {
			return fmt.Errorf("epic book grant must create one equipment")
		}
		for _, slot := range slots {
			key := currentCeraShopInventorySlotKey(repo.MainInventoryListType, int16(slot))
			stack, found := inventory.Slots[key]
			if !found || stack.ItemID != int64(outputID) || stack.Count != 1 {
				return epicbook.ErrRecipe
			}
			entry := currentItemListEntryFromStack(repo.MainInventoryListType, int16(slot), stack)
			stack.RawEntry = append([]byte(nil), entry.data[:]...)
			inventory.Slots[key] = stack
			updates = append(updates, alignedcmd.ItemSlotRefresh{ListType: repo.MainInventoryListType, SlotIndex: int16(slot)})
		}
		return nil
	})
	if err != nil {
		return currentEpicBookCraftResult{}, err
	}
	for _, slot := range result.ChangedSoulSlots {
		updates = append(updates, alignedcmd.ItemSlotRefresh{ListType: repo.MainInventoryListType, SlotIndex: slot})
	}
	return currentEpicBookCraftResult{Craft: result, Updates: updates}, nil
}

func (s *Service) handleCurrentEpicBookCraft(session *gameSession, body []byte) error {
	const opcode = uint16(788)
	if session == nil || session.selectedCharacterID == 0 || len(body) != 4 || s.options.currentClientVersion != "official-24.9" {
		return s.sendGameUpperFailure(session, opcode, 3)
	}
	output := binary.LittleEndian.Uint32(body)
	s.initialEquipmentMu.Lock()
	archive, err := s.initialEquipmentArchiveLocked()
	s.initialEquipmentMu.Unlock()
	if err != nil {
		return s.sendGameUpperFailure(session, opcode, 3)
	}
	catalog, err := epicbook.LoadCatalog(archive)
	if err != nil {
		return s.sendGameUpperFailure(session, opcode, 3)
	}
	items, err := s.currentPVFItemCatalog()
	if err != nil {
		return s.sendGameUpperFailure(session, opcode, 3)
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	result, err := s.commitCurrentEpicBookCraft(ctx, session, catalog, items, output, time.Now().UTC())
	if err != nil {
		code := byte(3)
		if errors.Is(err, epicbook.ErrMaterial) || errors.Is(err, repo.ErrEpicBookPieceInsufficient) {
			code = 22
		}
		s.logGameEvent(session, "game-epic-book-craft-refused", "output_id", output, "reason", err)
		return s.sendGameUpperFailure(session, opcode, code)
	}
	if err := s.sendGameUpperSuccess(session, opcode, nil); err != nil {
		return err
	}
	if err := s.sendSelectedIncrementalItemSlotRefreshes(session, "epic_book_craft", result.Updates); err != nil {
		return err
	}
	// The usual credit update omits zero rows, so it cannot clear a consumed
	// piece. Re-read the full account after commit and send reset=0 even when
	// empty. This also reflects credits committed since this craft. The pinned
	// 2008190 consumer clears its map on reset=0 before reading count/pairs.
	g, ok := s.repositoryGroup()
	if !ok || g.Account == nil {
		return repo.ErrAccountEpicPieceCounterUnavailable
	}
	account, found, err := g.Account.Load(ctx, s.accountIDForSession(session))
	if err != nil {
		return err
	}
	if !found {
		return repo.ErrEpicBookOwnership
	}
	balanceBody, err := buildCurrentEpicPieceBalanceSyncBody(repo.AccountEpicPieceBalances(account), true)
	if err != nil {
		return err
	}
	return s.sendGameUpperRawClass(session, currentEpicPieceBalanceSyncOpcode, balanceBody, 0)
}
