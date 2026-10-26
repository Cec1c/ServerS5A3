package dnfbridge

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/adminapi"
	"longheng.io/server/internal/modules/dnf/onlineevent"
	"longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

// rest26FakeSecret stands in for DSN credential text that must never reach the
// independent GM through the private management response.
const rest26FakeSecret = "FakeSecret-2b7c9d41"

// rest26LegacyFixedError is the single English message the bridge returned for
// every save failure; a rejected save must keep an actionable prompt instead.
const rest26LegacyFixedError = "activity revision or update invalid"

const rest26MaxRevision = "18446744073709551615"

type rest26FailingStore struct{ repository.SettingsRepository }

func (s rest26FailingStore) Save(context.Context, repository.SettingsRecord) error {
	return errors.New("dial tcp 10.9.8.7:3306: access denied for dsn=root:" + rest26FakeSecret + "@tcp(db.internal:3306)/dnf90")
}

func rest26Defs() []onlineevent.ControlDefinition {
	return []onlineevent.ControlDefinition{{ID: 2347, Name: "online", DefaultEnabled: true}, {ID: 2365, Name: "joust", DefaultEnabled: false}}
}

func rest26Service(t *testing.T, store repository.SettingsRepository) (*Service, *onlineevent.Controls) {
	t.Helper()
	controls, err := onlineevent.NewControls(context.Background(), store, rest26Defs())
	if err != nil {
		t.Fatal(err)
	}
	s := adminTestService(t)
	s.activityControls = controls
	return s, controls
}

func rest26Save(t *testing.T, s *Service, body string) (int, adminapi.Response) {
	t.Helper()
	w := adminTestCall(s, body, "127.0.0.1:1234", strings.Repeat("x", 32))
	var reply adminapi.Response
	if err := json.Unmarshal(w.Body.Bytes(), &reply); err != nil {
		t.Fatalf("decode reply: %v body=%s", err, w.Body.String())
	}
	if reply.Version != adminapi.Version {
		t.Fatalf("response schema version changed: %s", w.Body.String())
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("private response became cacheable")
	}
	return w.Code, reply
}

// TestRest26ActivitySaveKeepsActionablePrompts drives the real GameAdminHandler:
// stale revision, unsupported id, duplicate id and empty selection must each
// keep their own usable prompt instead of one fixed English message.
func TestRest26ActivitySaveKeepsActionablePrompts(t *testing.T) {
	s, controls := rest26Service(t, memory.NewMemoryGroup().Settings)

	code, reply := rest26Save(t, s, `{"version":1,"action":"save_activities","revision":0,"changes":[{"id":2347,"enabled":false}]}`)
	if code != 200 || !reply.OK || reply.Activities == nil {
		t.Fatalf("baseline save rejected: code=%d error=%q", code, reply.Error)
	}
	if reply.Activities.Revision != 1 || !reply.Activities.RestartRequired {
		t.Fatalf("baseline snapshot contract changed: %+v", *reply.Activities)
	}
	if controls.Snapshot().Revision != 1 || !controls.Enabled(2347) || controls.Enabled(2365) {
		t.Fatal("save changed running (restart-pending) availability")
	}
	settled := controls.Snapshot()

	prompts := map[string]string{}
	for _, tc := range []struct{ name, body, want string }{
		{"stale_revision", `{"version":1,"action":"save_activities","revision":0,"changes":[{"id":2347,"enabled":true}]}`, "刷新"},
		{"unsupported_id", `{"version":1,"action":"save_activities","revision":1,"changes":[{"id":9999,"enabled":true}]}`, "不支持"},
		{"duplicate_id", `{"version":1,"action":"save_activities","revision":1,"changes":[{"id":2365,"enabled":true},{"id":2365,"enabled":false}]}`, "重复"},
		{"empty_selection", `{"version":1,"action":"save_activities","revision":1,"changes":[]}`, "请选择"},
	} {
		code, reply = rest26Save(t, s, tc.body)
		if code != 200 || reply.OK {
			t.Fatalf("%s: rejected request reported success: code=%d body=%+v", tc.name, code, reply)
		}
		if reply.Error == "" || reply.Error == rest26LegacyFixedError {
			t.Fatalf("%s: actionable prompt lost: %q", tc.name, reply.Error)
		}
		if !strings.Contains(reply.Error, tc.want) {
			t.Fatalf("%s: prompt %q does not mention %q", tc.name, reply.Error, tc.want)
		}
		if reply.Activities != nil {
			t.Fatalf("%s: rejected save returned a snapshot", tc.name)
		}
		prompts[tc.name] = reply.Error
		if got := controls.Snapshot(); !reflect.DeepEqual(settled, got) {
			t.Fatalf("%s: rejected save mutated controls: %+v", tc.name, got)
		}
	}
	for left, l := range prompts {
		for right, r := range prompts {
			if left != right && l == r {
				t.Fatalf("distinct failures share one prompt: %s=%s=%q", left, right, l)
			}
		}
	}
}

