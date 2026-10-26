package guild

import (
	"context"
	"errors"
	"reflect"
	"time"

	r "longheng.io/server/internal/modules/dnf/repository"
)

var ErrTeleportUnavailable = errors.New("guild teleport unavailable")

type TeleportLocation struct {
	Town, Area, X, Y, Direction int64
}

func CharacterTeleportLocation(c r.CharacterRecord) TeleportLocation {
	return TeleportLocation{c.Stats["town_id"], c.Stats["area_id"], c.Stats["pos_x"], c.Stats["pos_y"], c.Stats["direction"]}
}

type TeleportParticipant struct {
	Owner r.GuildOwner
	From  TeleportLocation
}

type TeleportReceipt struct {
	GuildID      uint32                `json:"guild_id"`
	Initiator    r.GuildOwner          `json:"initiator"`
	Participants []TeleportParticipant `json:"participants"`
	Destination  TeleportLocation      `json:"destination"`
	At           time.Time             `json:"at"`
	JoinedAt     time.Time             `json:"joined_at"`
}

type TeleportQuery struct {
	RequestID    string
	GuildID      uint32
	Initiator    r.GuildOwner
	Participants []TeleportParticipant
	Destination  TeleportLocation
	MinimumLevel int
	JoinedAt     time.Time
	Revalidate   func() bool
}

func validTeleportLocation(l TeleportLocation) bool {
	return l.Town > 0 && l.Town <= 255 && l.Area >= 0 && l.Area <= 255 &&
		l.X >= 0 && l.X <= 32767 && l.Y >= 0 && l.Y <= 32767 && l.Direction >= 0 && l.Direction <= 255
}

func validateTeleports(reg Registry) error {
	for key, receipt := range reg.Teleports {
		_, cargo := reg.CargoPurchases[key]
		_, benefit := reg.BenefitPurchases[key]
		_, expansion := reg.MemberExpansions[key]
		_, contract := reg.ContractClaims[key]
		if cargo || benefit || expansion || contract {
			return ErrInvalidRegistry
		}
		if !validPurchaseID(key) || receipt.GuildID == 0 || receipt.At.IsZero() || receipt.JoinedAt.IsZero() || !validTeleportLocation(receipt.Destination) {
			return ErrInvalidRegistry
		}
		owners := make([]r.GuildOwner, 0, len(receipt.Participants))
		initiator := false
		for _, p := range receipt.Participants {
			owners = append(owners, p.Owner)
			initiator = initiator || p.Owner == receipt.Initiator
			if !validTeleportLocation(p.From) {
				return ErrInvalidRegistry
			}
		}
		if _, err := r.GuildTravelOwners(owners); err != nil || !initiator {
			return ErrInvalidRegistry
		}
	}
	return nil
}

// CommitTeleport atomically spends one shared use and updates every consenting
// participant. Retries never move anyone again or restore an old use balance.
func CommitTeleport(ctx context.Context, uow r.GuildUnitOfWork, q TeleportQuery, now time.Time) (TeleportReceipt, error) {
	travel, ok := uow.(r.GuildTravelUnitOfWork)
	if !ok || q.Revalidate == nil {
		return TeleportReceipt{}, ErrRepositoryUnavailable
	}
	if !validPurchaseID(q.RequestID) || q.GuildID == 0 || now.IsZero() || !validTeleportLocation(q.Destination) || q.MinimumLevel < 0 {
		return TeleportReceipt{}, ErrTeleportUnavailable
	}
	want := TeleportReceipt{GuildID: q.GuildID, Initiator: q.Initiator, Participants: append([]TeleportParticipant(nil), q.Participants...), Destination: q.Destination, At: now, JoinedAt: q.JoinedAt.UTC()}
	if err := validateTeleports(Registry{Teleports: map[string]TeleportReceipt{q.RequestID: want}}); err != nil {
		return TeleportReceipt{}, ErrTeleportUnavailable
	}
	owners := make([]r.GuildOwner, 0, len(q.Participants))
	for _, p := range q.Participants {
		owners = append(owners, p.Owner)
	}
	result := want
	err := travel.WithinGuildTravel(ctx, owners, func(tx r.Group) error {
		if !q.Revalidate() {
			return ErrTeleportUnavailable
		}
		reg, err := Load(ctx, tx.Settings)
		if err != nil {
			return err
		}
		g, membership, member := reg.GuildForOwner(q.Initiator)
		if !member || g.ID != q.GuildID || !membership.JoinedAt.Equal(q.JoinedAt) {
			return ErrNotMember
		}
		if old, exists := reg.Teleports[q.RequestID]; exists {
			want.At = old.At
			if !reflect.DeepEqual(want, old) {
				return ErrPurchaseIdentity
			}
			result = old
			return nil
		}
		benefit := g.Benefits[7]
		if benefit.Type != 7 || benefit.Value == 0 || !benefit.ExpiresAt.IsZero() {
			return ErrTeleportUnavailable
		}
		for _, p := range q.Participants {
			actor, found, err := tx.Character.Load(ctx, p.Owner.CharacterID)
			if err != nil {
				return err
			}
			if !found || actor.AccountID != p.Owner.AccountID || actor.CharacterID != p.Owner.CharacterID || actor.Level < q.MinimumLevel || CharacterTeleportLocation(actor) != p.From {
				return ErrTeleportUnavailable
			}
			actor = r.CloneCharacter(actor)
			actor.Stats["town_id"], actor.Stats["area_id"] = q.Destination.Town, q.Destination.Area
			actor.Stats["pos_x"], actor.Stats["pos_y"], actor.Stats["direction"] = q.Destination.X, q.Destination.Y, q.Destination.Direction
			actor.UpdatedAt = now
			if err := r.SaveCharacterFields(ctx, tx.Character, actor, r.CharacterFieldStats); err != nil {
				return err
			}
		}
		benefit.Value--
		g.Benefits[7], g.UpdatedAt = benefit, now
		if reg.Teleports == nil {
			reg.Teleports = make(map[string]TeleportReceipt)
		}
		reg.Teleports[q.RequestID] = result
		if !q.Revalidate() {
			return ErrTeleportUnavailable
		}
		if err := Save(ctx, tx.Settings, reg, now); err != nil {
			return err
		}
		if !q.Revalidate() {
			return ErrTeleportUnavailable
		}
		return nil
	})
	if err != nil {
		return TeleportReceipt{}, err
	}
	return result, nil
}
