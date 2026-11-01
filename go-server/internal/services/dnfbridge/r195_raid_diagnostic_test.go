package dnfbridge

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/worldmap"
)

func r195DiagnosticEvent(t *testing.T, logs *bytes.Buffer, kind string) map[string]any {
	t.Helper()
	var found map[string]any
	for _, line := range bytes.Split(logs.Bytes(), []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		var event map[string]any
		if err := json.Unmarshal(line, &event); err != nil {
			t.Fatal(err)
		}
		if event["kind"] == kind {
			found = event
		}
	}
	if found == nil {
		t.Fatalf("missing %s in %s", kind, logs.String())
	}
	return found
}

func TestR195RaidReturnDiagnosticDistinguishesDetachedAndUnsettledRuntime(t *testing.T) {
	for _, hasRuntime := range []bool{false, true} {
		t.Run(map[bool]string{false: "detached", true: "unsettled"}[hasRuntime], func(t *testing.T) {
			var logs bytes.Buffer
			s := &Service{logger: slog.New(slog.NewJSONHandler(&logs, nil))}
			p := &gameSession{selectedCharacterID: 1047, conn: &bufferConn{}}
			if hasRuntime {
				p.dungeon.runtime = &runtimeDungeonState{
					Dungeon:   worldmap.Dungeon{ID: 5061},
					raidEntry: &currentRaidDungeonEntry{RunID: "run-current", InstanceID: "instance-current", Phase: 1, Attempt: 3},
				}
			}
			before := p.dungeon.runtime
			if err := s.handleDungeonEplpCommand(p, []byte{1, 2}); err != nil {
				t.Fatal(err)
			}
			event := r195DiagnosticEvent(t, &logs, "game-dungeon-card-exit-blocked")
			if event["has_runtime"] != hasRuntime || event["request_value_a"] != float64(1) || event["request_value_b"] != float64(2) || event["reason"] != "settlement_clear_reward_not_sent" {
				t.Fatalf("ambiguous exit diagnostic: %v", event)
			}
			if hasRuntime && (event["dungeon_id"] != float64(5061) || event["run_id"] != "run-current" || event["entry_phase"] != float64(1) || event["entry_attempt"] != float64(3)) {
				t.Fatalf("missing current run identity: %v", event)
			}
			if !hasRuntime && event["run_id"] != nil {
				t.Fatalf("detached diagnostic invented a run: %v", event)
			}
			if p.dungeon.runtime != before || p.conn.(*bufferConn).write.Len() != 0 {
				t.Fatal("diagnostic changed exit authorization or sent a packet")
			}
		})
	}
}

func TestR195RaidDiagnosticDoesNotForwardArbitraryErrorOrPacket(t *testing.T) {
	var logs bytes.Buffer
	s := &Service{logger: slog.New(slog.NewJSONHandler(&logs, nil))}
	s.logGameEvent(nil, "game-dungeon-use-coin-blocked", "char_id", uint16(1047),
		"reason", "revive_coin_asset_transaction_failed", "target_object_key", uint16(1047),
		"error", "PRIVATE_REPOSITORY_ERROR", "body", "PRIVATE_PACKET_BODY", "account_id", "PRIVATE_ACCOUNT")
	event := r195DiagnosticEvent(t, &logs, "game-dungeon-use-coin-blocked")
	if event["char_id"] != float64(1047) || event["target_object_key"] != float64(1047) || strings.Contains(logs.String(), "PRIVATE_") {
		t.Fatalf("unexpected diagnostic fields: %s", logs.String())
	}
}
