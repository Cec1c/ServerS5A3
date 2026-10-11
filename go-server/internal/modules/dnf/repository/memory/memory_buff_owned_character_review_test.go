package memory

import (
	"context"
	"errors"
	"longheng.io/server/internal/modules/dnf/repository"
	"reflect"
	"testing"
)

func TestReviewBuffOwnedCharacterSnapshotInterfaceScope(t *testing.T) {
	makeSnapshot := func() *memoryBuffReadCharacter {
		return &memoryBuffReadCharacter{owner: "31", record: repository.CharacterRecord{CharacterID: "31", AccountID: "9", Name: "owned", Stats: map[string]int64{"strength": 7}}}
	}
	t.Run("owned_detached", func(t *testing.T) {
		r := makeSnapshot()
		got, found, err := r.Load(nil, "31")
		if err != nil || !found {
			t.Fatalf("load: found=%v err=%v", found, err)
		}
		got.Stats["strength"] = 99
		again, found, err := r.Load(context.Background(), "31")
		if err != nil || !found || again.Stats["strength"] != 7 {
			t.Fatalf("snapshot escaped: %+v found=%v err=%v", again, found, err)
		}
	})
	t.Run("foreign_owner", func(t *testing.T) {
		r := makeSnapshot()
		got, found, err := r.Load(nil, "32")
		if err == nil || found || !reflect.DeepEqual(got, repository.CharacterRecord{}) {
			t.Fatalf("foreign load leaked: %+v found=%v err=%v", got, found, err)
		}
	})
	t.Run("unsupported_nil_context", func(t *testing.T) {
		r := makeSnapshot()
		rows, err := r.ListByAccount(nil, "9", 32)
		if len(rows) != 0 || !errors.Is(err, repository.ErrBuffOwnedSnapshotUnavailable) {
			t.Fatalf("list: %+v %v", rows, err)
		}
		id, found, err := r.FindIDByName(nil, "owned")
		if id != "" || found || !errors.Is(err, repository.ErrBuffOwnedSnapshotUnavailable) {
			t.Fatalf("find: %q %v %v", id, found, err)
		}
		next, err := r.NextNumericID(nil)
		if next != 0 || !errors.Is(err, repository.ErrBuffOwnedSnapshotUnavailable) {
			t.Fatalf("next: %d %v", next, err)
		}
	})
	t.Run("cancelled_context", func(t *testing.T) {
		r := makeSnapshot()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, found, err := r.Load(ctx, "31")
		if found || !errors.Is(err, context.Canceled) {
			t.Fatalf("load: %v %v", found, err)
		}
		_, err = r.ListByAccount(ctx, "9", 32)
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		_, found, err = r.FindIDByName(ctx, "owned")
		if found || !errors.Is(err, context.Canceled) {
			t.Fatalf("find: %v %v", found, err)
		}
		_, err = r.NextNumericID(ctx)
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	})
	t.Run("save_readonly", func(t *testing.T) {
		r := makeSnapshot()
		before := repository.CloneCharacter(r.record)
		err := r.Save(nil, repository.CharacterRecord{CharacterID: "31", Name: "replacement"})
		if !errors.Is(err, repository.ErrBuffOwnedSnapshotUnavailable) || !reflect.DeepEqual(before, r.record) {
			t.Fatalf("readonly save changed snapshot: %+v %v", r.record, err)
		}
	})
}
