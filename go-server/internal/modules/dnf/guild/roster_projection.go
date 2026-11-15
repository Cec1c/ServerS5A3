package guild

import r "longheng.io/server/internal/modules/dnf/repository"

// RosterProjection returns detached membership, management and benefit fields
// from this operation's immutable snapshot. It deliberately excludes storage,
// applications, progression and receipt history; it must never be saved or used
// as a mutation input. Display retains the complete detached Guild contract.
func (membership *GuildReadMembership) RosterProjection(owner r.GuildOwner) (*Guild, error) {
	if membership == nil || membership.characterID != owner.CharacterID {
		return nil, nil
	}
	g := membership.snapshot.owned(owner)
	if g == nil {
		return nil, nil
	}
	return detachedRosterProjection(g), nil
}

func detachedRosterProjection(g *Guild) *Guild {
	result := &Guild{
		ID: g.ID, Name: g.Name, MasterCharacterID: g.MasterCharacterID, MasterName: g.MasterName,
		Level: g.Level, Experience: g.Experience, Mileage: g.Mileage,
		Notice: g.Notice, Promotion: g.Promotion, Mark: g.Mark,
		AuraMark: g.AuraMark, AuraFrame: g.AuraFrame,
		RecommendedRegion: g.RecommendedRegion, RecommendedChannel: g.RecommendedChannel,
		WebPromotion: g.WebPromotion, JoinMode: g.JoinMode, MemberLimit: g.MemberLimit,
		CreatedAt: g.CreatedAt, UpdatedAt: g.UpdatedAt,
		Tags:             append([]byte(nil), g.Tags...),
		Benefits:         make(map[byte]Benefit, len(g.Benefits)),
		GradeNames:       make(map[byte]string, len(g.GradeNames)),
		GradePermissions: make(map[byte]uint32, len(g.GradePermissions)),
		Members:          make(map[string]Member, len(g.Members)),
	}
	for key, value := range g.Benefits {
		result.Benefits[key] = value
	}
	for key, value := range g.GradeNames {
		result.GradeNames[key] = value
	}
	for key, value := range g.GradePermissions {
		result.GradePermissions[key] = value
	}
	for key, member := range g.Members {
		if member.RankingActivity != nil {
			value := *member.RankingActivity
			member.RankingActivity = &value
		}
		if member.RankingPresence != nil {
			value := *member.RankingPresence
			member.RankingPresence = &value
		}
		if member.DungeonContributionPoints != nil {
			value := *member.DungeonContributionPoints
			member.DungeonContributionPoints = &value
		}
		result.Members[key] = member
	}
	return result
}
