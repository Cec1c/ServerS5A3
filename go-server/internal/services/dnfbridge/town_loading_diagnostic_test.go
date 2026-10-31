package dnfbridge

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func townLoadingDiagnosticEvents(t *testing.T, logs *bytes.Buffer) []map[string]any {
	t.Helper()
	var events []map[string]any
	for _, line := range bytes.Split(logs.Bytes(), []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		var event map[string]any
		if err := json.Unmarshal(line, &event); err != nil {
			t.Fatal(err)
		}
		if event["msg"] == "dnfbridge town loading diagnostic" {
			events = append(events, event)
		}
	}
	return events
}

func townLoadingDiagnosticEvent(t *testing.T, logs *bytes.Buffer, kind string) map[string]any {
	t.Helper()
	for _, event := range townLoadingDiagnosticEvents(t, logs) {
		if event["kind"] == kind {
			return event
		}
	}
	t.Fatalf("missing town loading diagnostic %s in %s", kind, logs.String())
	return nil
}

func TestTownLoadingDiagnosticKeepsOnlyBoundedTownEventsWithoutRawLogging(t *testing.T) {
	var logs bytes.Buffer
	service := &Service{logger: slog.New(slog.NewJSONHandler(&logs, nil))}
	if service.packetLog != nil {
		t.Fatal("test requires raw packet logging to be disabled")
	}

	kinds := []string{
		"game-town-copresence-actor-state-send",
		"game-town-copresence-actor-state-delivered",
		"game-main-finish-loading-state-send",
		"game-town-set-user-area-committed",
		"game-town-presence-published",
	}
	for _, kind := range kinds {
		service.logPacketEvent(kind,
			"conn_id", "r195-town-loading-receiver",
			"channel_id", 118,
			"source", "r195-town-loading-fixture",
			"target_char_id", 29,
			"actor_char_id", 17,
			"actor_channel_id", 118,
			"char_id", 17,
			"town_id", 38,
			"area_id", 2,
			"same_area_peers", 1,
			"mode0_body_len", 123,
			"mode1_body_len", 456,
			"body_len", 789,
			"construction_us", 11,
			"send_us", 7,
			"body", "PRIVATE_PACKET_BODY",
			"account_id", "PRIVATE_ACCOUNT",
			"error", errors.New("PRIVATE_RAW_ERROR"),
			"position_x", 321,
			"map_path", "PRIVATE_MAP_PATH",
		)
	}

	for _, kind := range kinds {
		event := townLoadingDiagnosticEvent(t, &logs, kind)
		if event["conn_id"] != "r195-town-loading-receiver" || event["channel_id"] != float64(118) ||
			event["target_char_id"] != float64(29) || event["actor_char_id"] != float64(17) ||
			event["actor_channel_id"] != float64(118) || event["char_id"] != float64(17) {
			t.Fatalf("%s lost receiver/actor direction: %v", kind, event)
		}
		for _, key := range []string{"construction_us", "send_us"} {
			elapsed, ok := event[key].(float64)
			if !ok || elapsed < 0 {
				t.Fatalf("%s has invalid %s=%v", kind, key, event[key])
			}
		}
		for _, key := range []string{"body", "account_id", "error", "position_x", "map_path"} {
			if _, exists := event[key]; exists {
				t.Fatalf("%s forwarded private or unbounded field %q: %v", kind, key, event)
			}
		}
	}
	if strings.Contains(logs.String(), "PRIVATE_") {
		t.Fatal("town loading diagnostic forwarded a private payload or error")
	}
}

func TestTownLoadingDiagnosticDropsEveryFramePositionWithoutRawLogging(t *testing.T) {
	var logs bytes.Buffer
	service := &Service{logger: slog.New(slog.NewJSONHandler(&logs, nil))}
	service.logPacketEvent("game-town-set-user-position-captured",
		"conn_id", "r195-town-position",
		"channel_id", 118,
		"char_id", 29,
		"town_id", 38,
		"area_id", 2,
		"position_x", 900,
		"position_y", 250,
	)
	if events := townLoadingDiagnosticEvents(t, &logs); len(events) != 0 {
		t.Fatalf("per-frame position event produced town loading diagnostics: %v", events)
	}
}

func TestTownLoadingDiagnosticReportsDeliveredOnlyAfterSuccessfulActorPair(t *testing.T) {
	fixture := newTownResendFixture(t)
	fixture.local.connID = "town-loading-local"
	var logs bytes.Buffer
	fixture.service.logger = slog.New(slog.NewJSONHandler(&logs, nil))
	if fixture.service.packetLog != nil {
		t.Fatal("test requires raw packet logging to be disabled")
	}

	repos, _ := fixture.service.repositoryGroup()
	readFailure := errors.New("PRIVATE_REMOTE_ACTOR_EQUIPMENT_READ")
	fault := &remoteActorSnapshotFaultEquipment{
		EquipmentRepository: repos.Equipment,
		failFirst:           true,
		err:                 readFailure,
	}
	repos.Equipment = fault
	fixture.service.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }
	peer, ok := fixture.service.onlinePlayers.PlayerForCharacter(17)
	if !ok {
		t.Fatal("town peer presence missing from fixture")
	}

	if err := fixture.service.sendTownRemoteActorState(fixture.local, peer, "town_loading_read_failure"); !errors.Is(err, readFailure) {
		t.Fatalf("failed snapshot returned %v, want repository error", err)
	}
	if events := townLoadingDiagnosticEvents(t, &logs); len(events) != 0 {
		t.Fatalf("failed actor construction reported town loading delivery: %v", events)
	}
	if fixture.localBuf.write.Len() != 0 || fixture.peerBuf.write.Len() != 0 {
		t.Fatal("failed actor construction wrote actor frames")
	}

	fault.loads = nil
	fault.failFirst = false
	logs.Reset()
	if err := fixture.service.sendTownRemoteActorState(fixture.local, peer, "town_loading_success"); err != nil {
		t.Fatalf("successful actor construction: %v", err)
	}
	events := townLoadingDiagnosticEvents(t, &logs)
	if len(events) != 2 || events[0]["kind"] != "game-town-copresence-actor-state-send" ||
		events[1]["kind"] != "game-town-copresence-actor-state-delivered" {
		t.Fatalf("successful actor events=%v, want one send followed by one delivered", events)
	}
	delivered := events[1]
	if delivered["target_char_id"] != float64(29) || delivered["actor_char_id"] != float64(17) ||
		delivered["conn_id"] != "town-loading-local" || delivered["source"] != "town_loading_success" {
		t.Fatalf("delivered event lost receiver/actor direction: %v", delivered)
	}
	for _, key := range []string{"construction_us", "send_us"} {
		elapsed, ok := delivered[key].(float64)
		if !ok || elapsed < 0 {
			t.Fatalf("delivered event has invalid %s=%v", key, delivered[key])
		}
	}
	if fixture.localBuf.write.Len() == 0 || fixture.peerBuf.write.Len() != 0 {
		t.Fatalf("successful delivery bytes local=%d peer=%d", fixture.localBuf.write.Len(), fixture.peerBuf.write.Len())
	}
}
