package dnfbridge

import (
	"context"
	"fmt"
	"sort"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func (s *Service) currentPVPPeerSkills(ctx context.Context, character dnfrepo.CharacterRecord) ([]byte, error) {
	repos, ok := s.repositoryGroup()
	if !ok || repos.Skill == nil {
		return nil, fmt.Errorf("PVP skill repository unavailable")
	}
	record, found, err := repos.Skill.Load(ctx, character.CharacterID)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("PVP character %s has no skill record", character.CharacterID)
	}
	active := byte(dnfrepo.ActiveSkillTree(record))
	return buildCurrentPVPPeerSkills(record, active)
}

func buildCurrentPVPPeerSkills(record dnfrepo.SkillRecord, active byte) ([]byte, error) {
	if active >= currentSkillInfoTreeCount {
		return nil, fmt.Errorf("invalid PVP skill tree %d", active)
	}
	if _, found := dnfrepo.SkillTree(record, int(active)); !found {
		return nil, fmt.Errorf("PVP active skill tree %d is missing", active)
	}
	var w packetWriter
	w.writeByte(active)
	for tree := 0; tree < currentSkillInfoTreeCount; tree++ {
		state, _ := dnfrepo.SkillTree(record, tree)
		ids := make([]int, 0, len(state.Skills))
		for id, skill := range state.Skills {
			if id < 0 || id >= 512 || skill.Level < 0 || skill.Level > 255 {
				return nil, fmt.Errorf("PVP skill outside native bounds: tree=%d id=%d level=%d", tree, id, skill.Level)
			}
			if skill.Level > 0 {
				ids = append(ids, int(id))
			}
		}
		if len(ids) > 255 {
			return nil, fmt.Errorf("PVP tree %d exceeds native skill count", tree)
		}
		sort.Ints(ids)
		w.writeByte(byte(len(ids)))
		for _, id := range ids {
			w.writeUint16(uint16(id))
			w.writeByte(byte(state.Skills[int64(id)].Level))
		}
	}
	return w.bytes(), nil
}
