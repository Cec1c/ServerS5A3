package guild

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	r "longheng.io/server/internal/modules/dnf/repository"
)

var ErrContributionBackfillConflict = errors.New("guild registry changed during contribution backfill")

// InitializeContributionProjection runs before listeners open. The expensive
// historical read never holds a write lock. CAS refuses to overwrite a writer
// that committed while the snapshot was being computed; retries are bounded.
func InitializeContributionProjection(ctx context.Context, uow r.GuildReadUnitOfWork, now time.Time) (int, error) {
	if uow == nil {
		return 0, ErrRepositoryUnavailable
	}
	for attempt := 0; attempt < 3; attempt++ {
		var before, after r.SettingsRecord
		count := 0
		err := uow.WithinGuildRead(ctx, func(view r.GuildReadView) error {
			var err error
			var found bool
			before, found, err = view.Settings.Load(ctx, RegistryScope)
			if err != nil || !found {
				return err
			}
			registry, err := Load(ctx, view.Settings)
			if err != nil {
				return err
			}
			for _, g := range registry.Guilds {
				missing := false
				for _, m := range g.Members {
					if m.DungeonContributionPoints == nil {
						missing = true
					}
				}
				if !missing {
					continue
				}
				members, err := HongbaoContributionMembers(ctx, view.Settings, g)
				if err != nil {
					return err
				}
				for _, projected := range members {
					m := g.Members[projected.CharacterID]
					if m.DungeonContributionPoints != nil {
						continue
					}
					points := m.ContributionPoints - projected.ContributionPoints
					m.DungeonContributionPoints = &points
					g.Members[m.CharacterID] = m
					count++
				}
			}
			if count == 0 {
				return nil
			}
			encoded, err := json.Marshal(registry)
			if err != nil {
				return err
			}
			after = r.CloneSettings(before)
			after.Values[registryStateKey] = string(encoded)
			after.UpdatedAt = now
			return nil
		})
		if err != nil || count == 0 {
			return count, err
		}
		updated, err := uow.CompareGuildRegistry(ctx, before, after)
		if err != nil {
			return 0, err
		}
		if updated {
			return count, nil
		}
		if err := ctx.Err(); err != nil {
			return 0, err
		}
	}
	return 0, ErrContributionBackfillConflict
}
