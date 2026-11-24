package skillcmd

import (
	"context"
	"fmt"
	"strings"
	"time"

	darkcombo "longheng.io/server/internal/modules/dnf/darkknightcombo"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

// PVPComboScope belongs to the same independent profile as PK/dojo skills.
func PVPComboScope(characterID string) string {
	return "character:" + strings.TrimSpace(characterID) + ":pvp_dark_knight_combo_v1"
}

// The combo codec keeps its ordinary scope; this view redirects only the
// selected character's combo record, including inside settlement transactions.
type pvpComboSettings struct {
	base        repo.SettingsRepository
	characterID string
}

func (s pvpComboSettings) Load(ctx context.Context, scope string) (repo.SettingsRecord, bool, error) {
	if s.base == nil {
		return repo.SettingsRecord{}, false, ErrOwnerUnavailable
	}
	if scope != darkcombo.SettingsScope(s.characterID) {
		return s.base.Load(ctx, scope)
	}
	record, found, err := s.base.Load(ctx, PVPComboScope(s.characterID))
	if err != nil || !found {
		return record, found, err
	}
	if record.Scope != PVPComboScope(s.characterID) {
		return repo.SettingsRecord{}, false, fmt.Errorf("PK combo record owner mismatch")
	}
	record = repo.CloneSettings(record)
	record.Scope = scope
	return record, true, nil
}

func (s pvpComboSettings) Save(ctx context.Context, record repo.SettingsRecord) error {
	if s.base == nil {
		return ErrOwnerUnavailable
	}
	if record.Scope == darkcombo.SettingsScope(s.characterID) {
		record = repo.CloneSettings(record)
		record.Scope = PVPComboScope(s.characterID)
	}
	return s.base.Save(ctx, record)
}

// Seed once under the existing character transaction. Preserve the legacy
// order as a starting point, filter it against PK skills at projection time,
// and never copy it again over an existing (including explicitly empty) page.
func preparePVPCombo(ctx context.Context, base repo.Group, characterID string, now time.Time) error {
	character, found, err := base.Character.Load(ctx, characterID)
	if err != nil || !found {
		return err
	}
	if character.Job != "9" {
		return nil
	}
	_, exists, err := base.Settings.Load(ctx, PVPComboScope(characterID))
	if err != nil || exists {
		return err
	}
	pages, found, err := darkcombo.LoadPages(ctx, base.Settings, characterID)
	if err != nil || !found {
		return err
	}
	return darkcombo.SavePages(ctx, pvpComboSettings{base.Settings, characterID}, characterID, pages, now)
}
