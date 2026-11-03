package dnfbridge

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

func TestSettlementPlanFailureRetainsBoundedDiagnosticWithoutPacketLog(t *testing.T) {
	for _, kind := range []string{"game-dungeon-settlement-plan-blocked", "game-dungeon-settlement-results-blocked", "game-dungeon-settlement-historical-pickup-verified"} {
		t.Run(kind, func(t *testing.T) {
			var logs bytes.Buffer
			s := &Service{logger: slog.New(slog.NewJSONHandler(&logs, nil))}
			s.logGameEvent(&gameSession{connID: "test-connection"}, kind,
				"failure_stage", "committed_pickup_projection", "settlement_result_plan_frozen", false,
				"reward_granted", false, "error", errors.New("PRIVATE_ERROR"), "body", []byte("PRIVATE_BODY"), "account_id", "PRIVATE_ACCOUNT")
			events := dungeonCardGoldDiagnosticEvents(t, &logs)
			if len(events) != 1 {
				t.Fatalf("events=%v", events)
			}
			if events[0]["failure_stage"] != "committed_pickup_projection" || events[0]["settlement_result_plan_frozen"] != false || events[0]["reward_granted"] != false {
				t.Fatalf("missing state fields: %v", events[0])
			}
			for _, key := range []string{"error", "body", "account_id"} {
				if _, found := events[0][key]; found {
					t.Fatalf("private field %s escaped", key)
				}
			}
			if strings.Contains(logs.String(), "PRIVATE_") {
				t.Fatal("private diagnostic leaked")
			}
		})
	}
}
