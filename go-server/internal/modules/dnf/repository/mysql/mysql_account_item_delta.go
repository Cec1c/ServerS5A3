package mysql

import (
	"context"
	"reflect"
	"sort"

	"longheng.io/server/internal/modules/dnf/repository"
)

// changedAccountItemKeys includes inserts, removals and every changed instance
// field (including raw bytes and extra). Comparing the authoritative before and
// after maps also covers redirected auto-stack destinations and split moves.
func changedAccountItemKeys(before, after map[string]repository.ItemStack) []string {
	keys := make([]string, 0)
	for key, previous := range before {
		current, found := after[key]
		if !found || !reflect.DeepEqual(previous, current) {
			keys = append(keys, key)
		}
	}
	for key := range after {
		if _, found := before[key]; !found {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys
}

func (s *mysqlBagStore) saveAccountTransactionCollection(ctx context.Context, database SQLDB, itemsTable, extraTable string, record repository.InventoryRecord, field repository.InventoryField) error {
	values := record.Slots
	if field == repository.InventoryFieldWarehouse {
		values = record.Warehouse
	}
	if s.trackSlotChanges && s.loadedSnapshot != nil && s.loadedSnapshot.CharacterID == record.CharacterID {
		before := s.loadedSnapshot.Slots
		if field == repository.InventoryFieldWarehouse {
			before = s.loadedSnapshot.Warehouse
		}
		keys := changedAccountItemKeys(before, values)
		if len(keys) == 0 {
			return nil
		}
		return replaceItemStackKeys(ctx, database, itemsTable, extraTable, "character_id", record.CharacterID, string(field), true, values, keys)
	}
	return replaceItemStackCollection(ctx, database, itemsTable, extraTable, "character_id", record.CharacterID, string(field), true, values)
}

// A partial save must not advance the baseline of an unsaved collection. A
// save without a prior successful Load cannot create a complete before-image.
func (s *mysqlBagStore) rememberAccountTransactionFields(record repository.InventoryRecord, fields []repository.InventoryField) {
	if s.loadedSnapshot == nil || s.loadedSnapshot.CharacterID != record.CharacterID {
		return
	}
	copy := repository.CloneInventory(record)
	for _, field := range fields {
		switch field {
		case repository.InventoryFieldSlots:
			s.loadedSnapshot.Slots = copy.Slots
		case repository.InventoryFieldWarehouse:
			s.loadedSnapshot.Warehouse = copy.Warehouse
		}
	}
	s.loadedSnapshot.UpdatedAt = record.UpdatedAt
}

// Only advance keys actually persisted by a successful slot-delta write.
// Other in-memory edits and the other collection remain dirty against the last
// known database state, including inserts and removals.
func (s *mysqlBagStore) rememberAccountTransactionKeys(record repository.InventoryRecord, field repository.InventoryField, keys []string) {
	if s.loadedSnapshot == nil || s.loadedSnapshot.CharacterID != record.CharacterID {
		return
	}
	copy := repository.CloneInventory(record)
	var before, after map[string]repository.ItemStack
	switch field {
	case repository.InventoryFieldSlots:
		if s.loadedSnapshot.Slots == nil {
			s.loadedSnapshot.Slots = make(map[string]repository.ItemStack)
		}
		before, after = s.loadedSnapshot.Slots, copy.Slots
	case repository.InventoryFieldWarehouse:
		if s.loadedSnapshot.Warehouse == nil {
			s.loadedSnapshot.Warehouse = make(map[string]repository.ItemStack)
		}
		before, after = s.loadedSnapshot.Warehouse, copy.Warehouse
	default:
		return
	}
	for _, key := range keys {
		if value, ok := after[key]; ok {
			before[key] = value
		} else {
			delete(before, key)
		}
	}
	s.loadedSnapshot.UpdatedAt = record.UpdatedAt
}
