package guild

import (
	"errors"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

var ErrMemberRemovalMaster = errors.New("guild master removal requires the separate lifecycle policy")

type MemberRemoval struct {
	GuildID   uint32
	GuildName string
	Member    Member
	Voluntary bool
}

// RemoveOrdinaryMember is the membership stage of the Guild UoW, not a complete
// leave operation. Its caller must apply the verified departure/cooldown policy
// in that same transaction and invalidate the removed member's live session
// after commit. Master disband/transfer is a separate lifecycle operation.
func (r *Registry) RemoveOrdinaryMember(actor, target dnfrepo.GuildOwner, now time.Time) (MemberRemoval, error) {
	g, acting, found := r.GuildForOwner(actor)
	if !found {
		return MemberRemoval{}, ErrNotMember
	}
	targetGuild, selected, found := r.GuildForOwner(target)
	if !found || targetGuild.ID != g.ID {
		return MemberRemoval{}, ErrNotMember
	}
	if selected.CharacterID == g.MasterCharacterID || selected.Grade == DefaultMasterGrade {
		return MemberRemoval{}, ErrMemberRemovalMaster
	}
	if acting.Grade < 1 || acting.Grade > 4 || selected.Grade < 2 || selected.Grade > 4 {
		return MemberRemoval{}, ErrInvalidRegistry
	}
	voluntary := actor == target
	if !voluntary {
		// Native ordinary-member menu: bit10 and strict rank precedence.
		// Table index5 is the separate commander role, not an admitted grade.
		precedence := [...]byte{0, 5, 4, 2, 1}
		if GradePermissionsFor(g, acting.Grade)&(1<<10) == 0 || precedence[acting.Grade] <= precedence[selected.Grade] {
			return MemberRemoval{}, ErrPermissionDenied
		}
		if acting.Grade == DefaultMasterGrade && acting.CharacterID != g.MasterCharacterID {
			return MemberRemoval{}, ErrInvalidRegistry
		}
	}
	result := MemberRemoval{GuildID: g.ID, GuildName: g.Name, Member: selected, Voluntary: voluntary}
	delete(g.Members, selected.CharacterID)
	g.UpdatedAt = now
	return result, nil
}
