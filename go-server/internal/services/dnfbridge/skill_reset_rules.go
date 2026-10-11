package dnfbridge

import (
	"context"
	"strconv"

	repo "longheng.io/server/internal/modules/dnf/repository"
	skill "longheng.io/server/internal/modules/dnf/skill"
)

// Only op491 uses the full profession baseline. Buy/slot rules keep their
// existing path; reset must also reconstruct class and awakening free grants.
func (s *Service) skillResetOwnerRules(ctx context.Context, repos repo.Group, characterID uint16) (*skill.Table, map[uint16]int, *repo.SkillPointState) {
	catalog, _, points := s.skillOwnerRules(ctx, repos, characterID)
	if catalog == nil || points == nil {
		return nil, nil, nil
	}
	character, ok, err := repos.Character.Load(ctx, strconv.Itoa(int(characterID)))
	if err != nil || !ok {
		return nil, nil, nil
	}
	job, ok := characterJobByte(character)
	if !ok {
		return nil, nil, nil
	}
	packed, ok := repo.CharacterGrowTypeInDomain(character)
	if !ok {
		return nil, nil, nil
	}
	profiles, professionCatalog, err := s.currentProfessionResources(ctx)
	if err != nil {
		s.logInfo("dnfbridge skill reset profession unavailable", "character_id", characterID, "error", err)
		return nil, nil, nil
	}
	grants, err := profiles.ResetGrants(professionCatalog, job, byte(packed), character.Level)
	if err != nil {
		s.logInfo("dnfbridge skill reset grants unavailable", "character_id", characterID, "error", err)
		return nil, nil, nil
	}
	levels := make(map[uint16]int, len(grants))
	for _, grant := range grants {
		levels[grant.SkillID] = grant.Level
	}
	return professionCatalog, levels, points
}
