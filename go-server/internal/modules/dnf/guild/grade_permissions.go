package guild

import (
	"errors"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

var ErrInvalidGradePermissions = errors.New("guild grade permission selection is invalid")

// SetGradePermissions runs inside the Guild UoW. Native op748 edits a role's
// mask, not a member's rank. Only the current master can edit roles 2..5;
// granting a member all ordinary permission bits does not grant this power.
func (r *Registry) SetGradePermissions(owner dnfrepo.GuildOwner, grade byte, mask uint32, now time.Time) (*Guild, error) {
	g, member, found := r.GuildForOwner(owner)
	if !found {
		return nil, ErrNotMember
	}
	if member.Grade != DefaultMasterGrade || g.MasterCharacterID != owner.CharacterID {
		return nil, ErrPermissionDenied
	}
	if grade < 2 || grade > 5 || mask & ^GradePermissionMask != 0 {
		return nil, ErrInvalidGradePermissions
	}
	if GradePermissionsFor(g, grade) == mask {
		return g, nil
	}
	if g.GradePermissions == nil {
		g.GradePermissions = make(map[byte]uint32)
	}
	g.GradePermissions[grade] = mask
	g.UpdatedAt = now
	return g, nil
}
