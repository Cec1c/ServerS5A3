package guild

import (
	"context"
	"math"
	"time"

	r "longheng.io/server/internal/modules/dnf/repository"
)

type CargoQuery struct {
	Owner      r.GuildOwner
	Revalidate func() bool
}

type CargoView struct {
	GuildID uint32
	Cargo   *Cargo
}

// ReadCargo obtains membership, permissions and warehouse contents from one
// serialized registry view. It does not initialize, save or migrate anything.
func ReadCargo(ctx context.Context, uow r.GuildUnitOfWork, query CargoQuery) (CargoView, error) {
	return readCargo(ctx, uow, query, nil)
}

// CargoInitialization is an explicit runtime policy. A missing legacy cargo
// record is not evidence of any initial size; only OpenCargo may initialize it.
type CargoInitialization struct {
	InitialCapacity uint32
	MaxCapacity     uint32
	Now             time.Time
}

// OpenCargo initializes a missing warehouse once, in the same transaction that
// checks ownership and access. Existing slots, capacity and history are retained.
func OpenCargo(ctx context.Context, uow r.GuildUnitOfWork, query CargoQuery, policy CargoInitialization) (CargoView, error) {
	if policy.InitialCapacity == 0 || policy.InitialCapacity > policy.MaxCapacity || policy.MaxCapacity > math.MaxUint16+1 || policy.Now.IsZero() {
		return CargoView{}, ErrInvalidRegistry
	}
	return readCargo(ctx, uow, query, &policy)
}

func readCargo(ctx context.Context, uow r.GuildUnitOfWork, query CargoQuery, policy *CargoInitialization) (CargoView, error) {
	if uow == nil || query.Revalidate == nil {
		return CargoView{}, ErrRepositoryUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var result CargoView
	err := uow.WithinGuild(ctx, []r.GuildOwner{query.Owner}, func(tx r.Group) error {
		if !query.Revalidate() {
			return ErrPermissionDenied
		}
		actor, found, err := tx.Character.Load(ctx, query.Owner.CharacterID)
		if err != nil {
			return err
		}
		if !found || actor.CharacterID != query.Owner.CharacterID || actor.AccountID != query.Owner.AccountID {
			return ErrPermissionDenied
		}
		registry, err := Load(ctx, tx.Settings)
		if err != nil {
			return err
		}
		g, member, found := registry.GuildForCharacter(actor.CharacterID)
		if !found || member.AccountID != actor.AccountID {
			return ErrNotMember
		}
		if GradePermissionsFor(g, member.Grade)&CargoPermission == 0 {
			return ErrPermissionDenied
		}
		initialize := g.Cargo == nil && policy != nil
		if initialize {
			g.Cargo = &Cargo{Capacity: policy.InitialCapacity}
		}
		if g.Cargo == nil {
			return ErrCargoUninitialized
		}
		if policy != nil && g.Cargo.Capacity > policy.MaxCapacity {
			return ErrInvalidRegistry
		}
		if !query.Revalidate() {
			return ErrPermissionDenied
		}
		if initialize {
			g.UpdatedAt = policy.Now
			if err := Save(ctx, tx.Settings, registry, policy.Now); err != nil {
				return err
			}
			if !query.Revalidate() {
				return ErrPermissionDenied
			}
		}
		// Load unmarshals a fresh registry; no repository-owned maps escape.
		result = CargoView{GuildID: g.ID, Cargo: g.Cargo}
		return nil
	})
	if err != nil {
		return CargoView{}, err
	}
	return result, nil
}
