package guild

import (
	"context"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

var ErrCreationReceipt = errors.New("guild creation receipt does not match request")

type CreationReceipt struct {
	Solo      bool               `json:"solo,omitempty"`
	Creator   dnfrepo.GuildOwner `json:"creator"`
	Founder   dnfrepo.GuildOwner `json:"founder"`
	Name      string             `json:"name"`
	Promotion string             `json:"promotion"`
	GuildID   uint32             `json:"guild_id"`
}

type CreateRequest struct {
	OperationID string
	Creator     dnfrepo.GuildOwner
	Founder     dnfrepo.GuildOwner
	Name        string
	Promotion   string
	// Revalidate checks the live session generations after acquiring the DB
	// transaction. It must not perform repository writes or network I/O.
	Revalidate func() bool
}

type CreateResult struct {
	Guild     *Guild
	GoldAfter int64
	Replay    bool
}

// Update owns the durable registry transaction shared by every membership and
// settings mutation. Concurrent services therefore share the same SQL lock.
func Update(ctx context.Context, uow dnfrepo.GuildUnitOfWork, owners []dnfrepo.GuildOwner, now time.Time, apply func(dnfrepo.Group, *Registry) error) error {
	if uow == nil || apply == nil {
		return ErrRepositoryUnavailable
	}
	return uow.WithinGuild(ctx, owners, func(tx dnfrepo.Group) error {
		r, err := Load(ctx, tx.Settings)
		if err != nil {
			return err
		}
		if err = apply(tx, &r); err != nil {
			return err
		}
		return Save(ctx, tx.Settings, r, now)
	})
}

// CreateWithFoundingMember commits the creator's gold, both memberships and a
// durable retry receipt together. Session authentication/consent is checked by
// the bridge; the domain rechecks ownership, uniqueness and balances here.
func CreateWithFoundingMember(ctx context.Context, uow dnfrepo.GuildUnitOfWork, request CreateRequest, now time.Time) (CreateResult, error) {
	return createGuild(ctx, uow, request, now, false)
}

// CreateSolo is an explicit server policy, not consent from a fabricated member.
// The same transaction and receipt protect the single creator's gold and guild.
func CreateSolo(ctx context.Context, uow dnfrepo.GuildUnitOfWork, request CreateRequest, now time.Time) (CreateResult, error) {
	return createGuild(ctx, uow, request, now, true)
}

func createGuild(ctx context.Context, uow dnfrepo.GuildUnitOfWork, request CreateRequest, now time.Time, solo bool) (CreateResult, error) {
	var result CreateResult
	if _, err := hex.DecodeString(request.OperationID); err != nil {
		return result, ErrCreationReceipt
	}
	if len(request.OperationID) != 32 || strings.ContainsAny(request.OperationID, "\r\n\x00") || request.Creator.CharacterID == request.Founder.CharacterID {
		return result, ErrCreationReceipt
	}
	request.Name = strings.TrimSpace(request.Name)
	request.Promotion = strings.TrimSpace(request.Promotion)
	if err := ValidateName(request.Name); err != nil {
		return result, err
	}
	if err := ValidatePromotion(request.Promotion); err != nil {
		return result, err
	}
	owners := []dnfrepo.GuildOwner{request.Creator, request.Founder}
	if solo {
		if request.Founder != (dnfrepo.GuildOwner{}) {
			return result, ErrCreationReceipt
		}
		owners = owners[:1]
	}
	want := CreationReceipt{Solo: solo, Creator: request.Creator, Founder: request.Founder, Name: request.Name, Promotion: request.Promotion}
	err := Update(ctx, uow, owners, now, func(tx dnfrepo.Group, r *Registry) error {
		if request.Revalidate != nil && !request.Revalidate() {
			return ErrCharacterNotFound
		}
		creator, found, err := tx.Character.Load(ctx, request.Creator.CharacterID)
		if err != nil {
			return err
		}
		if !found {
			return ErrCharacterNotFound
		}
		var founder dnfrepo.CharacterRecord
		if !solo {
			founder, found, err = tx.Character.Load(ctx, request.Founder.CharacterID)
			if err != nil {
				return err
			}
			if !found {
				return ErrCharacterNotFound
			}
		}
		if old, ok := r.Creations[request.OperationID]; ok {
			comparison := old
			comparison.GuildID = 0
			if comparison != want {
				return ErrCreationReceipt
			}
			g, ok := r.GuildByID(old.GuildID)
			if !ok {
				return ErrCreationReceipt
			}
			if g.MasterCharacterID != creator.CharacterID {
				return ErrCreationReceipt
			}
			if _, ok := g.Members[founder.CharacterID]; !solo && !ok {
				return ErrCreationReceipt
			}
			result = CreateResult{Guild: g, GoldAfter: creator.Stats["gold"], Replay: true}
			return nil
		}
		if creator.Stats["gold"] < DefaultCreateGold {
			return ErrInsufficientGold
		}
		g, err := r.Create(creator, request.Name, now)
		if err != nil {
			return err
		}
		g.Promotion = request.Promotion
		if !solo {
			if _, err = r.JoinDirect(g.ID, founder, now); err != nil {
				return err
			}
		}
		creator = dnfrepo.CloneCharacter(creator)
		creator.Stats["gold"] -= DefaultCreateGold
		creator.UpdatedAt = now
		if err = dnfrepo.SaveCharacterFields(ctx, tx.Character, creator, dnfrepo.CharacterFieldStats); err != nil {
			return err
		}
		if r.Creations == nil {
			r.Creations = map[string]CreationReceipt{}
		}
		want.GuildID = g.ID
		r.Creations[request.OperationID] = want
		result = CreateResult{Guild: g, GoldAfter: creator.Stats["gold"]}
		return nil
	})
	if err != nil {
		return CreateResult{}, err
	}
	return result, nil
}
