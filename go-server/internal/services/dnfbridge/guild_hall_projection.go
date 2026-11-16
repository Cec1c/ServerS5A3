package dnfbridge

import "longheng.io/server/internal/modules/dnf/guild"

// Adapt the detached hall projection to the existing packet builders. This
// object is packet input only; it has no membership or authoritative history
// and must never be passed to a mutation or persisted as a complete guild.
func currentGuildHallDisplay(projection guild.HallProjection) *guild.Guild {
	return &guild.Guild{
		ID: projection.GuildID, Essence: projection.Essence,
		Guardians: projection.Guardians, Merchants: projection.Merchants,
		Placements: projection.Placements,
	}
}
