package guild

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"

	r "longheng.io/server/internal/modules/dnf/repository"
)

var (
	ErrGuardianNotOwned    = errors.New("guild guardian is not owned")
	ErrGuardianEssence     = errors.New("insufficient guild guardian essence")
	ErrGuardianInteraction = errors.New("guild guardian interaction is unavailable")
)

const (
	GuardianSelect   uint32 = 7
	GuardianPurchase uint32 = 8
)

// All acquisition defaults are operator policy, independent of PVF prices.
// FirstSelect is a pointer so explicit false differs from an omitted field.
type GuardianPolicy struct {
	FirstPurchase GuardianFirstPurchasePolicy `json:"first_purchase"`
	InitialLevel  uint8                       `json:"initial_level"`
	InitialSkills []uint8                     `json:"initial_skills"`
	FirstSelect   *bool                       `json:"first_select"`
}

func (p GuardianPolicy) Validate() error {
	if (p.FirstPurchase != GuardianFreeOnce && p.FirstPurchase != GuardianPaid) || p.InitialLevel < 1 || p.InitialLevel > 50 || len(p.InitialSkills) != 5 || p.FirstSelect == nil {
		return ErrGuardianPolicy
	}
	for _, level := range p.InitialSkills {
		if level > 5 {
			return ErrGuardianPolicy
		}
	}
	return nil
}

func ParseGuardianPolicy(text string) (GuardianPolicy, error) {
	var p GuardianPolicy
	d := json.NewDecoder(strings.NewReader(text))
	d.DisallowUnknownFields()
	if err := d.Decode(&p); err != nil {
		return GuardianPolicy{}, ErrGuardianPolicy
	}
	var extra any
	if d.Decode(&extra) != io.EOF || p.Validate() != nil {
		return GuardianPolicy{}, ErrGuardianPolicy
	}
	return p, nil
}

type GuardianRequest struct {
	Owner              r.GuildOwner
	GuildID            uint32
	RequestID          string
	Action, GuardianID uint32
	Observed, Value    uint32
	Growth             *GuardianGrowthCatalog
	Policy             GuardianPolicy
	Revalidate         func() bool
	// Called inside the registry/character UOW. Replays only recover a receipt;
	// their scene check must not depend on NPCs replaced by the original grant.
	CanInteract func(r.CharacterRecord, *Guild, bool) bool
}

type GuardianReceipt struct {
	Observed      uint32                      `json:"observed,omitempty"`
	Value         uint32                      `json:"value,omitempty"`
	Owner         r.GuildOwner                `json:"owner"`
	GuildID       uint32                      `json:"guild_id"`
	Action        uint32                      `json:"action"`
	Guardian      Guardian                    `json:"guardian"`
	Cost          EssenceBalance              `json:"cost"`
	BalanceAfter  EssenceBalance              `json:"balance_after"`
	ActiveID      uint32                      `json:"active_id"`
	FirstPurchase GuardianFirstPurchasePolicy `json:"first_purchase,omitempty"`
	At            time.Time                   `json:"at"`
}

func validateGuardianReceipts(reg Registry) error {
	for key, receipt := range reg.GuardianReceipts {
		if !strings.HasPrefix(key, "guardian:") || !validPurchaseID(strings.TrimPrefix(key, "guardian:")) || receipt.Owner.AccountID == "" || receipt.Owner.CharacterID == "" || receipt.GuildID == 0 || receipt.At.IsZero() || receipt.ActiveID > 2 {
			return ErrInvalidRegistry
		}
		state := &GuardianState{EverAcquired: true, Owned: []Guardian{receipt.Guardian}}
		if state.Validate() != nil {
			return ErrInvalidRegistry
		}
		switch receipt.Action {
		case GuardianPurchase:
			if receipt.Observed != 0 || receipt.Value != 0 {
				return ErrInvalidRegistry
			}
			if receipt.FirstPurchase != GuardianFreeOnce && receipt.FirstPurchase != GuardianPaid {
				return ErrInvalidRegistry
			}
		case GuardianSelect:
			if receipt.Observed != 0 || receipt.Value != 0 || receipt.FirstPurchase != "" || receipt.Cost != (EssenceBalance{}) || receipt.ActiveID != receipt.Guardian.ID {
				return ErrInvalidRegistry
			}
		case GuardianInvestAP, GuardianInvestGP:
			if receipt.FirstPurchase != "" || receipt.Value == 0 || receipt.Value > 0x7fffffff {
				return ErrInvalidRegistry
			}
			want := EssenceBalance{AP: receipt.Value}
			if receipt.Action == GuardianInvestGP {
				want = EssenceBalance{GP: receipt.Value}
			}
			if receipt.Cost != want {
				return ErrInvalidRegistry
			}
		case 2, 3, 4, 5, 6:
			if receipt.FirstPurchase != "" || receipt.Observed < 1 || receipt.Observed >= 5 || receipt.Value != receipt.Observed+1 || uint32(receipt.Guardian.Skills[receipt.Action-2]) != receipt.Value || receipt.Cost.AP == 0 {
				return ErrInvalidRegistry
			}
		default:
			return ErrInvalidRegistry
		}
	}
	return nil
}

