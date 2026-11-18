package dnfbridge

import (
	"context"
	"fmt"
	"strings"
	"testing"

	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/tagtournament"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

type tagColdEntrySettings struct {
	repo.SettingsRepository
	initial context.Context
	frozen  bool
}

func (r *tagColdEntrySettings) Load(ctx context.Context, scope string) (repo.SettingsRecord, bool, error) {
	if !r.frozen {
		r.initial = ctx
	}
	if err := ctx.Err(); err != nil {
		return repo.SettingsRecord{}, false, err
	}
	return r.SettingsRepository.Load(ctx, scope)
}

type tagColdEntryMaps struct {
	townMoveTestSource
	settings *tagColdEntrySettings
	waited   bool
	reads    []string
}

func (r *tagColdEntryMaps) ReadText(path string) (string, error) {
	r.reads = append(r.reads, path)
	if strings.HasSuffix(strings.ToLower(path), ".pvf") && !r.waited {
		if r.settings.initial == nil {
			return "", fmt.Errorf("cold map preceded character snapshot")
		}
		r.waited = true
		r.settings.frozen = true
		// The same cold resource boundary used by production's map cache. Wait
		// for the actual initial repository deadline rather than guessing a sleep.
		<-r.settings.initial.Done()
	}
	return r.townMoveTestSource.ReadText(path)
}

func TestTagTournamentColdMapsDoNotExpireFinalCharacterRead(t *testing.T) {
	s, p, g := tagPlayFixture(t)
	settings := &tagColdEntrySettings{SettingsRepository: g.Settings}
	g.Settings = settings
	s.repositoryProvider = func() (repo.Group, bool) { return g, true }
	cold := &tagColdEntryMaps{townMoveTestSource: tagPlayTestSource(t), settings: settings}
	options := worldmap.DefaultMapCacheOptions()
	options.PinnedBytes = 0
	if err := s.worldMapTable.EnableMapCache(cold, options); err != nil {
		t.Fatal(err)
	}
	tagPlayChallenge(t, s, p, tagtournament.Normal)
	if !cold.waited || settings.initial.Err() != context.DeadlineExceeded {
		t.Fatal("test did not cross the real initial read deadline", cold.waited, settings.initial.Err(), cold.reads)
	}
}
