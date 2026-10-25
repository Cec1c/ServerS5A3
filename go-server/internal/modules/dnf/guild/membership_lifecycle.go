package guild

import (
	"errors"
	"time"

	"longheng.io/server/internal/modules/dnf/itemexpiration"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

var (
	ErrDepartureCooldown = errors.New("guild departure restriction lasts until the next service day")
	ErrDisbandHasMembers = errors.New("guild master must remove other members before disbanding")
)

// Departure is character-owned, so another character on the same account can
// still join. The fixed service-day deadline survives reconnects and restarts.
type Departure struct {
	Owner dnfrepo.GuildOwner `json:"owner"`
	At    time.Time          `json:"at"`
	Until time.Time          `json:"until"`
}

func (r *Registry) checkDeparture(owner dnfrepo.GuildOwner, now time.Time) error {
	if prior, ok := r.Departures[owner.CharacterID]; ok && prior.Owner == owner && now.Before(prior.Until) {
		return ErrDepartureCooldown
	}
	return nil
}

func (r *Registry) recordDeparture(owner dnfrepo.GuildOwner, now time.Time) {
	if r.Departures == nil {
		r.Departures = make(map[string]Departure)
	}
	r.Departures[owner.CharacterID] = Departure{Owner: owner, At: now.UTC(), Until: itemexpiration.NextDailyDeleteAt(now)}
}

func validateDepartures(reg Registry) error {
	for id, d := range reg.Departures {
		if id == "" || d.Owner.CharacterID != id || d.Owner.AccountID == "" || d.At.IsZero() || !d.Until.Equal(itemexpiration.NextDailyDeleteAt(d.At)) {
			return ErrInvalidRegistry
		}
	}
	return nil
}

// DepartMember completes the removal stage inside the caller's Guild UoW.
// The matched ordinary-server confirmation applies the 06:00 restriction to
// a voluntary exit; forced removal does not create that voluntary restriction.
func (r *Registry) DepartMember(actor, target dnfrepo.GuildOwner, now time.Time) (MemberRemoval, error) {
	if now.IsZero() {
		return MemberRemoval{}, ErrInvalidRegistry
	}
	g, member, found := r.GuildForOwner(actor)
	if !found {
		return MemberRemoval{}, ErrNotMember
	}
	if actor == target && (member.Grade == DefaultMasterGrade || member.CharacterID == g.MasterCharacterID) {
		return r.Disband(actor, now)
	}
	removed, err := r.RemoveOrdinaryMember(actor, target, now)
	if err != nil {
		return MemberRemoval{}, err
	}
	if removed.Voluntary {
		r.recordDeparture(target, now)
	}
	r.removeApplicationsForCharacter(target.CharacterID, now)
	for _, guild := range r.Guilds {
		delete(guild.Invitations, target.CharacterID)
	}
	return removed, nil
}

func (r *Registry) Disband(owner dnfrepo.GuildOwner, now time.Time) (MemberRemoval, error) {
	if now.IsZero() {
		return MemberRemoval{}, ErrInvalidRegistry
	}
	g, member, found := r.GuildForOwner(owner)
	if !found {
		return MemberRemoval{}, ErrNotMember
	}
	if member.Grade != DefaultMasterGrade || member.CharacterID != g.MasterCharacterID {
		return MemberRemoval{}, ErrPermissionDenied
	}
	if len(g.Members) != 1 {
		return MemberRemoval{}, ErrDisbandHasMembers
	}
	removed := MemberRemoval{GuildID: g.ID, GuildName: g.Name, Member: member, Voluntary: true}
	delete(r.Guilds, g.ID)
	r.recordDeparture(owner, now)
	r.removeApplicationsForCharacter(owner.CharacterID, now)
	for _, guild := range r.Guilds {
		delete(guild.Invitations, owner.CharacterID)
	}
	return removed, nil
}
