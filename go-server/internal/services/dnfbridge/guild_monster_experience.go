package dnfbridge

import (
	"context"
	"strconv"

	"longheng.io/server/internal/modules/dnf/guild"
	r "longheng.io/server/internal/modules/dnf/repository"
)

// Resolve the entitlement for the accepted death. Progression subsequently
// revalidates the same character/account; no clear or quest consumer calls this.
func (s *Service) currentGuildMonsterExperienceBonus(ctx context.Context, session *gameSession, base uint32) (uint32, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	repos, ok := s.repositoryGroup()
	if !ok || repos.Settings == nil || session == nil || base == 0 {
		return 0, nil
	}
	reg, err := guild.Load(ctx, repos.Settings)
	if err != nil {
		return 0, err
	}
	g, _, found := reg.GuildForOwner(r.GuildOwner{AccountID: s.accountIDForSession(session), CharacterID: strconv.Itoa(int(session.selectedCharacterID))})
	if !found {
		return 0, nil
	}
	return guild.MonsterExperienceBonus(g, base, s.gameplayNow()), nil
}
