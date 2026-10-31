package dnfbridge

import (
	"context"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func characterSelectionIdentities(ctx context.Context, repo dnfrepo.CharacterRepository, account string) ([]dnfrepo.CharacterRecord, error) {
	if narrow, ok := repo.(dnfrepo.CharacterSelectionIdentityReader); ok {
		return narrow.ListCharacterIdentities(ctx, account, defaultCharacterSlots)
	}
	// Limited repositories retain compatibility without roster equipment,
	// initialization or quest projections for unselected characters.
	return repo.ListByAccount(ctx, account, defaultCharacterSlots)
}

func loadSelectedCharacterIdentity(ctx context.Context, repo dnfrepo.CharacterRepository, identity dnfrepo.CharacterRecord, account string) (dnfrepo.CharacterRecord, bool) {
	if identity.AccountID != account || numericCharacterID(identity) <= 0 || numericCharacterID(identity) > 65535 {
		return dnfrepo.CharacterRecord{}, false
	}
	full, found, err := repo.Load(ctx, identity.CharacterID)
	if err != nil || !found || full.CharacterID != identity.CharacterID || full.AccountID != account ||
		full.Slot != identity.Slot || full.Stats["delete_flag"] != 0 {
		return dnfrepo.CharacterRecord{}, false
	}
	return full, true
}
