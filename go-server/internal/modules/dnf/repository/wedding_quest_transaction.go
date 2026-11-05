package repository

import "context"

// WeddingQuestUnitOfWork keeps reciprocal relationship state and both
// preparation quest records in one transaction. It exposes no inventory,
// account or settings repositories; character wallets remain transactional.
type WeddingQuestUnitOfWork interface {
	WithinWeddingQuests(context.Context, []string, func(CharacterRepository, QuestRepository) error) error
}

// ScopeWeddingQuests limits the transaction to the exact character pair.
func ScopeWeddingQuests(base QuestRepository, ids []string) QuestRepository {
	allowed := make(map[string]bool, len(ids))
	for _, id := range ids {
		allowed[id] = true
	}
	return &weddingQuests{base: base, allowed: allowed}
}

type weddingQuests struct {
	base    QuestRepository
	allowed map[string]bool
}

func (q *weddingQuests) Load(ctx context.Context, id string) (QuestRecord, bool, error) {
	if !q.allowed[id] {
		return QuestRecord{}, false, ErrCharacterTradeTransactionUnavailable
	}
	return q.base.Load(ctx, id)
}
func (q *weddingQuests) Save(ctx context.Context, record QuestRecord) error {
	if !q.allowed[record.CharacterID] {
		return ErrCharacterTradeTransactionUnavailable
	}
	return q.base.Save(ctx, record)
}
func (q *weddingQuests) SaveFields(ctx context.Context, record QuestRecord, fields ...QuestField) error {
	if !q.allowed[record.CharacterID] {
		return ErrCharacterTradeTransactionUnavailable
	}
	return SaveQuestFields(ctx, q.base, record, fields...)
}
