package onlineevent

import (
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

type rest26WrappedWriteStore struct{ repository.SettingsRepository }

func (s rest26WrappedWriteStore) Save(context.Context, repository.SettingsRecord) error {
	return fmt.Errorf("settings write aborted: %w", ErrControlRevisionStale)
}

func rest26Controls(t *testing.T, store repository.SettingsRepository) *Controls {
	t.Helper()
	c, err := NewControls(context.Background(), store, []ControlDefinition{{ID: 2347, Name: "online", DefaultEnabled: true}, {ID: 2365, Name: "joust", DefaultEnabled: false}})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// TestRest26ControlsSaveClassifiesValidationErrors keeps the existing operator
// text while making each Save rejection identifiable through errors.Is.
func TestRest26ControlsSaveClassifiesValidationErrors(t *testing.T) {
	ctx := context.Background()
	c := rest26Controls(t, memory.NewMemoryGroup().Settings)
	if _, err := c.Save(ctx, 0, []ControlChange{{ID: 2347, Enabled: false}}); err != nil {
		t.Fatal(err)
	}
	settled := c.Snapshot()

	all := []error{ErrControlRevisionStale, ErrControlSelectionInvalid, ErrControlUnsupported, ErrControlDuplicate, ErrControlRevisionExhausted, ErrControlPersist}
	for _, tc := range []struct {
		name     string
		revision uint64
		changes  []ControlChange
		kind     error
		text     string
	}{
		{"stale_revision", 0, []ControlChange{{ID: 2347, Enabled: true}}, ErrControlRevisionStale, "活动设置已更新，请刷新后重试"},
		{"unsupported_id", 1, []ControlChange{{ID: 181, Enabled: true}}, ErrControlUnsupported, "不支持开启活动 181"},
		{"duplicate_id", 1, []ControlChange{{ID: 2365, Enabled: true}, {ID: 2365, Enabled: false}}, ErrControlDuplicate, "活动 2365 重复"},
		{"empty_selection", 1, nil, ErrControlSelectionInvalid, "请选择受支持的活动"},
	} {
		_, err := c.Save(ctx, tc.revision, tc.changes)
		if !errors.Is(err, tc.kind) {
			t.Fatalf("%s: not classified as %v: %v", tc.name, tc.kind, err)
		}
		if err == nil || err.Error() != tc.text {
			t.Fatalf("%s: operator text changed: %v", tc.name, err)
		}
		for _, other := range all {
			if other != tc.kind && errors.Is(err, other) {
				t.Fatalf("%s: also classified as %v", tc.name, other)
			}
		}
		if got := c.Snapshot(); !reflect.DeepEqual(settled, got) {
			t.Fatalf("%s: rejected save mutated state: %+v", tc.name, got)
		}
	}

	failing := rest26Controls(t, failingControlStore{memory.NewMemoryGroup().Settings})
	before := failing.Snapshot()
	_, err := failing.Save(ctx, 0, []ControlChange{{ID: 2347, Enabled: false}})
	if !errors.Is(err, ErrControlPersist) {
		t.Fatalf("storage failure not classified: %v", err)
	}
	if err == nil || err.Error() != "保存活动设置失败: write unavailable" {
		t.Fatalf("storage failure text changed: %v", err)
	}
	for _, other := range all {
		if other != ErrControlPersist && errors.Is(err, other) {
			t.Fatalf("storage failure also classified as %v", other)
		}
	}
	if got := failing.Snapshot(); !reflect.DeepEqual(before, got) {
		t.Fatalf("storage failure mutated state: %+v", got)
	}
}

// TestRest26ControlsWrappedStorageCauseKeepsStorageIdentity is the ordering
// counterexample: when a repository error wraps a validation sentinel, the
// result carries both identities, so callers must test ErrControlPersist first.
func TestRest26ControlsWrappedStorageCauseKeepsStorageIdentity(t *testing.T) {
	c := rest26Controls(t, rest26WrappedWriteStore{memory.NewMemoryGroup().Settings})
	before := c.Snapshot()
	_, err := c.Save(context.Background(), 0, []ControlChange{{ID: 2347, Enabled: false}})
	if !errors.Is(err, ErrControlPersist) {
		t.Fatalf("wrapped write failure lost storage identity: %v", err)
	}
	if !errors.Is(err, ErrControlRevisionStale) {
		t.Fatalf("wrapped cause sentinel is not visible to the caller: %v", err)
	}
	if got := c.Snapshot(); !reflect.DeepEqual(before, got) {
		t.Fatalf("wrapped write failure mutated state: %+v", got)
	}
}

// TestRest26ControlsExhaustedRevisionSurvivesRestart records that the revision
// counter is persisted: a restarted Controls instance reads MaxUint64 back and
// still refuses to save, so exhausting it is terminal for that store.
func TestRest26ControlsExhaustedRevisionSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	repo := memory.NewMemoryGroup().Settings
	if err := repo.Save(ctx, repository.SettingsRecord{Scope: ControlsScope, Values: map[string]string{"revision": "18446744073709551615"}}); err != nil {
		t.Fatal(err)
	}
	for _, pass := range []string{"first_start", "restart"} {
		c := rest26Controls(t, repo)
		if c.Snapshot().Revision != math.MaxUint64 {
			t.Fatalf("%s: persisted revision not read back: %d", pass, c.Snapshot().Revision)
		}
		before := c.Snapshot()
		_, err := c.Save(ctx, math.MaxUint64, []ControlChange{{ID: 2347, Enabled: false}})
		if !errors.Is(err, ErrControlRevisionExhausted) || errors.Is(err, ErrControlPersist) {
			t.Fatalf("%s: exhausted revision misclassified: %v", pass, err)
		}
		if err == nil || err.Error() != "activity revision exhausted" {
			t.Fatalf("%s: exhausted text changed: %v", pass, err)
		}
		if got := c.Snapshot(); !reflect.DeepEqual(before, got) {
			t.Fatalf("%s: exhausted save mutated state: %+v", pass, got)
		}
	}
}
