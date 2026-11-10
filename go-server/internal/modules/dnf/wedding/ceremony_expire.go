package wedding

import (
	"context"
	r "longheng.io/server/internal/modules/dnf/repository"
)

type CeremonyReturnLocation struct {
	Town, Area, X, Y, Direction int64
}

func (p CeremonyReturnLocation) Valid() bool {
	return p.Town > 0 && p.Town <= 255 && (p.Town != 39 || p.Area < 8 || p.Area > 38) && p.Area >= 0 && p.Area <= 255 && p.X >= 0 && p.X <= 32767 && p.Y >= 0 && p.Y <= 32767 && p.Direction >= 0 && p.Direction <= 255
}

// ExpireReservation is this server's recovery policy for an unattended
// reservation. It preserves the engagement, paid costs and daily application
// limit. The caller resolves return coordinates from the actual town catalog.
func (o Owner) ExpireReservation(ctx context.Context, req PairRequest, reservedAt int64, returns map[string]CeremonyReturnLocation) (PairResult, error) {
	return o.pair(ctx, req, func(a, b *r.CharacterRecord, _, _ *r.InventoryRecord) error {
		x, y, err := ValidatePair(*a, *b)
		if err != nil || x.CeremonyAt != reservedAt || !ReservationExpired(x, req.Now.Unix()) {
			return ErrState
		}
		for _, c := range []*r.CharacterRecord{a, b} {
			if c.Stats["town_id"] != 39 || c.Stats["area_id"] != x.CeremonyArea {
				continue
			}
			p, ok := returns[c.CharacterID]
			if !ok || !p.Valid() {
				return ErrUnavailable
			}
			c.Stats["town_id"], c.Stats["area_id"] = p.Town, p.Area
			c.Stats["pos_x"], c.Stats["pos_y"], c.Stats["direction"] = p.X, p.Y, p.Direction
		}
		x.Phase, y.Phase = Engaged, Engaged
		x.CeremonyAt, y.CeremonyAt = 0, 0
		x.CeremonyChannel, y.CeremonyChannel = 0, 0
		x.CeremonyArea, y.CeremonyArea = 0, 0
		write(a, x)
		write(b, y)
		return nil
	})
}
