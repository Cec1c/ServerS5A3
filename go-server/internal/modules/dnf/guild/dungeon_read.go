package guild

import (
	"context"
	"time"

	r "longheng.io/server/internal/modules/dnf/repository"
)

// DungeonReadView is an immutable registry version for one bounded read. Its
// methods return detached counts or entry values, never a saveable Registry
// or a mutable guild pointer. Settlement still reloads and validates inside
// its authoritative transaction.
type DungeonReadView struct{ snapshot *guildReadSnapshot }

func ReadDungeonView(ctx context.Context, settings r.SettingsRepository) (*DungeonReadView, error) {
	snapshot, err := loadGuildReadSnapshot(ctx, settings)
	if err != nil {
		return nil, err
	}
	return &DungeonReadView{snapshot: snapshot}, nil
}

func (v *DungeonReadView) Counts(owner r.GuildOwner, now time.Time) (DungeonCycle, map[uint32]uint32, bool, error) {
	return v.CountsForGuild(owner, 0, now)
}

func (v *DungeonReadView) CountsForGuild(owner r.GuildOwner, expectedGuildID uint32, now time.Time) (DungeonCycle, map[uint32]uint32, bool, error) {
	if expectedGuildID == 0 {
		return v.CountsForGuilds(owner, nil, now)
	}
	return v.CountsForGuilds(owner, []uint32{expectedGuildID}, now)
}

func (v *DungeonReadView) CountsForGuilds(owner r.GuildOwner, expectedGuildIDs []uint32, now time.Time) (DungeonCycle, map[uint32]uint32, bool, error) {
	if v == nil || v.snapshot == nil {
		return DungeonCycle{}, nil, false, ErrRepositoryUnavailable
	}
	g := v.snapshot.owned(owner)
	if g == nil {
		return DungeonCycle{}, nil, false, nil
	}
	if len(expectedGuildIDs) > 0 {
		matched := false
		for _, id := range expectedGuildIDs {
			if id != 0 && g.ID == id {
				matched = true
				break
			}
		}
		if !matched {
			return DungeonCycle{}, nil, false, nil
		}
	}
	cycle, err := v.snapshot.registry.DungeonPolicy.Cycle(now)
	if err != nil {
		return DungeonCycle{}, nil, false, err
	}
	counts, err := DungeonCounts(g, cycle)
	return cycle, counts, true, err
}

func (v *DungeonReadView) PrepareEntries(catalog *DungeonCatalog, id uint32, owners []r.GuildOwner, runID string, now time.Time) ([]DungeonEntry, error) {
	if v == nil || v.snapshot == nil {
		return nil, ErrRepositoryUnavailable
	}
	return PrepareDungeonEntries(v.snapshot.registry, catalog, id, owners, runID, now)
}
