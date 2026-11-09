package dnfbridge

import (
	"context"
	"longheng.io/server/internal/modules/dnf/guild"
	"strconv"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// This value is a wire projection, never a character stat or persisted copy.
type currentSceneGuildProjection struct {
	id   uint32
	mark uint32
	name string
}

func (s *Service) currentSceneGuild(ctx context.Context, characterID uint16) (currentSceneGuildProjection, error) {
	if characterID == 0 {
		return currentSceneGuildProjection{}, nil
	}
	repos, ok := s.repositoryGroup()
	if !ok || repos.Settings == nil {
		return currentSceneGuildProjection{}, nil
	}
	character, found, err := dnfrepo.LoadCharacterIdentity(ctx, repos.Character, strconv.Itoa(int(characterID)))
	if err != nil {
		return currentSceneGuildProjection{}, err
	}
	if !found {
		return currentSceneGuildProjection{}, nil
	}
	g, found, err := guild.ReadGuildEntitlement(ctx, repos.Settings, dnfrepo.GuildOwner{CharacterID: character.CharacterID, AccountID: character.AccountID})
	if err != nil {
		return currentSceneGuildProjection{}, err
	}
	if !found {
		return currentSceneGuildProjection{}, nil
	}
	return currentSceneGuildProjection{id: g.ID, mark: uint32(g.Mark), name: g.Name}, nil
}
