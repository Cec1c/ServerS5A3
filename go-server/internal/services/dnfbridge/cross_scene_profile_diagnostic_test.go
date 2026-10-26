package dnfbridge

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

const crossSceneProfileDiagnosticPrivate = "PRIVATE_CROSS_SCENE_PROFILE"

type crossSceneProfileDiagnosticFailConn struct {
	bufferConn
	err    error
	writes int
}

func (c *crossSceneProfileDiagnosticFailConn) Write([]byte) (int, error) {
	c.writes++
	return 0, c.err
}

// The reported refresh runs with raw packet logging disabled. Exercise the
// real profile batch so the normal diagnostic still carries enough identity
// and size evidence to explain the refresh without retaining the payload.
func TestCrossSceneProfileRefreshDiagnosticsSurviveDisabledPacketLog(t *testing.T) {
	t.Run("sent", func(t *testing.T) {
		f := newCurrentCrossSceneProfileBatchFixture(t)
		f.reset()
		f.target.connID = "cross-scene-profile-diagnostic-sent"
		var logs bytes.Buffer
		f.service.logger = slog.New(slog.NewJSONHandler(&logs, nil))
		if f.service.packetLog != nil {
			t.Fatal("test requires packet logging to be disabled")
		}
		owner := currentConnectionTownActorOwnerContext(f.target)
		if owner == 0 {
			t.Fatal("fixture has no connection owner context")
		}

		batch := newCurrentCrossSceneProfileBatch(f.service, context.Background())
		if batch == nil {
			t.Fatal("newCurrentCrossSceneProfileBatch returned nil")
		}
		f.service.sendCurrentCrossSceneProfileRefresh(batch, f.target, f.actor, owner, "party_roster")

		event := r195DiagnosticEvent(t, &logs, "game-peer-userinfo-profile-refresh-sent")
		assertCrossSceneProfileRefreshDiagnosticIdentity(t, event, f.target, f.actor, owner)
		body := requireCurrentCrossSceneProfileBatchMode8(
			t,
			splitUpperFrames(t, f.targetConn.write.Bytes()),
			f.actor.selectedCharacterID,
			owner,
		)
		if event["body_len"] != float64(len(body)) {
			t.Fatalf("sent diagnostic body_len=%v want %d: %v", event["body_len"], len(body), event)
		}
		assertCrossSceneProfileRefreshDiagnosticKeys(t, event)
		assertCrossSceneProfileRefreshDiagnosticNoPrivateContent(t, logs.String())
	})

	t.Run("failed", func(t *testing.T) {
		f := newCurrentCrossSceneProfileBatchFixture(t)
		f.reset()
		f.target.connID = "cross-scene-profile-diagnostic-failed"
		privateErr := errors.New(crossSceneProfileDiagnosticPrivate + "_WRITE_ERROR")
		failedConn := &crossSceneProfileDiagnosticFailConn{err: privateErr}
		f.target.conn = failedConn
		var logs bytes.Buffer
		f.service.logger = slog.New(slog.NewJSONHandler(&logs, nil))
		if f.service.packetLog != nil {
			t.Fatal("test requires packet logging to be disabled")
		}
		owner := currentConnectionTownActorOwnerContext(f.target)
		if owner == 0 {
			t.Fatal("fixture has no connection owner context")
		}

		batch := newCurrentCrossSceneProfileBatch(f.service, context.Background())
		if batch == nil {
			t.Fatal("newCurrentCrossSceneProfileBatch returned nil")
		}
		f.service.sendCurrentCrossSceneProfileRefresh(batch, f.target, f.actor, owner, "equipment_appearance")

		event := r195DiagnosticEvent(t, &logs, "game-peer-userinfo-profile-refresh-failed")
		assertCrossSceneProfileRefreshDiagnosticIdentity(t, event, f.target, f.actor, owner)
		if event["reason"] != "equipment_appearance" {
			t.Fatalf("failed diagnostic lost reason: %v", event)
		}
		if event["error_kind"] != "prepare_or_write" {
			t.Fatalf("failed diagnostic lost bounded failure kind: %v", event)
		}
		if failedConn.writes != 1 || failedConn.write.Len() != 0 || f.targetConn.write.Len() != 0 {
			t.Fatalf("failed diagnostic changed send result: writes=%d failedBytes=%d viewerBytes=%d",
				failedConn.writes, failedConn.write.Len(), f.targetConn.write.Len())
		}
		assertCrossSceneProfileRefreshDiagnosticKeys(t, event)
		assertCrossSceneProfileRefreshDiagnosticNoPrivateContent(t, logs.String())
		if strings.Contains(logs.String(), privateErr.Error()) {
			t.Fatalf("failed diagnostic leaked the write error: %s", logs.String())
		}
	})
}