// ApplyGuardian commits essence, ownership/selection and a permanent receipt
// together. Persistent master identity AND grade are required; upgrade bit16
// is not treated as acquisition permission. Caller reserves session identity.
func ApplyGuardian(ctx context.Context, uow r.GuildUnitOfWork, q GuardianRequest, catalog *GuardianCatalog, now time.Time) (GuardianReceipt, error) {
	var result GuardianReceipt
	if uow == nil || q.Revalidate == nil || q.CanInteract == nil {
		return result, ErrRepositoryUnavailable
	}
	if !validPurchaseID(q.RequestID) || q.GuildID == 0 {
		return result, ErrPurchaseIdentity
	}
	if q.Action > GuardianPurchase || q.GuardianID < 1 || q.GuardianID > 2 || now.IsZero() {
		return result, ErrGuardianInteraction
	}
	if q.Action >= GuardianSelect && (q.Observed != 0 || q.Value != 0) {
		return result, ErrGuardianInteraction
	}
	if err := q.Policy.Validate(); err != nil {
		return result, err
	}
	// Namespace the connection-wide identity, without including action/ID:
	// reusing one identity for different guardian commands is an error.
	key := "guardian:" + q.RequestID
	err := uow.WithinGuild(ctx, []r.GuildOwner{q.Owner}, func(tx r.Group) error {
		if !q.Revalidate() {
			return ErrCharacterNotFound
		}
		actor, found, err := tx.Character.Load(ctx, q.Owner.CharacterID)
		if err != nil {
			return err
		}
		if !found || actor.CharacterID != q.Owner.CharacterID || actor.AccountID != q.Owner.AccountID || actor.Stats["delete_flag"] != 0 {
			return ErrCharacterNotFound
		}
		reg, err := Load(ctx, tx.Settings)
		if err != nil {
			return err
		}
		g, member, found := reg.GuildForOwner(q.Owner)
		if !found || g.ID != q.GuildID {
			return ErrNotMember
		}
		if (q.Action <= GuardianSkillLast && GradePermissionsFor(g, member.Grade)&GuardianGrowthPermission == 0) || (q.Action >= GuardianSelect && (g.MasterCharacterID != q.Owner.CharacterID || member.Grade != DefaultMasterGrade)) {
			return ErrPermissionDenied
		}
		old, replay := reg.GuardianReceipts[key]
		if replay && (old.Owner != q.Owner || old.GuildID != q.GuildID || old.Action != q.Action || old.Guardian.ID != q.GuardianID || old.Observed != q.Observed || old.Value != q.Value) {
			return ErrPurchaseIdentity
		}
		if !q.CanInteract(actor, g, replay) {
			return ErrGuardianInteraction
		}
		if replay {
			if !q.Revalidate() {
				return ErrCharacterNotFound
			}
			result = old
			return nil
		}
		if g.Guardians == nil {
			g.Guardians = &GuardianState{}
		}
		state := g.Guardians
		var row Guardian
		for _, owned := range state.Owned {
			if owned.ID == q.GuardianID {
				row = owned
				break
			}
		}
		cost := EssenceBalance{}
		fee := GuardianFirstPurchasePolicy("")
		if q.Action == GuardianPurchase {
			cost, err = catalog.PurchaseCost(q.Policy.FirstPurchase, state, q.GuardianID)
			if err != nil {
				return err
			}
			if g.Essence.AP < cost.AP || g.Essence.GP < cost.GP {
				return ErrGuardianEssence
			}
			row = Guardian{ID: q.GuardianID, Level: q.Policy.InitialLevel}
			copy(row.Skills[:], q.Policy.InitialSkills)
			g.Essence.AP -= cost.AP
			g.Essence.GP -= cost.GP
			if !state.EverAcquired && *q.Policy.FirstSelect {
				state.ActiveID = row.ID
			}
			state.EverAcquired = true
			state.Owned = append(state.Owned, row)
			fee = q.Policy.FirstPurchase
		} else if q.Action <= GuardianSkillLast {
			row, cost, err = q.Growth.growth(row, q.Action, q.Observed, q.Value)
			if err != nil {
				return err
			}
			if g.Essence.AP < cost.AP || g.Essence.GP < cost.GP {
				return ErrGuardianEssence
			}
			g.Essence.AP -= cost.AP
			g.Essence.GP -= cost.GP
			for i := range state.Owned {
				if state.Owned[i].ID == row.ID {
					state.Owned[i] = row
					break
				}
			}
		} else {
			if row.ID == 0 {
				return ErrGuardianNotOwned
			}
			state.ActiveID = row.ID
		}
		g.UpdatedAt = now
		result = GuardianReceipt{Observed: q.Observed, Value: q.Value, Owner: q.Owner, GuildID: g.ID, Action: q.Action, Guardian: row, Cost: cost, BalanceAfter: g.Essence, ActiveID: state.ActiveID, FirstPurchase: fee, At: now}
		if reg.GuardianReceipts == nil {
			reg.GuardianReceipts = make(map[string]GuardianReceipt)
		}
		reg.GuardianReceipts[key] = result
		if !q.Revalidate() {
			return ErrCharacterNotFound
		}
		if err := Save(ctx, tx.Settings, reg, now); err != nil {
			return err
		}
		if !q.Revalidate() {
			return ErrCharacterNotFound
		}
		return nil
	})
	if err != nil {
		return GuardianReceipt{}, err
	}
	return result, nil
}
