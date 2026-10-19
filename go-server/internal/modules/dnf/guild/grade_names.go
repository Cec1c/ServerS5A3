package guild

import (
	"time"
	"unicode/utf16"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// NOTI70 contains a 24-byte NUL-terminated name for each grade. The shared
// native input also enforces 20 UTF-16 units; neither bound can be truncated.
const GradeNameMaxBytes = 23
const GradeNameMaxUTF16 = 20

// SetGradeName runs inside the Guild UoW. Renaming the master role changes its
// label, not the master character's name or ownership. The native model has
// grades 0..5; zero is not a member role and the UI's fallback 6 is invalid.
func (r *Registry) SetGradeName(owner dnfrepo.GuildOwner, grade byte, name string, now time.Time) (*Guild, error) {
	g, member, found := r.GuildForOwner(owner)
	if !found {
		return nil, ErrNotMember
	}
	if member.Grade != DefaultMasterGrade || g.MasterCharacterID != owner.CharacterID {
		return nil, ErrPermissionDenied
	}
	if grade < 1 || grade > 5 || validateText(name, GradeNameMaxBytes, false) != nil || len(utf16.Encode([]rune(name))) > GradeNameMaxUTF16 {
		return nil, ErrInvalidText
	}
	if g.GradeNames[grade] == name {
		return g, nil
	}
	if g.GradeNames == nil {
		g.GradeNames = defaultGradeNames()
	}
	g.GradeNames[grade] = name
	g.UpdatedAt = now
	return g, nil
}