// Pin the event-specific whitelist directly as well: the two refresh events
// may receive owner, body length and reason fields at different call sites.
// All of those remain useful, while account, payload and error text never do.
func TestCrossSceneProfileRefreshDiagnosticWhitelistDropsPrivateContent(t *testing.T) {
	for _, kind := range []string{
		"game-peer-userinfo-profile-refresh-sent",
		"game-peer-userinfo-profile-refresh-failed",
	} {
		t.Run(kind, func(t *testing.T) {
			service := &Service{}
			var logs bytes.Buffer
			service.logger = slog.New(slog.NewJSONHandler(&logs, nil))
			service.logCrossSceneProfileDiagnostic(kind,
				"conn_id", "cross-scene-profile-whitelist",
				"channel_id", byte(19),
				"source_char_id", uint16(29),
				"target_char_id", uint16(17),
				"owner_context", byte(23),
				"body_len", 4096,
				"reason", "whitelisted_refresh_reason",
				"account", crossSceneProfileDiagnosticPrivate+"_ACCOUNT",
				"account_id", crossSceneProfileDiagnosticPrivate+"_ACCOUNT_ID",
				"body", crossSceneProfileDiagnosticPrivate+"_BODY",
				"error", errors.New(crossSceneProfileDiagnosticPrivate+"_ERROR"),
				"token", crossSceneProfileDiagnosticPrivate+"_TOKEN",
				"char_id", uint16(999),
			)

			event := r195DiagnosticEvent(t, &logs, kind)
			want := map[string]any{
				"conn_id":        "cross-scene-profile-whitelist",
				"channel_id":     float64(19),
				"source_char_id": float64(29),
				"target_char_id": float64(17),
				"owner_context":  float64(23),
				"body_len":       float64(4096),
				"reason":         "whitelisted_refresh_reason",
			}
			for key, value := range want {
				if event[key] != value {
					t.Fatalf("%s lost %s=%v: %v", kind, key, value, event)
				}
			}
			assertCrossSceneProfileRefreshDiagnosticKeys(t, event)
			assertCrossSceneProfileRefreshDiagnosticNoPrivateContent(t, logs.String())
			if event["char_id"] != nil {
				t.Fatalf("%s forwarded a field outside its refresh whitelist: %v", kind, event)
			}
		})
	}
}

func assertCrossSceneProfileRefreshDiagnosticIdentity(
	t *testing.T,
	event map[string]any,
	viewer *gameSession,
	actor *gameSession,
	owner byte,
) {
	t.Helper()
	if event["conn_id"] != viewer.connID ||
		event["channel_id"] != float64(viewer.channel.ID) ||
		event["source_char_id"] != float64(viewer.selectedCharacterID) ||
		event["target_char_id"] != float64(actor.selectedCharacterID) ||
		event["owner_context"] != float64(owner) {
		t.Fatalf("refresh identity fields = %v", event)
	}
}

func assertCrossSceneProfileRefreshDiagnosticKeys(t *testing.T, event map[string]any) {
	t.Helper()
	allowed := map[string]bool{
		"time": true, "level": true, "msg": true, "kind": true,
		"conn_id": true, "channel_id": true,
		"source_char_id": true, "target_char_id": true,
		"owner_context": true, "body_len": true, "reason": true, "error_kind": true,
	}
	for key := range event {
		if !allowed[key] {
			t.Fatalf("refresh diagnostic forwarded non-whitelisted field %q: %v", key, event)
		}
	}
}

func assertCrossSceneProfileRefreshDiagnosticNoPrivateContent(t *testing.T, logged string) {
	t.Helper()
	for _, value := range []string{
		crossSceneProfileDiagnosticPrivate,
		"dnf:1",
		"dnf:2",
		`"account"`,
		`"account_id"`,
		`"body"`,
		`"error"`,
		`"token"`,
	} {
		if strings.Contains(logged, value) {
			t.Fatalf("refresh diagnostic leaked %q: %s", value, logged)
		}
	}
}
