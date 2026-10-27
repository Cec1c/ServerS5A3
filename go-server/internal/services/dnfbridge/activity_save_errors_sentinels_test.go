package dnfbridge

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/onlineevent"
	"longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

// rest26WrappedSentinelStore is a storage fault whose cause carries a
// validation sentinel: it must still be reported as a storage failure.
type rest26WrappedSentinelStore struct{ repository.SettingsRepository }

func (s rest26WrappedSentinelStore) Save(context.Context, repository.SettingsRecord) error {
	return fmt.Errorf("settings transaction aborted: %w", onlineevent.ErrControlRevisionStale)
}

// TestRest26ActivitySaveWrappedSentinelStorageFailureIsNotOperatorFixable is the
// prompt-ordering counterexample: this repository error satisfies both the
// storage sentinel and a validation sentinel, so the bridge must test
// ErrControlPersist before the conflict branches.
func TestRest26ActivitySaveWrappedSentinelStorageFailureIsNotOperatorFixable(t *testing.T) {
	s, controls := rest26Service(t, rest26WrappedSentinelStore{memory.NewMemoryGroup().Settings})
	before := controls.Snapshot()

	_, err := controls.Save(context.Background(), 0, []onlineevent.ControlChange{{ID: 2347, Enabled: false}})
	if !errors.Is(err, onlineevent.ErrControlPersist) || !errors.Is(err, onlineevent.ErrControlRevisionStale) {
		t.Fatalf("wrapped storage cause is not identifiable as storage plus conflict: %v", err)
	}
	if got := controls.Snapshot(); !reflect.DeepEqual(before, got) {
		t.Fatalf("wrapped storage failure mutated controls: %+v", got)
	}

	code, reply := rest26Save(t, s, `{"version":1,"action":"save_activities","revision":0,"changes":[{"id":2347,"enabled":false}]}`)
	if code != 200 || reply.OK {
		t.Fatalf("wrapped storage failure reported success: code=%d body=%+v", code, reply)
	}
	if !strings.Contains(reply.Error, "保存活动设置失败") || strings.Contains(reply.Error, "刷新") {
		t.Fatalf("wrapped storage cause was reported as an operator-fixable conflict: %q", reply.Error)
	}
	for _, leak := range []string{"settings transaction aborted", "activity control revision stale"} {
		if strings.Contains(reply.Error, leak) {
			t.Fatalf("storage cause detail leaked through the private API: %q", reply.Error)
		}
	}
}
