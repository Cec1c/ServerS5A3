package guild

import (
	"errors"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

var ErrInvalidRecommendedChannel = errors.New("guild recommended channel selection is invalid")

// SetRecommendedChannel runs inside the Guild UoW. The bridge owns live
// directory eligibility; only the current guild master may set its channel.
func (r *Registry) SetRecommendedChannel(owner dnfrepo.GuildOwner, region, channel uint32, now time.Time) (*Guild, error) {
	g, member, found := r.GuildForOwner(owner)
	if !found {
		return nil, ErrNotMember
	}
	if member.Grade != DefaultMasterGrade || g.MasterCharacterID != owner.CharacterID {
		return nil, ErrPermissionDenied
	}
	if region > 249 || channel == 0 {
		return nil, ErrInvalidRecommendedChannel
	}
	if g.RecommendedRegion == region && g.RecommendedChannel == channel {
		return g, nil
	}
	g.RecommendedRegion = region
	g.RecommendedChannel = channel
	g.UpdatedAt = now
	return g, nil
}
