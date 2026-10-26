package dnfbridge

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/dungeoncmd"
)

func r195FiendWarDiagnosticEvent(t *testing.T, logs *bytes.Buffer, reason string) map[string]any {
	t.Helper()
	for _, line := range bytes.Split(bytes.TrimSpace(logs.Bytes()), []byte("\n")) {
		var event map[string]any
		if err := json.Unmarshal(line, &event); err != nil {
			continue
		}
		if event["kind"] == "game-fiendwar-admission-diagnostic" && event["reason"] == reason {
			return event
		}
	}
	t.Fatalf("missing FiendWar diagnostic reason=%s logs=%s", reason, logs.String())
	return nil
}

func TestR195FiendWarAdmissionDiagnosticWritesBoundedStructuredContext(t *testing.T) {
	var logs bytes.Buffer
	s := &Service{logger: slog.New(slog.NewJSONHandler(&logs, nil))}
	p := &gameSession{selectedCharacterID: 19, connID: "game-19", townSceneReadyCharacterID: 19}
	p.townPositionSnapshot = currentTownPositionSnapshot{CharacterID: 19, TownID: 98, AreaID: 31, PositionValid: true}
	diagnostic := newCurrentFiendWarAdmissionDiagnostic(p, 4953)
	diagnostic.reason = "target_dungeon_mismatch"
	diagnostic.raidKey = 32769047
	diagnostic.raidInstanceID = "battle-19"
	diagnostic.memberArea = 31
	diagnostic.memberGroup = 4
	diagnostic.memberAreaOpen = true
	s.logCurrentFiendWarAdmissionDiagnostic(diagnostic)

	event := r195FiendWarDiagnosticEvent(t, &logs, "target_dungeon_mismatch")
	for key, want := range map[string]any{
		"char_id": float64(19), "conn_id": "game-19", "raid_key": float64(32769047),
		"raid_instance_id": "battle-19", "member_area": float64(31), "member_group": float64(4),
		"member_area_open": true, "town_id": float64(98), "town_area": float64(31),
		"position_valid": true, "scene_ready": true, "request_dungeon": float64(4953),
	} {
		if event[key] != want {
			t.Fatalf("diagnostic %s=%v want=%v event=%v", key, event[key], want, event)
		}
	}
	if s.packetLog != nil {
		t.Fatal("diagnostic unexpectedly depends on packet logging")
	}
}

func TestR195FiendWarAdmissionDiagnosticSourceReasons(t *testing.T) {
	p := &gameSession{selectedCharacterID: 19}
	p.townPositionSnapshot = currentTownPositionSnapshot{CharacterID: 19, TownID: 98, AreaID: 31}
	if _, _, _, _, _, reason := currentFiendWarEncounterSourceAreaWithReason(p, 31, 98, "battle"); reason != "source_position_invalid" {
		t.Fatalf("invalid position reason=%s", reason)
	}
	p.townPositionSnapshot.PositionValid = true
	p.townPositionSnapshot.AreaID = 29
	if _, ready, atTarget, _, ok, reason := currentFiendWarEncounterSourceAreaWithReason(p, 31, 98, "battle"); !ok || ready || atTarget || reason != "source_scene_not_ready" {
		t.Fatalf("scene reason=%s ready=%t at_target=%t ok=%t", reason, ready, atTarget, ok)
	}
	p.townSceneReadyCharacterID = 19
	if _, ready, atTarget, _, ok, reason := currentFiendWarEncounterSourceAreaWithReason(p, 31, 98, "battle"); !ok || !ready || atTarget || reason != "source_not_target" {
		t.Fatalf("target reason=%s ready=%t at_target=%t ok=%t", reason, ready, atTarget, ok)
	}
	p.townPositionSnapshot.AreaID = 31
	p.fiendWarPendingMove = &currentFiendWarMovePlan{instance: "battle", character: 19, from: 29, to: 31}
	if source, ready, atTarget, pending, ok, reason := currentFiendWarEncounterSourceAreaWithReason(p, 31, 98, "battle"); !ok || !ready || !atTarget || !pending || source != 29 || reason != "source_ready" {
		t.Fatalf("pending reason=%s source=%d ready=%t at_target=%t pending=%t ok=%t", reason, source, ready, atTarget, pending, ok)
	}
}

