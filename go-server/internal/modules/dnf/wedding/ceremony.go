package wedding

import (
	"context"

	r "longheng.io/server/internal/modules/dnf/repository"
)

type Gift struct{ ItemID, Count int64 }

// Local recovery policy: the native reservation notice stops being accepted
// sixty seconds after its start. This is not an animation duration or a PVF
// refund rule. Fully admitted participants retain their gift authorization.
const CeremonyAdmissionGraceSeconds int64 = 60

func ReservationExpired(s State, now int64) bool {
	return (s.Phase == Reserved || s.Phase == InCeremony) && s.CeremonyReadyAt == 0 && now > s.CeremonyAt+CeremonyAdmissionGraceSeconds
}

type CeremonyPosition struct {
	X, Y      int16
	Direction byte
}

// The route is selected from the current channel and a real PVF Church map.
// It is never taken from a client's destination or an invitation identifier.
type CeremonyRoute struct{ Channel, Area byte }

func (p CeremonyRoute) Valid() bool { return p.Channel != 0 && p.Area >= 8 && p.Area <= 38 }

// Begin is retained for a durable intermediate ceremony state. The live bridge
// normally calls StartCeremony, which grants and marries both owners together.
func (o Owner) Begin(ctx context.Context, req PairRequest, reservedAt int64) (PairResult, error) {
	return o.pair(ctx, req, func(a, b *r.CharacterRecord, _, _ *r.InventoryRecord) error {
		x, y, err := ValidatePair(*a, *b)
		if err != nil || x.Phase != Reserved || x.CeremonyAt != reservedAt || req.Now.Unix() < reservedAt {
			return ErrState
		}
		x.Phase, y.Phase = InCeremony, InCeremony
		write(a, x)
		write(b, y)
		return nil
	})
}

func (o Owner) ceremonyGift(kind byte, grant AssetGrant) (Gift, error) {
	if o.Rules == nil || grant == nil {
		return Gift{}, ErrUnavailable
	}
	ceremony, known := o.Rules.Ceremonies[kind]
	if !known || ceremony.Ring < 0 || ceremony.Ring >= len(o.Rules.Rings) {
		return Gift{}, ErrState
	}
	gift, known := o.Rules.CeremonyGifts[kind]
	if !known || gift.ItemID <= 0 || gift.Count <= 0 {
		return Gift{}, ErrUnavailable
	}
	return gift, nil
}

func (o Owner) Complete(ctx context.Context, req PairRequest, reservedAt int64, grant AssetGrant) (PairResult, error) {
	return o.completeCeremony(ctx, req, reservedAt, grant, false)
}

// StartCeremony authorizes marriage at the server-owned start event. Native
// animation completion is presentation only and cannot mint marriage or gifts.
// Both participants must already have a committed Church location; the bridge
// additionally validates current presence, channel, instance and generations.
func (o Owner) StartCeremony(ctx context.Context, req PairRequest, reservedAt int64, grant AssetGrant) (PairResult, error) {
	return o.completeCeremony(ctx, req, reservedAt, grant, true)
}

func (o Owner) completeCeremony(ctx context.Context, req PairRequest, reservedAt int64, grant AssetGrant, start bool) (PairResult, error) {
	if o.Rules == nil || grant == nil {
		return PairResult{}, ErrUnavailable
	}
	if req.Now.Unix() < 0 || req.Now.Unix() > 0xffffffff {
		return PairResult{}, ErrState
	}
	return o.pair(ctx, req, func(a, b *r.CharacterRecord, ai, bi *r.InventoryRecord) error {
		x, y, err := ValidatePair(*a, *b)
		if err != nil || x.CeremonyAt != reservedAt || reservedAt <= 0 || req.Now.Unix() < reservedAt || ReservationExpired(x, req.Now.Unix()) ||
			(x.Phase != InCeremony && (!start || x.Phase != Reserved)) {
			return ErrState
		}
		for _, c := range []*r.CharacterRecord{a, b} {
			if c.Stats["town_id"] != 39 || c.Stats["area_id"] != x.CeremonyArea {
				return ErrState
			}
		}
		gift, err := o.ceremonyGift(x.Kind, grant)
		if err != nil {
			return err
		}
		if err = grant(ai, gift.ItemID, gift.Count, req.Now); err != nil {
			return err
		}
		if err = grant(bi, gift.ItemID, gift.Count, req.Now); err != nil {
			return err
		}
		x.Phase, y.Phase = Married, Married
		x.Ring, y.Ring = o.Rules.Ceremonies[x.Kind].Ring, o.Rules.Ceremonies[x.Kind].Ring
		x.MarriedAt, y.MarriedAt = req.Now.Unix(), req.Now.Unix()
		write(a, x)
		write(b, y)
		return nil
	})
}

// EnterChurch persists the selected owner's location under the same pair lock
// as reservation validation. A later transport write can recover this location
// rather than repeating a wallet or gift mutation.
func (o Owner) EnterChurch(ctx context.Context, req PairRequest, reservedAt int64, position CeremonyPosition) (PairResult, error) {
	if position.X < 0 || position.Y < 0 {
		return PairResult{}, ErrState
	}
	return o.pair(ctx, req, func(a, b *r.CharacterRecord, _, _ *r.InventoryRecord) error {
		x, _, err := ValidatePair(*a, *b)
		if err != nil || (x.Phase != Reserved && x.Phase != InCeremony) || x.CeremonyAt != reservedAt || ReservationExpired(x, req.Now.Unix()) {
			return ErrState
		}
		if a.Stats == nil {
			return ErrState
		}
		a.Stats["town_id"], a.Stats["area_id"] = 39, x.CeremonyArea
		a.Stats[statPrefix+"guest_key"], a.Stats[statPrefix+"guest_at"] = 0, 0
		a.Stats["pos_x"], a.Stats["pos_y"], a.Stats["direction"] = int64(position.X), int64(position.Y), int64(position.Direction)
		return nil
	})
}

// MarkCeremonyReady persists admission before trying either gift inventory.
// Revalidate is supplied by the live scene owner and checked before and after
// the pair mutation, including both finish-loading gates and generations.
func (o Owner) MarkCeremonyReady(ctx context.Context, req PairRequest, reservedAt int64) (PairResult, error) {
	if req.Revalidate == nil {
		return PairResult{}, ErrUnavailable
	}
	return o.pair(ctx, req, func(a, b *r.CharacterRecord, _, _ *r.InventoryRecord) error {
		x, y, err := ValidatePair(*a, *b)
		if err != nil || (x.Phase != Reserved && x.Phase != InCeremony) || x.CeremonyAt != reservedAt || req.Now.Unix() < reservedAt || ReservationExpired(x, req.Now.Unix()) {
			return ErrState
		}
		if x.CeremonyReadyAt != 0 {
			return nil
		}
		for _, c := range []*r.CharacterRecord{a, b} {
			if c.Stats["town_id"] != 39 || c.Stats["area_id"] != x.CeremonyArea {
				return ErrState
			}
		}
		x.CeremonyReadyAt, y.CeremonyReadyAt = req.Now.Unix(), req.Now.Unix()
		write(a, x)
		write(b, y)
		return nil
	})
}
