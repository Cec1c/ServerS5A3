package repository

import "context"

// CharacterIdentity cannot be saved as a partial CharacterRecord. It is only
// for existence and ownership checks; callers needing gameplay fields use Load.
type CharacterIdentity struct {
	CharacterID string
	AccountID   string
}

type CharacterIdentityReader interface {
	LoadCharacterIdentity(context.Context, string) (CharacterIdentity, bool, error)
}

func LoadCharacterIdentity(ctx context.Context, characters CharacterRepository, id string) (CharacterIdentity, bool, error) {
	if narrow, ok := characters.(CharacterIdentityReader); ok {
		return narrow.LoadCharacterIdentity(ctx, id)
	}
	record, found, err := characters.Load(ctx, id)
	return CharacterIdentity{CharacterID: record.CharacterID, AccountID: record.AccountID}, found, err
}
