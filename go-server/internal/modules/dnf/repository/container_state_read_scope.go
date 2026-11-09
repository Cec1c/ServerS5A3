package repository

import "context"

// WithCharacterContainerRead adds only the current character's capacity
// snapshot to an existing transaction. Capacity writes remain with their owner.
func WithCharacterContainerRead(primary, readOnly SettingsRepository, id string) SettingsRepository {
	return &characterContainerRead{primary: primary, readOnly: readOnly, scope: CharacterContainerStateScope(id)}
}

type characterContainerRead struct {
	primary, readOnly SettingsRepository
	scope             string
}

func (s *characterContainerRead) Load(ctx context.Context, scope string) (SettingsRecord, bool, error) {
	if scope == s.scope && s.readOnly != nil {
		return s.readOnly.Load(ctx, scope)
	}
	if s.primary != nil {
		return s.primary.Load(ctx, scope)
	}
	return SettingsRecord{}, false, ErrCharacterSettlementTransactionUnavailable
}
func (s *characterContainerRead) Save(ctx context.Context, record SettingsRecord) error {
	if record.Scope == s.scope || s.primary == nil {
		return ErrCharacterSettlementTransactionUnavailable
	}
	return s.primary.Save(ctx, record)
}
