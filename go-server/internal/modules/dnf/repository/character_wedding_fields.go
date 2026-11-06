package repository

import (
	"context"
	"strings"
)

type characterWeddingWriteContextKey struct{}

// CharacterSaveFields recovers explicit fields through Store-only decorators.
// Ordinary direct Save calls retain the regular full-save field set.
func CharacterSaveFields(ctx context.Context) []CharacterField {
	if ctx != nil {
		if fields, ok := ctx.Value(characterWeddingWriteContextKey{}).([]CharacterField); ok {
			return append([]CharacterField(nil), fields...)
		}
	}
	return AllCharacterFields()
}

const CharacterWeddingStatPrefix = "wedding_v1_"

func IsCharacterWeddingStat(key string) bool {
	return strings.HasPrefix(key, CharacterWeddingStatPrefix)
}

// MergeCharacterFields gives the memory backend the same explicit field and
// wedding namespace ownership as MySQL. Returned maps never alias either input.
func MergeCharacterFields(current, incoming CharacterRecord, fields ...CharacterField) CharacterRecord {
	out := CloneCharacter(current)
	out.CharacterID = incoming.CharacterID
	out.UpdatedAt = incoming.UpdatedAt
	for _, field := range CharacterFields.Normalize(fields) {
		switch field {
		case CharacterFieldBase:
			out.AccountID, out.Slot, out.Name = incoming.AccountID, incoming.Slot, incoming.Name
			out.Job, out.Level, out.CreatedAt = incoming.Job, incoming.Level, incoming.CreatedAt
		case CharacterFieldStats:
			for key := range out.Stats {
				if !IsCharacterWeddingStat(key) {
					delete(out.Stats, key)
				}
			}
			if out.Stats == nil {
				out.Stats = make(map[string]int64)
			}
			for key, value := range incoming.Stats {
				if !IsCharacterWeddingStat(key) {
					out.Stats[key] = value
				}
			}
		case CharacterFieldWedding:
			for key := range out.Stats {
				if IsCharacterWeddingStat(key) {
					delete(out.Stats, key)
				}
			}
			if out.Stats == nil {
				out.Stats = make(map[string]int64)
			}
			for key, value := range incoming.Stats {
				if IsCharacterWeddingStat(key) {
					out.Stats[key] = value
				}
			}
		case CharacterFieldLocation:
			out.Location = incoming.Location
		case CharacterFieldRoster:
			out.Roster = cloneCharacterRoster(incoming.Roster)
		}
	}
	return out
}
