package inventory

import (
	"context"
	"time"

	r "longheng.io/server/internal/modules/dnf/repository"
)

// Native247 supplies a destination cargo slot, but no destination item ID.
// Resolve that ID from the locked registry rather than a bridge pre-read.
type GuildCargoDepositRequest struct {
	Owner      r.GuildOwner
	GuildID    uint32
	SourceList byte
	SourceSlot uint16
	ItemID     uint32
	Count      uint32
	CargoSlot  uint16
	RequestID  string
	Revalidate func() bool
}

func (o *Owner) DepositGuildCargo(ctx context.Context, req GuildCargoDepositRequest, rules GuildCargoRules, now time.Time) (GuildCargoResult, error) {
	return o.transferGuildCargo(ctx, GuildCargoRequest{
		Owner: req.Owner, GuildID: req.GuildID,
		Source:       GuildCargoLocation{List: req.SourceList, Slot: req.SourceSlot},
		Destination:  GuildCargoLocation{Cargo: true, Slot: req.CargoSlot},
		SourceItemID: req.ItemID, Count: req.Count, Revalidate: req.Revalidate, RequestID: req.RequestID,
	}, rules, now, guildCargoResolveDeposit)
}
