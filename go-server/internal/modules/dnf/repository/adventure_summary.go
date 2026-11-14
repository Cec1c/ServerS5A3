package repository

import "context"

// AccountCharacterLevel is a read-only projection, never a saveable record.
type AccountCharacterLevel struct {
	Level   int
	Deleted bool
}

type AccountCharacterLevelsReader interface {
	ListAccountCharacterLevels(context.Context, string, int) ([]AccountCharacterLevel, error)
}

type AccountHonorExperienceReader interface {
	LoadAccountHonorExperience(context.Context, string) (uint64, bool, error)
}

func ListAccountCharacterLevels(ctx context.Context, characters CharacterRepository, account string, limit int) ([]AccountCharacterLevel, error) {
	if reader, ok := characters.(AccountCharacterLevelsReader); ok {
		return reader.ListAccountCharacterLevels(ctx, account, limit)
	}
	charactersForAccount, err := characters.ListByAccount(ctx, account, limit)
	if err != nil {
		return nil, err
	}
	rows := make([]AccountCharacterLevel, 0, len(charactersForAccount))
	for _, ch := range charactersForAccount {
		rows = append(rows, AccountCharacterLevel{Level: ch.Level, Deleted: ch.Stats["delete_flag"] != 0})
	}
	return rows, nil
}

func LoadAccountHonorExperience(ctx context.Context, accounts AccountRepository, account string) (uint64, bool, error) {
	if reader, ok := accounts.(AccountHonorExperienceReader); ok {
		return reader.LoadAccountHonorExperience(ctx, account)
	}
	record, found, err := accounts.Load(ctx, account)
	return record.HonorExp, found && record.AccountID == account, err
}

func (s *RedisCachedCharStore) ListAccountCharacterLevels(ctx context.Context, account string, limit int) ([]AccountCharacterLevel, error) {
	return ListAccountCharacterLevels(ctx, s.backing, account, limit)
}
