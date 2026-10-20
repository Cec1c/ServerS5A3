package onlineevent

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

type failingControlStore struct{ repository.SettingsRepository }

func (s failingControlStore) Save(context.Context, repository.SettingsRecord) error {
	return errors.New("write unavailable")
}

func TestControlsSavePersistsButOnlyRestartChangesEffectiveAvailability(t *testing.T) {
	ctx := context.Background()
	repo := memory.NewMemoryGroup().Settings
	defs := []ControlDefinition{{ID: 2347, Name: "online", DefaultEnabled: true}, {ID: 2365, Name: "joust", DefaultEnabled: false}}
	c, err := NewControls(ctx, repo, defs)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := c.Save(ctx, 0, []ControlChange{{ID: 2347, Enabled: false}, {ID: 2365, Enabled: true}})
	if err != nil {
		t.Fatal(err)
	}
	if !saved.RestartRequired || saved.Revision != 1 || !c.Enabled(2347) || c.Enabled(2365) {
		t.Fatalf("running state changed: %+v", saved)
	}
	if _, err = c.Save(ctx, 0, []ControlChange{{ID: 2347, Enabled: true}}); err == nil {
		t.Fatal("stale editor overwrote settings")
	}
	restarted, err := NewControls(ctx, repo, defs)
	if err != nil {
		t.Fatal(err)
	}
	if restarted.Enabled(2347) || !restarted.Enabled(2365) || restarted.Snapshot().RestartRequired {
		t.Fatalf("restart did not load durable policy: %+v", restarted.Snapshot())
	}
	for _, changes := range [][]ControlChange{{{ID: 181, Enabled: true}}, {{ID: 2347, Enabled: true}, {ID: 2347, Enabled: false}}, nil} {
		before := restarted.Snapshot()
		if _, err = restarted.Save(ctx, 1, changes); err == nil {
			t.Fatal("invalid changes accepted")
		}
		if !reflect.DeepEqual(before, restarted.Snapshot()) {
			t.Fatal("invalid request mutated state")
		}
	}
}

func TestControlsWriteFailureDoesNotPublishSuccessOrMutateState(t *testing.T) {
	repo := memory.NewMemoryGroup().Settings
	c, err := NewControls(context.Background(), failingControlStore{repo}, []ControlDefinition{{ID: 2347, Name: "online", DefaultEnabled: true}})
	if err != nil {
		t.Fatal(err)
	}
	before := c.Snapshot()
	if _, err = c.Save(context.Background(), 0, []ControlChange{{ID: 2347, Enabled: false}}); err == nil {
		t.Fatal("write failure ignored")
	}
	if !reflect.DeepEqual(before, c.Snapshot()) {
		t.Fatal("failed save mutated state")
	}
}

func TestControlsRejectCorruptPersistentPolicy(t *testing.T) {
	ctx := context.Background()
	for _, values := range []map[string]string{{"2347": "unknown"}, {"9999": "true"}, {"revision": "-1"}} {
		repo := memory.NewMemoryGroup().Settings
		if err := repo.Save(ctx, repository.SettingsRecord{Scope: ControlsScope, Values: values}); err != nil {
			t.Fatal(err)
		}
		if _, err := NewControls(ctx, repo, []ControlDefinition{{ID: 2347, Name: "online", DefaultEnabled: true}}); err == nil {
			t.Fatalf("corrupt settings accepted: %v", values)
		}
	}
}
