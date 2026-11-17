package dnfbridge

import (
	"context"
	"fmt"
	"os"

	"longheng.io/server/internal/modules/dnf/buffswap"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

func (s *Service) currentTagTournamentBuffGroupsEnabled() bool {
	return s != nil && s.options.currentClientVersion == "official-24.9" && buffGroupSyncEnabled() &&
		os.Getenv("DNFBRIDGE_TAG_BUFF_GROUP_SYNC") == "on"
}

// Preload this local reader only. nativeBuffSwapUserInfoRows then reuses the
// detached group projection, including the ordinary full-item/pet serializer.
// The shared userinfo loader and the persisted compatibility view stay intact.
func (s *Service) currentTagTournamentBuffProjection(ctx context.Context, p *gameSession, row currentTagTournamentCharacterProjection, group *byte) (*csharpLegacyUserInfoReader, buffswap.OwnedProjection, error) {
	r := &csharpLegacyUserInfoReader{ctx: ctx, service: s, session: p, characterID: row.character.CharacterID}
	if group == nil {
		source, err := r.loadNativeBuffOwnedProjection()
		return r, source, err
	}
	// Preserve the old authoritative-view identity migration before taking
	// read-only group projections. Never persist a cropped group State: that
	// would replace the other group's registration. Group zero is always first.
	if *group == 0 {
		if _, err := r.loadNativeBuffOwnedProjection(); err != nil {
			return nil, buffswap.OwnedProjection{}, err
		}
	}
	g, ok := s.repositoryGroup()
	if !ok || p == nil {
		return nil, buffswap.OwnedProjection{}, fmt.Errorf("帝国竞技场Buff换装仓储或会话不可用")
	}
	source, err := buffswap.LoadResolvedOwnedWithOptions(ctx, g, row.character.CharacterID, s.accountIDForSession(p), buffswap.OwnedProjectionOptions{
		Group:                   group,
		MigrateCurrentInstances: true,
		NeedsEquippedPetMetadata: func(item repo.EquipmentEntry) bool {
			return item.SlotIndex == 26 && currentEquippedCreatureFromEquipment(item).valid() && currentEquippedCreatureEnchantCardID(item) == 0
		},
	})
	if err != nil {
		return nil, buffswap.OwnedProjection{}, err
	}
	r.buffOwnedLoaded, r.buffOwned = true, source
	return r, source, nil
}
