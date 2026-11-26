package repository

import "context"

// CharacterWeaponSkinStateReader is an optional narrow, read-only projection
// returning only the fields needed to validate a character's active weapon
// skin appearance (CharacterID, Job, and the selected/owned weapon-skin stat
// entries), never a writable CharacterRecord.
type CharacterWeaponSkinStateReader interface {
	LoadCharacterWeaponSkinState(context.Context, string) (CharacterRecord, bool, error)
}

func LoadCharacterWeaponSkinState(ctx context.Context, store CharacterRepository, id string) (CharacterRecord, bool, error) {
	if reader, ok := store.(CharacterWeaponSkinStateReader); ok {
		return reader.LoadCharacterWeaponSkinState(ctx, id)
	}
	return store.Load(ctx, id)
}