func TestR195FiendWarAdmissionDiagnosticCapturesTargetMismatch(t *testing.T) {
	f, peers := r134N1Fixture(t)
	var logs bytes.Buffer
	f.service.logger = slog.New(slog.NewJSONHandler(&logs, nil))
	f.state.Battle.FiendWar.MemberAreas[peers[0].selectedCharacterID] = 29
	handled, err := f.service.interceptCurrentFiendWarNormalSelection(peers[0], 4953)
	if !handled || err != nil {
		t.Fatalf("target mismatch handled=%t err=%v", handled, err)
	}
	event := r195FiendWarDiagnosticEvent(t, &logs, "target_dungeon_mismatch")
	if event["request_dungeon"] != float64(4953) || event["member_area"] != float64(29) {
		t.Fatalf("target mismatch lost context: %v", event)
	}
}

func TestR195FiendWarAdmissionDiagnosticCapturesRealOp16PreAdmissionReject(t *testing.T) {
	f, peers := r134N1Fixture(t)
	var logs bytes.Buffer
	f.service.logger = slog.New(slog.NewJSONHandler(&logs, nil))
	f.state.Battle.PhaseCompleted = true
	request := dungeoncmd.SelectDungeonRequest{DungeonID: 4953}
	if err := f.service.handleDungeonSelectUpperPlannedForAdmission(
		peers[0], encodeCurrentDungeonSelectRequest(request), nil, true, nil, false,
	); err != nil {
		t.Fatal(err)
	}
	event := r195FiendWarDiagnosticEvent(t, &logs, "pre_admission_selection_rejected")
	if event["char_id"] != float64(peers[0].selectedCharacterID) ||
		event["raid_key"] != float64(f.state.RaidKey) ||
		event["raid_instance_id"] != f.state.Battle.InstanceID ||
		event["member_area"] == float64(-1) {
		t.Fatalf("real op16 front gate diagnostic lost raid/member context: %v", event)
	}
}

func TestR195FiendWarAdmissionDiagnosticCapturesClaimBusyWithoutMaintenanceSpam(t *testing.T) {
	f, peers := r134N1Fixture(t)
	s, b := f.service, f.state.Battle
	b.FiendWar.Symbols[b.FiendWar.Config.Symbols["top boss state"]] = 1
	b.FiendWar.Symbols[b.FiendWar.Config.Symbols["bot boss state"]] = 1
	syncCurrentFiendWarDungeons(b)
	late := peers[0]
	r134N1Presence(t, f, late, 31)
	b.FiendWarAdmissionClaim = currentFiendWarAdmissionClaim{
		InstanceID:       b.InstanceID,
		SourceArea:       29,
		TargetArea:       30,
		Participants:     []uint16{peers[1].selectedCharacterID},
		Leader:           peers[1].selectedCharacterID,
		LeaderGeneration: peers[1].characterGeneration,
		TakenAt:          time.Now(),
	}
	diagnostic := newCurrentFiendWarAdmissionDiagnostic(late, 4953)
	if err := s.tryCurrentFiendWarEncounterWithDiagnostic(late, &diagnostic); err != nil {
		t.Fatal(err)
	}
	if diagnostic.reason != "claim_busy" {
		t.Fatalf("claim busy reason=%s", diagnostic.reason)
	}

	var logs bytes.Buffer
	s.logger = slog.New(slog.NewJSONHandler(&logs, nil))
	if err := s.tryCurrentFiendWarEncounter(late); err != nil {
		t.Fatal(err)
	}
	if logs.Len() != 0 {
		t.Fatalf("maintenance retry emitted diagnostics: %s", logs.String())
	}
}
