package dnfbridge

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

func dungeonCardGoldDiagnosticEvents(t *testing.T, logs *bytes.Buffer) []map[string]any {
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
		if event["msg"] == "dnfbridge dungeon settlement diagnostic" {
			events = append(events, event)
		}
	}
	return events
}

func TestDungeonCardGoldDiagnosticKeepsOnlyBoundedFields(t *testing.T) {
	var logs bytes.Buffer
	service := &Service{logger: slog.New(slog.NewJSONHandler(&logs, nil))}
	service.logDungeonSettlementDiagnostic("game-dungeon-card-reward-plan-frozen",
		"char_id", 17,
		"dungeon_id", 110,
		"free_gold", int64(12345),
		"paid_gold", int64(6789),
		"paid_card_cost", int64(10780),
		"free_item_count", 1,
		"paid_item_count", 2,
		"premium_gold_card_available", true,
		"gold_normal_kills", 12,
		"gold_champion_kills", 3,
		"gold_boss_kills", 1,
		"gold_entry_party_count", 4,
		"gold_difficulty_rate", 1.9,
		"gold_party_rate", 1.6,
		"gold_rank_bonus", 0.2,
		"plan_id", "PRIVATE_PLAN_ID",
		"account_id", "PRIVATE_ACCOUNT",
		"rawpacket", "PRIVATE_RAW_PACKET",
		"body", "PRIVATE_PACKET_BODY",
		"error", errors.New("PRIVATE_RAW_ERROR"),
	)

	events := dungeonCardGoldDiagnosticEvents(t, &logs)
	if len(events) != 1 {
		t.Fatalf("card gold diagnostic events=%d, want exactly one: %s", len(events), logs.String())
	}
	event := events[0]
	if event["kind"] != "game-dungeon-card-reward-plan-frozen" {
		t.Fatalf("unexpected diagnostic kind: %v", event)
	}
	for key, want := range map[string]any{
		"free_gold":                   float64(12345),
		"paid_gold":                   float64(6789),
		"paid_card_cost":              float64(10780),
		"free_item_count":             float64(1),
		"paid_item_count":             float64(2),
		"premium_gold_card_available": true,
		"gold_normal_kills":           float64(12),
		"gold_champion_kills":         float64(3),
		"gold_boss_kills":             float64(1),
		"gold_entry_party_count":      float64(4),
		"gold_difficulty_rate":        1.9,
		"gold_party_rate":             1.6,
		"gold_rank_bonus":             0.2,
	} {
		if got := event[key]; got != want {
			t.Fatalf("diagnostic field %s=%v, want %v: %v", key, got, want, event)
		}
	}
	for _, key := range []string{"plan_id", "account_id", "rawpacket", "error", "body"} {
		if _, exists := event[key]; exists {
			t.Fatalf("diagnostic forwarded forbidden field %q: %v", key, event)
		}
	}
	if strings.Contains(logs.String(), "PRIVATE_") {
		t.Fatal("card gold diagnostic forwarded a private payload or error")
	}
}
