package wedding

import (
	"context"
	r "longheng.io/server/internal/modules/dnf/repository"
	"time"
)

// CeremonyDelivery grants through the transaction-scoped repositories. A full
// ordinary bag can fall back to a mailbox without splitting the marriage commit.
type CeremonyDelivery func(context.Context, r.Group, *r.InventoryRecord, Gift, int64) error

func (o Owner) FinishAdmittedCeremonyDelivered(ctx context.Context, req PairRequest, at int64, deliver CeremonyDelivery) (PairResult, error) {
	u, ok := o.Repositories.CharacterTrade.(r.WeddingLifecycleUnitOfWork)
	if !ok || deliver == nil {
		return PairResult{}, ErrUnavailable
	}
	ids := []string{req.Actor.CharacterID, req.Partner.CharacterID}
	scope, e := r.WeddingPairScope(ids)
	if e != nil {
		return PairResult{}, e
	}
	var result PairResult
	e = u.WithinWeddingLifecycle(ctx, ids, scope, func(g r.Group) error {
		if g.Character == nil || g.Inventory == nil || g.Settings == nil || g.Mailbox == nil {
			return ErrUnavailable
		}
		inner := o
		inner.Repositories = g
		inner.Repositories.CharacterTrade = pairInTransaction{ids: [2]string{ids[0], ids[1]}, chars: g.Character, inventory: g.Inventory}
		var e error
		result, e = inner.FinishAdmittedCeremony(ctx, req, at, func(inv *r.InventoryRecord, id, count int64, _ time.Time) error {
			return deliver(ctx, g, inv, Gift{ItemID: id, Count: count}, at)
		})
		return e
	})
	if e != nil {
		return PairResult{}, e
	}
	return result, nil
}

// BeginAdmittedCeremony starts presentation after durable scene admission.
// Completion and rewards belong to the subsequent server-owned end event.
func (o Owner) BeginAdmittedCeremony(ctx context.Context, req PairRequest, at int64) (PairResult, error) {
	return o.pair(ctx, req, func(a, b *r.CharacterRecord, _, _ *r.InventoryRecord) error {
		x, y, e := ValidatePair(*a, *b)
		if e != nil || x.Phase != Reserved || x.CeremonyAt != at || x.CeremonyReadyAt == 0 || req.Now.Unix() < x.CeremonyReadyAt {
			return ErrState
		}
		for _, c := range []*r.CharacterRecord{a, b} {
			if c.Stats["town_id"] != 39 || c.Stats["area_id"] != x.CeremonyArea {
				return ErrState
			}
		}
		x.Phase, y.Phase = InCeremony, InCeremony
		write(a, x)
		write(b, y)
		return nil
	})
}

// FinishAdmittedCeremony follows the reference server's PVF noti-end interval.
// It does not stop or fast-forward the native movie. Native cleanup may already
// have moved a spouse out; the durable admission authorizes completion even
// after leaving or disconnecting. The exact pair/version still commits once.
func (o Owner) FinishAdmittedCeremony(ctx context.Context, req PairRequest, at int64, grant AssetGrant) (PairResult, error) {
	if o.Rules == nil || o.Rules.CeremonySeconds <= 0 || o.Rules.CeremonySeconds > 86400 || grant == nil || req.Now.Unix() < 0 || req.Now.Unix() > 0xffffffff {
		return PairResult{}, ErrUnavailable
	}
	return o.pair(ctx, req, func(a, b *r.CharacterRecord, ai, bi *r.InventoryRecord) error {
		x, y, e := ValidatePair(*a, *b)
		if e != nil || x.Phase != InCeremony || x.CeremonyAt != at || x.CeremonyReadyAt == 0 || req.Now.Unix() < x.CeremonyReadyAt+o.Rules.CeremonySeconds {
			return ErrState
		}
		gift, e := o.ceremonyGift(x.Kind, grant)
		if e != nil {
			return e
		}
		if e = grant(ai, gift.ItemID, gift.Count, req.Now); e != nil {
			return e
		}
		if e = grant(bi, gift.ItemID, gift.Count, req.Now); e != nil {
			return e
		}
		x.Phase, y.Phase = Married, Married
		x.Ring, y.Ring = o.Rules.Ceremonies[x.Kind].Ring, o.Rules.Ceremonies[x.Kind].Ring
		x.MarriedAt, y.MarriedAt = req.Now.Unix(), req.Now.Unix()
		write(a, x)
		write(b, y)
		return nil
	})
}
