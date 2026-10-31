package mysql

import (
	"context"
	"reflect"

	"longheng.io/server/internal/modules/dnf/repository"
)

// 只处理已锁读宠物的成长字段；进化、增删宠物、实例或神器变化均回原完整写入。
// 先检查整份快照，再发SQL，不能在发现不支持的变化前写入部分条目。
func savePetGrowthChanges(ctx context.Context, database SQLDB, entriesTable, tokensTable string, before, after repository.PetRecord) (bool, error) {
	if len(before.Entries) != len(after.Entries) || !reflect.DeepEqual(before.Artifacts, after.Artifacts) {
		return false, nil
	}
	for key, current := range after.Entries {
		previous, exists := before.Entries[key]
		if !exists {
			return false, nil
		}
		previous.Level, previous.Exp = current.Level, current.Exp
		previous.Satiety, previous.SatietyMicros = current.Satiety, current.SatietyMicros
		previous.AppliedClearTokens, previous.AppliedClearTokenOrder = current.AppliedClearTokens, current.AppliedClearTokenOrder
		if !reflect.DeepEqual(previous, current) {
			return false, nil
		}
	}
	for _, key := range sortedStringKeys(after.Entries) {
		previous, current := before.Entries[key], after.Entries[key]
		if previous.Level != current.Level || previous.Exp != current.Exp || previous.Satiety != current.Satiety || previous.SatietyMicros != current.SatietyMicros {
			if _, err := database.ExecContext(ctx, "UPDATE "+entriesTable+" SET satiety = ?, satiety_micros = ?, pet_level = ?, pet_exp = ? WHERE character_id = ? AND pet_key = ?",
				current.Satiety, current.SatietyMicros, current.Level, current.Exp, after.CharacterID, key); err != nil {
				return true, err
			}
		}
		if reflect.DeepEqual(previous.AppliedClearTokens, current.AppliedClearTokens) && reflect.DeepEqual(previous.AppliedClearTokenOrder, current.AppliedClearTokenOrder) {
			continue
		}
		// 有界回执顺序保留原索引语义；用既有批量器合并写回，避免128次逐行往返。
		if _, err := database.ExecContext(ctx, "DELETE FROM "+tokensTable+" WHERE character_id = ? AND pet_key = ?", after.CharacterID, key); err != nil {
			return true, err
		}
		batch := newRowInsertBatcher(ctx, database, after.CharacterID, insertShape{
			table: tokensTable, collection: "pet_clear_tokens", columns: 5,
			prefix: "INSERT INTO " + tokensTable + " (character_id, pet_key, token_order, token, applied) VALUES ", placeholders: "(?, ?, ?, ?, ?)",
		})
		for index, token := range orderedPetClearTokens(current) {
			if err := batch.add(after.CharacterID, key, index, token, current.AppliedClearTokens[token]); err != nil {
				return true, err
			}
		}
		if err := batch.flush(); err != nil {
			return true, err
		}
	}
	return true, nil
}

func orderedPetClearTokens(entry repository.PetEntry) []string {
	order := make([]string, 0, len(entry.AppliedClearTokens))
	seen := make(map[string]bool, len(entry.AppliedClearTokens))
	for _, token := range entry.AppliedClearTokenOrder {
		if _, exists := entry.AppliedClearTokens[token]; exists && !seen[token] {
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
