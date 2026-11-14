package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"longheng.io/server/internal/modules/dnf/repository"
)

// LoadInventorySlotItemPrefix reads the selected items and their attributes in
// one SQL view. The owner join excludes orphan rows without loading a full bag;
// the extra join is constrained by owner, collection and entry key.
func (s *mysqlBagStore) LoadInventorySlotItemPrefix(ctx context.Context, characterID, prefix string, itemID int64) (map[string]repository.ItemStack, error) {
	if characterID == "" || prefix == "" || itemID <= 0 {
		return nil, fmt.Errorf("inventory item projection requires owner, prefix and positive item ID")
	}
	owners, err := s.router.readTable(mysqlBagTable, characterID)
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
	query := "SELECT i.entry_key, i.item_id, i.item_count, i.bind_flag, i.expire_at, i.raw_entry, e.extra_key, e.extra_value FROM " + items + " i" +
		" INNER JOIN " + owners + " b ON b.character_id = i.character_id" +
		" LEFT JOIN " + extra + " e ON e.character_id = i.character_id AND e.collection_name = i.collection_name AND e.entry_key = i.entry_key" +
		" WHERE i.character_id = ? AND i.collection_name = ? AND i.item_id = ? AND i.entry_key LIKE ? ESCAPE '!' ORDER BY i.entry_key, e.extra_key"
	pattern := strings.NewReplacer("!", "!!", "%", "!%", "_", "!_").Replace(prefix) + "%"
	rows, err := s.router.db.QueryContext(ctx, s.router.selectQuery(query), characterID, "slots", itemID, pattern)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result map[string]repository.ItemStack
	for rows.Next() {
		var key string
		var stack repository.ItemStack
		var expiry sql.NullTime
		var raw []byte
		var extraKey, extraValue sql.NullString
		if err := rows.Scan(&key, &stack.ItemID, &stack.Count, &stack.Bind, &expiry, &raw, &extraKey, &extraValue); err != nil {
			return nil, err
		}
		stack.ExpireAt = scanTime(expiry)
		if previous, found := result[key]; found {
			stack.RawEntry, stack.Extra = previous.RawEntry, previous.Extra
		} else {
			stack.RawEntry = append([]byte(nil), raw...)
		}
		if extraKey.Valid && extraValue.Valid {
			if stack.Extra == nil {
				stack.Extra = make(map[string]string)
			}
			stack.Extra[extraKey.String] = extraValue.String
		}
		if result == nil {
			result = make(map[string]repository.ItemStack)
		}
		result[key] = stack
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	// This partial projection must neither replace nor seed loadedSnapshot.
	return result, nil
}
