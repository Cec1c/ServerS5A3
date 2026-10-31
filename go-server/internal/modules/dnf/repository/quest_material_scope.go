package repository

import "context"

// QuestMaterialPreparationGroup excludes asset repositories and bounds every
// key to the frozen recipient set. Preparation never writes inventory.
func QuestMaterialPreparationGroup(base Group, ids []string) Group {
	allowed := map[string]bool{}
	for _, id := range ids {
		if id != "" {
			allowed[id] = true
		}
	}
	return Group{Quest: &materialQuests{base.Quest, allowed}, Settings: &materialSettings{base.Settings, allowed}}
}

type materialQuests struct {
	base QuestRepository
	ids  map[string]bool
}

func (s *materialQuests) LoadQuestActivePlanning(ctx context.Context, id string) (QuestRecord, bool, error) {
	if !s.ids[id] {
		return QuestRecord{}, false, ErrCharacterSettlementTransactionUnavailable
	}
	return LoadQuestActivePlanning(ctx, s.base, id)
}
func (s *materialQuests) SaveQuestChanges(ctx context.Context, before, after QuestRecord, fields ...QuestField) error {
	if !s.ids[before.CharacterID] || before.CharacterID != after.CharacterID {
		return ErrCharacterSettlementTransactionUnavailable
	}
	return SaveQuestChanges(ctx, s.base, before, after, fields...)
}

func (s *materialQuests) Load(ctx context.Context, id string) (QuestRecord, bool, error) {
	if !s.ids[id] {
		return QuestRecord{}, false, ErrCharacterSettlementTransactionUnavailable
	}
	return s.base.Load(ctx, id)
}
func (s *materialQuests) Save(ctx context.Context, q QuestRecord) error {
	if !s.ids[q.CharacterID] {
		return ErrCharacterSettlementTransactionUnavailable
	}
	return s.base.Save(ctx, q)
}

type materialSettings struct {
	base SettingsRepository
	ids  map[string]bool
}

func (s *materialSettings) allowed(scope string) bool {
	for id := range s.ids {
		if QuestMaterialSettingsScope(id, scope) {
			return true
		}
	}
	return false
}
func (s *materialSettings) Load(ctx context.Context, key string) (SettingsRecord, bool, error) {
	if !s.allowed(key) {
		return SettingsRecord{}, false, ErrCharacterSettlementTransactionUnavailable
	}
	return s.base.Load(ctx, key)
}
func (s *materialSettings) Save(ctx context.Context, q SettingsRecord) error {
	if !s.allowed(q.Scope) {
		return ErrCharacterSettlementTransactionUnavailable
	}
	return s.base.Save(ctx, q)
}
