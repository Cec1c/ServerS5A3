package mysql

import (
	"context"
	"database/sql"
	"strings"

	"longheng.io/server/internal/modules/dnf/repository"
)

// This hook is only created by the new owned BUFF transaction. It provides no
// asset writes, clear tokens, artifacts, or cross-owner load capability.
type mysqlBuffPetMetadataStore struct {
	mysqlStoreBase
	characterID string
}

func (s *mysqlBuffPetMetadataStore) LoadBuffPetEntries(ctx context.Context, characterID string) (repository.PetRecord, bool, error) {
	if err := ctxErr(ctx); err != nil {
		return repository.PetRecord{}, false, err
	}
	if s == nil || !s.router.lockReads || strings.TrimSpace(characterID) != s.characterID {
		return repository.PetRecord{}, false, repository.ErrBuffOwnedSnapshotUnavailable
	}
	table, err := s.router.readTable(mysqlPetTable, characterID)
	if err != nil {
		return repository.PetRecord{}, false, err
	}
	entries, err := s.router.readTable(mysqlPetEntriesTable, characterID)
	if err != nil {
		return repository.PetRecord{}, false, err
	}
	extra, err := s.router.readTable(mysqlPetExtraTable, characterID)
	if err != nil {
		return repository.PetRecord{}, false, err
	}
	var record repository.PetRecord
	var updatedAt sql.NullTime
	err = s.router.db.QueryRowContext(ctx, s.router.selectQuery("SELECT character_id, equipped_key, town_display, updated_at FROM "+table+" WHERE character_id = ?"), characterID).Scan(&record.CharacterID, &record.EquippedKey, &record.TownDisplay, &updatedAt)
	if err != nil {
		found, err := scanErr(err)
		return repository.PetRecord{}, found, err
	}
	// Preserve the old optional creature-enchant projection's foreign/missing
	// record behavior, while never reading foreign entries or using its metadata.
	if record.CharacterID != characterID {
		return repository.PetRecord{}, false, nil
	}
	record.Entries, err = loadBuffPetEntryMetadata(ctx, s.router, entries, extra, characterID)
	if err != nil {
		return repository.PetRecord{}, false, err
	}
	record.UpdatedAt = scanTime(updatedAt)
	return repository.ClonePet(record), true, nil
}

func loadBuffPetEntryMetadata(
	ctx context.Context,
	router mysqlRouter,
	entriesTable, extraTable, characterID string,
) (map[string]repository.PetEntry, error) {
	query := router.selectQuery("SELECT pet_key, creature_key, item_id, source_list_type, source_slot_index, pet_name, name_raw, satiety, satiety_micros, mode_flag, mode1_field_0a, mode1_field_0b, pet_level, pet_exp, tail_flag, raw_entry FROM " + entriesTable + " WHERE character_id = ? ORDER BY pet_key")
	rows, err := router.db.QueryContext(ctx, query, characterID)
	if err != nil {
		return nil, err
	}
	var entries map[string]repository.PetEntry
	for rows.Next() {
		var key string
		var entry repository.PetEntry
		var nameRaw, rawEntry []byte
		if err := rows.Scan(
			&key,
			&entry.CreatureKey,
			&entry.ItemID,
			&entry.SourceListType,
			&entry.SourceSlotIndex,
			&entry.Name,
			&nameRaw,
			&entry.Satiety,
			&entry.SatietyMicros,
			&entry.ModeFlag,
			&entry.Mode1Field0A,
			&entry.Mode1Field0B,
			&entry.Level,
			&entry.Exp,
			&entry.TailFlag,
			&rawEntry,
		); err != nil {
			rows.Close()
			return nil, err
		}
		entry.PetKey = key
		entry.NameRaw = append([]byte(nil), nameRaw...)
		entry.RawEntry = append([]byte(nil), rawEntry...)
		if entries == nil {
			entries = make(map[string]repository.PetEntry)
		}
		entries[key] = entry
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	extras, err := router.db.QueryContext(
		ctx,
		router.selectQuery("SELECT entry_key, extra_key, extra_value FROM "+extraTable+" WHERE character_id = ? ORDER BY entry_key, extra_key"),
		characterID,
	)
	if err != nil {
		return nil, err
	}
	for extras.Next() {
		var petKey, key, value string
		if err := extras.Scan(&petKey, &key, &value); err != nil {
			extras.Close()
			return nil, err
		}
		entry, ok := entries[petKey]
		if !ok {
			continue
		}
		if entry.Extra == nil {
			entry.Extra = make(map[string]string)
		}
		entry.Extra[key] = value
		entries[petKey] = entry
	}
	if err := extras.Err(); err != nil {
		extras.Close()
		return nil, err
	}
	if err := extras.Close(); err != nil {
		return nil, err
	}

	return entries, nil
}
