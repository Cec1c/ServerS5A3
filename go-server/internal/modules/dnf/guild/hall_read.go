package guild

import (
	"context"
	"errors"
	"slices"
	"strings"

	r "longheng.io/server/internal/modules/dnf/repository"
)

// HallReadView binds one refresh or party admission to one immutable source
// version. Acquire a new view for a later request or post-commit projection.
type HallReadView struct{ snapshot *guildReadSnapshot }

// HallProjection contains only the fields used by native hall packets and
// practice admission. It is detached from the cached registry and deliberately
// cannot be passed to Save as a partial authoritative Registry.
type HallProjection struct {
	GuildID    uint32
	Essence    EssenceBalance
	Guardians  *GuardianState
	Merchants  *MerchantState
	Placements *PlacementState
}

func ReadHallView(ctx context.Context, settings r.SettingsRepository) (*HallReadView, error) {
	snapshot, err := loadGuildReadSnapshot(ctx, settings)
	if errors.Is(err, ErrRankingSnapshotChanged) {
		// Continuous writers can exhaust cache-admission retries. Retain the
		// original atomic source read rather than introducing a refresh failure
		// merely because no version remained stable long enough to cache it.
		var registry Registry
		registry, err = Load(ctx, settings)
		if err == nil {
			snapshot, err = newGuildReadSnapshot(registry)
		}
	}
	if err != nil {
		return nil, err
	}
	return &HallReadView{snapshot: snapshot}, nil
}

func (view *HallReadView) owned(owner r.GuildOwner) *Guild {
	if view == nil || view.snapshot == nil || owner.AccountID == "" {
		return nil
	}
	owner.CharacterID = strings.TrimSpace(owner.CharacterID)
	return view.snapshot.owned(owner)
}

func (view *HallReadView) GuildID(owner r.GuildOwner) (uint32, bool) {
	g := view.owned(owner)
	if g == nil {
		return 0, false
	}
	return g.ID, true
}

func (view *HallReadView) Projection(owner r.GuildOwner) (HallProjection, bool) {
	g := view.owned(owner)
	if g == nil {
		return HallProjection{}, false
	}
	result := HallProjection{GuildID: g.ID, Essence: g.Essence}
	if g.Guardians != nil {
		state := *g.Guardians
		state.Owned = slices.Clone(state.Owned)
		result.Guardians = &state
	}
	if g.Merchants != nil {
		state := *g.Merchants
		result.Merchants = &state
	}
	if g.Placements != nil {
		state := *g.Placements
		state.Rows = slices.Clone(state.Rows)
		for index := range state.Rows {
			state.Rows[index].Cells = slices.Clone(state.Rows[index].Cells)
		}
		result.Placements = &state
	}
	return result, true
}

// MerchantLevel checks current membership and the expected committed guild.
// A receipt alone does not authorize projecting a former guild's state.
func (view *HallReadView) MerchantLevel(owner r.GuildOwner, guildID uint32, index uint8) (uint8, error) {
	g := view.owned(owner)
	if g == nil || g.ID != guildID {
		return 0, ErrNotMember
	}
	return MerchantLevel(g, index)
}
