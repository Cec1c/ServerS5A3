package memory

import (
	"context"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"

	"longheng.io/server/internal/modules/dnf/repository"
	platformdb "longheng.io/server/internal/platform/db"
)

func TestMemoryCharacterGuildSummaryUsesCurrentRecordWithoutWriting(t *testing.T) {
	ctx := context.Background()
	var clones atomic.Int64
	characters := &memoryCharStore{MemoryStore: platformdb.NewMemoryStore(repository.CharacterKey, func(record repository.CharacterRecord) repository.CharacterRecord {
		clones.Add(1)
		return repository.CloneCharacter(record)
	})}
	record := repository.CharacterRecord{
		CharacterID: "character-7",
		AccountID:   "account-7",
		Name:        "current-name",
		Job:         "17",
		Level:       91,
		Stats: map[string]int64{
			repository.CharacterGrowTypeStatKey: 34,
			"gold":                              99,
		},
		Roster: repository.CharacterRoster{
			Entry: repository.CharacterRosterEntry{ObjectID: 77},
		},
	}
	if err := characters.Save(ctx, record); err != nil {
		t.Fatal(err)
	}
	before, found, err := characters.Load(ctx, record.CharacterID)
	if err != nil || !found {
		t.Fatalf("load before summary found=%t err=%v", found, err)
	}
	clones.Store(0)

	summaries, err := repository.LoadCharacterGuildSummaries(ctx, characters, []string{
		record.CharacterID, " " + record.CharacterID + " ", "missing", "",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := repository.CharacterGuildSummary{
		CharacterID: record.CharacterID,
		AccountID:   record.AccountID,
		Name:        record.Name,
		Job:         record.Job,
		Level:       record.Level,
		GrowType:    34,
	}
	if len(summaries) != 1 || summaries[record.CharacterID] != want {
		t.Fatalf("summaries = %+v, want %+v", summaries, want)
	}
	if clones.Load() != 0 {
		t.Fatalf("summary read invoked clone function %d times", clones.Load())
	}
	after, found, err := characters.Load(ctx, record.CharacterID)
	if err != nil || !found {
		t.Fatalf("load after summary found=%t err=%v", found, err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("summary read changed the character record:\nbefore=%+v\nafter=%+v", before, after)
	}
}

type directGuildSummaryCharacter struct {
	repository.CharacterRepository
	loadCalls int
	ids       []string
	err       error
}

func (r *directGuildSummaryCharacter) LoadCharacterGuildSummaries(_ context.Context, ids []string) (map[string]repository.CharacterGuildSummary, error) {
	r.ids = append([]string(nil), ids...)
	return nil, r.err
}

func (r *directGuildSummaryCharacter) Load(context.Context, string) (repository.CharacterRecord, bool, error) {
	r.loadCalls++
	return repository.CharacterRecord{}, false, nil
}

func TestLoadCharacterGuildSummariesUsesReaderWithoutFallbackOnError(t *testing.T) {
	wantErr := errors.New("direct summary read failed")
	characters := &directGuildSummaryCharacter{err: wantErr}
	ids := []string{"b", "a", "a"}

	got, err := repository.LoadCharacterGuildSummaries(context.Background(), characters, ids)
	if !errors.Is(err, wantErr) || got != nil {
		t.Fatalf("summary=%v err=%v, want nil/%v", got, err, wantErr)
	}
	if characters.loadCalls != 0 || !reflect.DeepEqual(characters.ids, ids) {
		t.Fatalf("fallback Load calls=%d direct ids=%v, want 0/%v", characters.loadCalls, characters.ids, ids)
	}
}

type fallbackGuildSummaryCharacter struct {
	repository.CharacterRepository
	records      map[string]repository.CharacterRecord
	errors       map[string]error
	loads        []string
	cancelOnLoad context.CancelFunc
}

func (r *fallbackGuildSummaryCharacter) Load(_ context.Context, id string) (repository.CharacterRecord, bool, error) {
	r.loads = append(r.loads, id)
	if err := r.errors[id]; err != nil {
		return repository.CharacterRecord{}, false, err
	}
	record, found := r.records[id]
	if cancel := r.cancelOnLoad; cancel != nil {
		r.cancelOnLoad = nil
		cancel()
	}
	return record, found, nil
}

func TestLoadCharacterGuildSummariesFallbackDeduplicatesAndOmitsMissing(t *testing.T) {
	characters := &fallbackGuildSummaryCharacter{records: map[string]repository.CharacterRecord{
		"a": {
			CharacterID: "a", AccountID: "account-a", Name: "alpha", Job: "3", Level: 40,
			Stats: map[string]int64{repository.CharacterGrowTypeStatKey: 33, "gold": 1},
		},
	}}

	ctx, counts := repository.WithCharacterGuildSummaryReadCounts(context.Background())
	got, err := repository.LoadCharacterGuildSummaries(ctx, characters, []string{" z ", "missing", "a", "z", ""})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]repository.CharacterGuildSummary{
		"a": {CharacterID: "a", AccountID: "account-a", Name: "alpha", Job: "3", Level: 40, GrowType: 33},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("summaries = %+v, want %+v", got, want)
	}
	if !reflect.DeepEqual(characters.loads, []string{"a", "missing", "z"}) {
		t.Fatalf("fallback Load ids = %v, want [a missing z]", characters.loads)
	}
	if counts.FullLoads != int64(len(characters.loads)) || counts.Queries != 0 {
		t.Fatalf("fallback counts = queries:%d full_loads:%d, want 0/%d", counts.Queries, counts.FullLoads, len(characters.loads))
	}
}

func TestLoadCharacterGuildSummariesFallbackChecksCancellationAfterLoad(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	characters := &fallbackGuildSummaryCharacter{
		records:      map[string]repository.CharacterRecord{"a": {CharacterID: "a"}},
		cancelOnLoad: cancel,
	}

	got, err := repository.LoadCharacterGuildSummaries(ctx, characters, []string{"b", "a"})
	if !errors.Is(err, context.Canceled) || got != nil {
		t.Fatalf("summary=%v err=%v, want nil/context.Canceled", got, err)
	}
	if !reflect.DeepEqual(characters.loads, []string{"a"}) {
		t.Fatalf("fallback Load ids = %v, want [a]", characters.loads)
	}
}

func TestLoadCharacterGuildSummariesFallbackDoesNotReturnPartialSuccess(t *testing.T) {
	wantErr := errors.New("fallback read failed")
	characters := &fallbackGuildSummaryCharacter{
		records: map[string]repository.CharacterRecord{"a": {CharacterID: "a"}},
		errors:  map[string]error{"b": wantErr},
	}

	got, err := repository.LoadCharacterGuildSummaries(context.Background(), characters, []string{"a", "b"})
	if !errors.Is(err, wantErr) || got != nil {
		t.Fatalf("summary=%v err=%v, want nil/%v", got, err, wantErr)
	}
}