// TestRest26ActivitySaveStorageFailureStaysSafeAndGeneric checks that a storage
// fault is answered with a generic actionable prompt and that the underlying
// error (fake DSN secret, address) never crosses the private API.
func TestRest26ActivitySaveStorageFailureStaysSafeAndGeneric(t *testing.T) {
	s, controls := rest26Service(t, rest26FailingStore{memory.NewMemoryGroup().Settings})
	before := controls.Snapshot()

	code, reply := rest26Save(t, s, `{"version":1,"action":"save_activities","revision":0,"changes":[{"id":2347,"enabled":false}]}`)
	if code != 200 || reply.OK || reply.Error == "" {
		t.Fatalf("storage failure not reported: code=%d body=%+v", code, reply)
	}
	if reply.Error == rest26LegacyFixedError {
		t.Fatalf("storage failure kept the fixed English message: %q", reply.Error)
	}
	if !strings.Contains(reply.Error, "保存活动设置失败") || !strings.Contains(reply.Error, "重试") {
		t.Fatalf("storage prompt is not actionable: %q", reply.Error)
	}
	for _, leak := range []string{rest26FakeSecret, "dsn=", "10.9.8.7", "3306", "dial tcp", "access denied", "db.internal"} {
		if strings.Contains(reply.Error, leak) {
			t.Fatalf("storage detail leaked through the private API: %q", reply.Error)
		}
	}
	if reply.Activities != nil {
		t.Fatal("failed save returned a snapshot")
	}
	if got := controls.Snapshot(); !reflect.DeepEqual(before, got) || got.Revision != 0 {
		t.Fatalf("failed save mutated controls: %+v", got)
	}
}

// TestRest26ActivitySaveExhaustedRevisionPromptIsAccurate pins the exhausted
// prompt to what the persisted counter allows: a restarted Controls instance
// reads MaxUint64 back, so telling the operator to restart would be wrong.
func TestRest26ActivitySaveExhaustedRevisionPromptIsAccurate(t *testing.T) {
	ctx := context.Background()
	repo := memory.NewMemoryGroup().Settings
	if err := repo.Save(ctx, repository.SettingsRecord{Scope: onlineevent.ControlsScope, Values: map[string]string{"revision": rest26MaxRevision}}); err != nil {
		t.Fatal(err)
	}
	s, controls := rest26Service(t, repo)
	if controls.Snapshot().Revision != math.MaxUint64 {
		t.Fatalf("seeded revision not loaded: %d", controls.Snapshot().Revision)
	}
	restarted, err := onlineevent.NewControls(ctx, repo, rest26Defs())
	if err != nil {
		t.Fatal(err)
	}
	if restarted.Snapshot().Revision != math.MaxUint64 {
		t.Fatalf("restart did not read the persisted revision back: %d", restarted.Snapshot().Revision)
	}

	before := controls.Snapshot()
	code, reply := rest26Save(t, s, `{"version":1,"action":"save_activities","revision":18446744073709551615,"changes":[{"id":2347,"enabled":false}]}`)
	if code != 200 || reply.OK || reply.Error == "" {
		t.Fatalf("exhausted revision not reported: code=%d body=%+v", code, reply)
	}
	if !strings.Contains(reply.Error, "无法继续保存") || !strings.Contains(reply.Error, "联系管理员") {
		t.Fatalf("exhausted prompt is not actionable: %q", reply.Error)
	}
	if strings.Contains(reply.Error, "重启") {
		t.Fatalf("exhausted prompt asks for a restart that cannot clear a persisted revision: %q", reply.Error)
	}
	if reply.Activities != nil {
		t.Fatal("rejected save returned a snapshot")
	}
	if got := controls.Snapshot(); !reflect.DeepEqual(before, got) {
		t.Fatalf("exhausted save mutated controls: %+v", got)
	}
}
