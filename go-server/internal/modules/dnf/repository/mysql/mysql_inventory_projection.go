package mysql

import (
	"context"
	"fmt"

	"longheng.io/server/internal/modules/dnf/repository"
)

func (s *mysqlBagStore) LoadInventorySlotPrefix(ctx context.Context, characterID, prefix string) (map[string]repository.ItemStack, error) {
	if characterID == "" || prefix == "" {
		return nil, fmt.Errorf("inventory slot projection requires owner and prefix")
	}
	table, err := s.router.readTable(mysqlBagTable, characterID)
	if err != nil {
		return nil, err
	}
	items, err := s.router.readTable(mysqlInventoryItemsTable, characterID)
	if err != nil {
		return nil, err
	}
	extra, err := s.router.readTable(mysqlInventoryExtraTable, characterID)
	if err != nil {
		return nil, err
	}
	var owner string
	if err := s.router.db.QueryRowContext(ctx, s.router.selectQuery("SELECT character_id FROM "+table+" WHERE character_id = ?"), characterID).Scan(&owner); err != nil {
		_, err = scanErr(err)
		return nil, err
	}
	// Never populate loadedSnapshot with this partial view.
	return loadItemStackCollectionPrefix(ctx, s.router.db, items, extra, "character_id", characterID, "slots", true, prefix)
}
