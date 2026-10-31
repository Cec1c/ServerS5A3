package mysql

import (
	"context"
	"errors"
	"maps"
	"reflect"
	"slices"
	"strings"

	"longheng.io/server/internal/modules/dnf/repository"
)

func (s *mysqlPetStore) SavePetGrowth(ctx context.Context, after repository.PetRecord, key string, before repository.PetEntry) error {
	if !s.router.lockReads {
		return errors.New("pet growth delta requires locked transaction")
	}
	if _, ok := s.router.db.(sqlTransactionDB); !ok {
		return errors.New("pet growth delta requires SQL transaction")
	}
	id, err := requireRecordKey(repository.PetKey, after, "pet")
	if err != nil {
		return err
	}
	next, found := after.Entries[key]
	if !found || key == "" || after.EquippedKey != key || before.PetKey != next.PetKey {
		return errors.New("pet growth delta identity mismatch")
	}
	// This API must never silently omit a new mutation outside growth fields.
	unchanged := next
	unchanged.ItemID, unchanged.Level, unchanged.Exp = before.ItemID, before.Level, before.Exp
	unchanged.Satiety, unchanged.SatietyMicros = before.Satiety, before.SatietyMicros
	unchanged.AppliedClearTokens, unchanged.AppliedClearTokenOrder = before.AppliedClearTokens, before.AppliedClearTokenOrder
	if !reflect.DeepEqual(unchanged, before) {
		return errors.New("pet growth delta contains unsupported field changes")
	}
	parent, err := s.router.writeTable(mysqlPetTable, id)
	if err != nil {
		return err
	}
	entries, err := s.router.writeTable(mysqlPetEntriesTable, id)
	if err != nil {
		return err
	}
	tokens, err := s.router.writeTable(mysqlPetTokensTable, id)
	if err != nil {
		return err
	}
	sets := []string{}
	args := []any{}
	add := func(column string, old, value any) {
		if old != value {
			sets = append(sets, column+" = ?")
			args = append(args, value)
		}
	}
	add("item_id", before.ItemID, next.ItemID)
	add("pet_level", before.Level, next.Level)
	add("pet_exp", before.Exp, next.Exp)
	add("satiety", before.Satiety, next.Satiety)
	add("satiety_micros", before.SatietyMicros, next.SatietyMicros)
	if len(sets) > 0 {
		args = append(args, id, key)
		if _, err := s.router.db.ExecContext(ctx, "UPDATE "+entries+" SET "+strings.Join(sets, ", ")+" WHERE character_id = ? AND pet_key = ?", args...); err != nil {
			return err
		}
	}
	if !maps.Equal(before.AppliedClearTokens, next.AppliedClearTokens) || !slices.Equal(before.AppliedClearTokenOrder, next.AppliedClearTokenOrder) {
		if err := savePetGrowthTokens(ctx, s.router.db, tokens, id, key, before, next); err != nil {
			return err
		}
	}
	_, err = s.router.db.ExecContext(ctx, "UPDATE "+parent+" SET updated_at = ? WHERE character_id = ?", timeOrNow(after.UpdatedAt, s.router.now), id)
	return err
}

func orderedPetTokens(entry repository.PetEntry) []string {
	order := make([]string, 0, len(entry.AppliedClearTokens))
	seen := map[string]bool{}
	for _, token := range entry.AppliedClearTokenOrder {
		if _, present := entry.AppliedClearTokens[token]; present && !seen[token] {
			seen[token] = true
			order = append(order, token)
		}
	}
	for _, token := range sortedStringKeys(entry.AppliedClearTokens) {
		if !seen[token] {
			order = append(order, token)
		}
	}
	return order
}

func savePetGrowthTokens(ctx context.Context, db SQLDB, table, id, key string, before, next repository.PetEntry) error {
	oldOrder, newOrder := orderedPetTokens(before), orderedPetTokens(next)
	// The common case retains a suffix of the old window and appends one
	// token. Shift its ordinals in one ordered UPDATE, without reinserting it.
	removed := 0
	for removed < len(oldOrder) {
		if _, exists := next.AppliedClearTokens[oldOrder[removed]]; exists {
			break
		}
		removed++
	}
	kept := oldOrder[removed:]
	appendOnly := len(newOrder) >= len(kept) && slices.Equal(kept, newOrder[:len(kept)])
	for _, token := range kept {
		if before.AppliedClearTokens[token] != next.AppliedClearTokens[token] {
			appendOnly = false
		}
	}
	if appendOnly && len(oldOrder) > 0 {
		// Older imports may have one-based or sparse ordinals. Load discards
		// their numeric values, so validate the physical sequence before shifting.
		rows, err := db.QueryContext(ctx, "SELECT token_order, token FROM "+table+" WHERE character_id = ? AND pet_key = ? ORDER BY token_order FOR UPDATE", id, key)
		if err != nil {
			return err
		}
		index := 0
		for rows.Next() {
			var ordinal int
			var token string
			if err := rows.Scan(&ordinal, &token); err != nil {
				rows.Close()
				return err
			}
			if index >= len(oldOrder) || ordinal != index || token != oldOrder[index] {
				appendOnly = false
			}
			index++
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if index != len(oldOrder) {
			appendOnly = false
		}
	}
	if appendOnly {
		for _, token := range oldOrder[:removed] {
			if _, err := db.ExecContext(ctx, "DELETE FROM "+table+" WHERE character_id = ? AND pet_key = ? AND token = ?", id, key, token); err != nil {
				return err
			}
		}
		if removed > 0 && len(kept) > 0 {
			if _, err := db.ExecContext(ctx, "UPDATE "+table+" SET token_order = token_order - ? WHERE character_id = ? AND pet_key = ? ORDER BY token_order ASC", removed, id, key); err != nil {
				return err
			}
		}
	} else {
		// Legacy unordered/false token migration is limited to this creature.
		if _, err := db.ExecContext(ctx, "DELETE FROM "+table+" WHERE character_id = ? AND pet_key = ?", id, key); err != nil {
			return err
		}
		kept = nil
	}
	for i := len(kept); i < len(newOrder); i++ {
		token := newOrder[i]
		if _, err := db.ExecContext(ctx, "INSERT INTO "+table+" (character_id, pet_key, token_order, token, applied) VALUES (?, ?, ?, ?, ?)", id, key, i, token, next.AppliedClearTokens[token]); err != nil {
			return err
		}
	}
	return nil
}
