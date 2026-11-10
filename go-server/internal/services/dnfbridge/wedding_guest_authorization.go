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
	_, registry, err := s.loadCurrentGuildRegistry(ctx)
	if err != nil {
		return false
	}
	a, am, foundA := registry.GuildForCharacter(source.CharacterID)
	b, bm, foundB := registry.GuildForCharacter(targetID)
	if !foundA || !foundB || a.ID == 0 || a.ID != b.ID || am.AccountID != source.AccountID {
		return false
	}
	target, found, err := repos.Character.Load(ctx, targetID)
	return err == nil && found && target.AccountID == bm.AccountID
}
