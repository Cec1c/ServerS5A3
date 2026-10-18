package guild

import (
	"errors"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

var (
	ErrMemberGradeSelf    = errors.New("cannot change own guild grade")
	ErrMemberGradeMaster  = errors.New("cannot change guild master's grade")
	ErrInvalidMemberGrade = errors.New("invalid ordinary guild member grade")
)

// ChangeMemberGrade runs inside the Guild UoW with both owners locked. Native
// ordinary rank actions use permission bit 11 and grades 2..4. Master transfer
// and commander selection have separate actions and are not handled here.
func (r *Registry) ChangeMemberGrade(actor, target dnfrepo.GuildOwner, grade byte, now time.Time) (*Guild, error) {
	g, member, found := r.GuildForOwner(actor)
	if !found {
		return nil, ErrNotMember
	}
	if GradePermissionsFor(g, member.Grade)&(1<<11) == 0 {
		return nil, ErrPermissionDenied
	}
	targetGuild, selected, found := r.GuildForOwner(target)
	if !found || targetGuild.ID != g.ID {
		return nil, ErrNotMember
	}
	if actor.CharacterID == target.CharacterID {
		return nil, ErrMemberGradeSelf
	}
	if target.CharacterID == g.MasterCharacterID || selected.Grade == DefaultMasterGrade {
		return nil, ErrMemberGradeMaster
	}
	if grade < 2 || grade > 4 {
		return nil, ErrInvalidMemberGrade
	}
	if selected.Grade == grade {
		return g, nil
	}
	selected.Grade = grade
	g.Members[target.CharacterID] = selected
	g.UpdatedAt = now
	return g, nil
}
