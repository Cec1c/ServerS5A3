package guild

import (
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

const InvitePermission = uint32(1 << 1)

// CheckInvitation is an advisory check when offering consent and a required
// recheck within the Guild UoW when accepting it. The bridge owns the live
// invitation, both session generations and expiry; this function grants none.
func (r *Registry) CheckInvitation(guildID uint32, inviter dnfrepo.GuildOwner, target dnfrepo.CharacterRecord) (*Guild, error) {
	g, member, found := r.GuildForOwner(inviter)
	if !found || g.ID != guildID {
		return nil, ErrNotMember
	}
	if GradePermissionsFor(g, member.Grade)&InvitePermission == 0 {
		return nil, ErrPermissionDenied
	}
	if target.CharacterID == "" || target.AccountID == "" || target.Name == "" {
		return nil, ErrCharacterNotFound
	}
	if _, err := CharacterServerGroup(target); err != nil {
		return nil, err
	}
	if _, _, found := r.GuildForCharacter(target.CharacterID); found {
		return nil, ErrAlreadyMember
	}
	if len(g.Members) >= g.MemberLimit {
		return nil, ErrMemberLimit
	}
	return g, nil
}

// AcceptInvitation requires verified live consent and both owners locked by
// the caller. It deliberately does not model departure cooldowns or a fee:
// neither is established by the invitation wire contract alone.
func (r *Registry) AcceptInvitation(guildID uint32, inviter dnfrepo.GuildOwner, target dnfrepo.CharacterRecord, now time.Time) (*Guild, error) {
	g, err := r.CheckInvitation(guildID, inviter, target)
	if err != nil {
		return nil, err
	}
	if _, err = r.JoinDirect(g.ID, target, now); err != nil {
		return nil, err
	}
	return g, nil
}
