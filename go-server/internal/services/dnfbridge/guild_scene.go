package dnfbridge

import (
	"context"
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
	_, registry, err := s.loadCurrentGuildRegistry(ctx)
	if err != nil {
		return currentSceneGuildProjection{}, err
	}
	if _, _, found := registry.GuildForCharacter(strconv.Itoa(int(characterID))); !found {
		return currentSceneGuildProjection{}, nil
	}
	character, found, err := repos.Character.Load(ctx, strconv.Itoa(int(characterID)))
	if err != nil {
		return currentSceneGuildProjection{}, err
	}
	if !found {
		return currentSceneGuildProjection{}, nil
	}
	g, _, found := registry.GuildForOwner(dnfrepo.GuildOwner{CharacterID: character.CharacterID, AccountID: character.AccountID})
	if !found {
		return currentSceneGuildProjection{}, nil
	}
	return currentSceneGuildProjection{id: g.ID, mark: uint32(g.Mark), name: g.Name}, nil
}
