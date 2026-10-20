package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"longheng.io/server/internal/modules/dnf/repository"
)

// Construction is restricted to the owned snapshot transaction layer. No
// Group composition or base LegacyInventory path exposes this capability.
type mysqlBuffOwnedLegacyStore struct {
	mysqlStoreBase
	characterID string
}

func (s *mysqlBuffOwnedLegacyStore) SelectOwnedBuffItems(ctx context.Context, characterID string, lists []byte) ([]repository.OwnedLegacyItem, error) {
	if err := ctxErr(ctx); err != nil {
		return nil, err
	}
	if s == nil || !s.router.lockReads || strings.TrimSpace(characterID) != s.characterID {
		return nil, repository.ErrBuffOwnedSnapshotUnavailable
	}
	id, err := strconv.ParseInt(characterID, 10, 64)
	if err != nil || id <= 0 {
		return nil, repository.ErrBuffLegacyOwnerMismatch
	}
	selected := append([]byte(nil), lists...)
	sort.Slice(selected, func(i, j int) bool { return selected[i] < selected[j] })
	out := []repository.OwnedLegacyItem{}
	seenLists := map[byte]bool{}
	seenUID := map[int64]bool{}
	itemIndexes := map[int64]int{}
	table, err := s.router.readTable("legacy_character_items", characterID)
	if err != nil {
		return nil, err
	}
	for _, list := range selected {
		if list != 0 && list != 1 && list != 7 {
			return nil, fmt.Errorf("unsupported owned legacy buff list %d", list)
		}
		if seenLists[list] {
			continue
		}
		seenLists[list] = true
		// Exact owner-container index range; the enclosing transaction must use
		// RepeatableRead. Even an empty range is selected before registration.
		query := "SELECT item_uid, owner_scope, owner_id, character_id, item_kind, list_type, slot_index, item_template_id, stack_count, instance_value, durability, seal_flag, option_value, marker_16, pet_serial_or_handle FROM " + table + " WHERE owner_scope = 'character' AND owner_id = ? AND list_type = ? ORDER BY slot_index, item_uid FOR UPDATE"
		rows, err := s.router.db.QueryContext(ctx, query, id, int(list))
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var row repository.OwnedLegacyItem
			var ownerID int64
			var charID sql.NullInt64
			var listRaw, slotRaw int64
			err = rows.Scan(&row.ItemUID, &row.OwnerScope, &ownerID, &charID, &row.ItemKind, &listRaw, &slotRaw, &row.Item.ItemTemplateID, &row.Item.StackCount, &row.Item.InstanceValue, &row.Item.Durability, &row.Item.SealFlag, &row.Item.OptionValue, &row.Item.Marker16, &row.Item.PetSerialOrHandle)
			if err != nil {
				rows.Close()
				return nil, err
			}
			if row.ItemUID <= 0 || row.OwnerScope != "character" || ownerID != id || !charID.Valid || charID.Int64 != id || listRaw != int64(list) || slotRaw < 0 || slotRaw > 32767 {
				rows.Close()
				return nil, repository.ErrBuffLegacyOwnerMismatch
			}
			if seenUID[row.ItemUID] {
				rows.Close()
				return nil, repository.ErrBuffLegacyAmbiguous
			}
			seenUID[row.ItemUID] = true
			row.OwnerID = strconv.FormatInt(ownerID, 10)
			row.CharacterID = strconv.FormatInt(charID.Int64, 10)
			row.Item.ListType, row.Item.SlotIndex = list, int16(slotRaw)
			itemIndexes[row.ItemUID] = len(out)
			out = append(out, row)
		}
		err = rows.Err()
		closeErr := rows.Close()
		if err != nil {
			return nil, err
		}
		if closeErr != nil {
			return nil, closeErr
		}
		// Item rows are completely closed before reading UID-associated extras.
		// Both sides remain in this same owner/list locking transaction.
		extraTable, err := s.router.readTable(mysqlLegacyItemExtraTable, characterID)
		if err != nil {
			return nil, err
		}
		extraQuery := "SELECT item.item_uid, extra.extra_key, extra.extra_value FROM " + extraTable + " extra JOIN " + table + " item ON item.item_uid = extra.item_uid WHERE item.owner_scope = 'character' AND item.owner_id = ? AND item.character_id = ? AND item.list_type = ? ORDER BY item.item_uid, extra.extra_key FOR UPDATE"
		extras, err := s.router.db.QueryContext(ctx, extraQuery, id, id, int(list))
		if err != nil {
			return nil, err
		}
		for extras.Next() {
			var uid int64
			var key, value string
			if err := extras.Scan(&uid, &key, &value); err != nil {
				extras.Close()
				return nil, err
			}
			index, exists := itemIndexes[uid]
			if !exists || out[index].Item.ListType != list {
				extras.Close()
				return nil, repository.ErrBuffLegacyOwnerMismatch
			}
			if out[index].Item.Extra == nil {
				out[index].Item.Extra = make(map[string]string)
			}
			if _, duplicate := out[index].Item.Extra[key]; duplicate {
				extras.Close()
				return nil, repository.ErrBuffLegacyAmbiguous
			}
			out[index].Item.Extra[key] = value
		}
		if err := extras.Err(); err != nil {
			extras.Close()
			return nil, err
		}
		if err := extras.Close(); err != nil {
			return nil, err
		}
	}
	return out, nil
}
