package guild

import (
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// ObserveMemberOnline records a server-observed lifecycle time inside the Guild
// UoW. Delayed observations must not move time backwards or affect a membership
// created after the observation. Online status itself remains a live projection.
func (r *Registry) ObserveMemberOnline(owner dnfrepo.GuildOwner, at time.Time) bool {
	g, member, found := r.GuildForOwner(owner)
	if !found || at.IsZero() || at.Before(member.JoinedAt) || !at.After(member.LastOnlineAt) {
		return false
	}
	member.LastOnlineAt = at
	member.observeRankingPresence(at)
	g.Members[owner.CharacterID] = member
	return true
}
