package dnfbridge

import (
	"context"

	"longheng.io/server/internal/modules/dnf/characterfriend"
	r "longheng.io/server/internal/modules/dnf/repository"
)

// The current native named-invite UI has character-friend and guild lists.
// Account friends and arbitrary numeric character IDs do not grant membership.
func (s *Service) currentWeddingNamedGuestAllowed(ctx context.Context, source r.CharacterRecord, targetID string) bool {
	repos, available := s.repositoryGroup()
	if !available || repos.Character == nil || repos.Account == nil {
		return false
	}
	rows, err := characterfriend.NewOwner(repos).List(ctx, source.AccountID, source.CharacterID)
	if err == nil {
		for _, row := range rows {
			if row.CharacterID == targetID {
				return true
			}
		}
	}
	_, registry, err := s.readCurrentGuildDirectory(ctx)
	if err != nil {
		return false
	}
	am, foundA := registry.Member(source.CharacterID)
	bm, foundB := registry.Member(targetID)
	if !foundA || !foundB || am.GuildID == 0 || am.GuildID != bm.GuildID || am.AccountID != source.AccountID {
		return false
	}
	target, found, err := repos.Character.Load(ctx, targetID)
	return err == nil && found && target.AccountID == bm.AccountID
}
