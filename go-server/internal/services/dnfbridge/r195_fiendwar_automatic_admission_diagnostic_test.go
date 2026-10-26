package dnfbridge

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"testing"
)

func r195FiendWarAutomaticDiagnosticEvents(t *testing.T, logs *bytes.Buffer) []map[string]any {
	t.Helper()
	var events []map[string]any
	for _, line := range bytes.Split(bytes.TrimSpace(logs.Bytes()), []byte("\n")) {
		var event map[string]any
		if err := json.Unmarshal(line, &event); err != nil {
			continue
		}
		if event["kind"] == "game-fiendwar-admission-diagnostic" {
			events = append(events, event)
		}
	}
	return events
}

func TestR195FiendWarAutomaticAdmissionDiagnosticDedupesPendingSignature(t *testing.T) {
	var logs bytes.Buffer
	s := &Service{logger: slog.New(slog.NewJSONHandler(&logs, nil))}
	p := &gameSession{selectedCharacterID: 19, connID: "game-19", townSceneReadyCharacterID: 19}
	p.townPositionSnapshot = currentTownPositionSnapshot{CharacterID: 19, TownID: 98, AreaID: 30}
	p.fiendWarPendingMove = &currentFiendWarMovePlan{
		instance:  "battle-19",
		character: 19,
		from:      29,
		to:        30,
		confirmed: true,
	}
	diagnostic := newCurrentFiendWarAdmissionDiagnostic(p, 0)
	diagnostic.phase = currentFiendWarAdmissionPhaseTownArea
	diagnostic.reason = "source_position_invalid"

	s.logCurrentFiendWarAutomaticAdmissionDiagnostic(p, diagnostic)
	s.logCurrentFiendWarAutomaticAdmissionDiagnostic(p, diagnostic)
	events := r195FiendWarAutomaticDiagnosticEvents(t, &logs)
	if len(events) != 1 {
		t.Fatalf("repeated pending signature emitted %d diagnostics: %s", len(events), logs.String())
	}
	event := events[0]
	for key, want := range map[string]any{
		"phase": currentFiendWarAdmissionPhaseTownArea, "pending_instance": "battle-19",
		"pending_from": float64(29), "pending_to": float64(30), "pending_confirmed": true,
	} {
		if event[key] != want {
			t.Fatalf("diagnostic %s=%v want=%v event=%v", key, event[key], want, event)
		}
	}

	// Packet phase is context, not part of the pending-state identity.
	diagnostic.phase = currentFiendWarAdmissionPhaseTownPosition
	s.logCurrentFiendWarAutomaticAdmissionDiagnostic(p, diagnostic)
	if events = r195FiendWarAutomaticDiagnosticEvents(t, &logs); len(events) != 1 {
		t.Fatalf("op35 phase change emitted duplicate diagnostic: %s", logs.String())
	}

	diagnostic.reason = "source_scene_not_ready"
	s.logCurrentFiendWarAutomaticAdmissionDiagnostic(p, diagnostic)
	if events = r195FiendWarAutomaticDiagnosticEvents(t, &logs); len(events) != 2 {
		t.Fatalf("reason change did not emit a new diagnostic: %s", logs.String())
	}

	diagnostic.pendingTo = 31
	diagnostic.reason = "source_not_target"
	s.logCurrentFiendWarAutomaticAdmissionDiagnostic(p, diagnostic)
	if events = r195FiendWarAutomaticDiagnosticEvents(t, &logs); len(events) != 3 {
		t.Fatalf("pending target change did not emit a new diagnostic: %s", logs.String())
	}
}

func TestR195FiendWarAutomaticAdmissionDiagnosticRequiresPendingMove(t *testing.T) {
	var logs bytes.Buffer
	s := &Service{logger: slog.New(slog.NewJSONHandler(&logs, nil))}
	p := &gameSession{selectedCharacterID: 19, townSceneReadyCharacterID: 19}
	p.channel.Type = 42
	p.townPositionSnapshot = currentTownPositionSnapshot{CharacterID: 19, TownID: 98, AreaID: 30}
	diagnostic := newCurrentFiendWarAdmissionDiagnostic(p, 0)
	diagnostic.phase = currentFiendWarAdmissionPhaseTownPosition
	diagnostic.reason = "source_position_invalid"

	s.logCurrentFiendWarAutomaticAdmissionDiagnostic(p, diagnostic)
	if logs.Len() != 0 {
		t.Fatalf("non-pending queue path emitted diagnostics: %s", logs.String())
	}
	if s.packetLog != nil {
		t.Fatal("automatic diagnostic unexpectedly depends on packet logging")
	}
	if _, ok := currentFiendWarQueuedAdmissionDiagnostic(p, currentFiendWarAdmissionPhaseTownPosition); ok {
		t.Fatal("session without a pending move was eligible for automatic diagnostics")
	}

	p.fiendWarPendingMove = &currentFiendWarMovePlan{instance: "battle-other", character: 1048, from: 29, to: 30}
	if _, ok := currentFiendWarQueuedAdmissionDiagnostic(p, currentFiendWarAdmissionPhaseTownPosition); ok {
		t.Fatal("another character's pending move was eligible for automatic diagnostics")
	}
	p.fiendWarPendingMove = &currentFiendWarMovePlan{instance: "battle-19", character: 19, from: 29, to: 30}
	p.dungeon.runtime = &runtimeDungeonState{}
	if _, ok := currentFiendWarQueuedAdmissionDiagnostic(p, currentFiendWarAdmissionPhaseTownPosition); ok {
		t.Fatal("already-entered session was eligible for automatic diagnostics")
	}
}
