package dnfbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"testing"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	r "longheng.io/server/internal/modules/dnf/repository"
)

func captureWeddingDiagnostics(s *Service) *bytes.Buffer {
	logs := &bytes.Buffer{}
	s.packetLog = nil
	s.logger = slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelInfo}))
	return logs
}

func weddingDiagnosticRecords(t *testing.T, logs *bytes.Buffer, event string) []map[string]any {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(logs.Bytes()))
	var records []map[string]any
	for {
		var row map[string]any
		if err := decoder.Decode(&row); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		if row["event"] != event {
			continue
		}
		if row["level"] != "INFO" {
			t.Fatalf("diagnostic is not INFO: %v", row)
		}
		for _, key := range []string{"conn_id", "channel_id", "char_id", "generation", "bound", "selected_char_id", "town_id", "area_id", "scene_ready_char_id", "position_valid"} {
			if _, exists := row[key]; !exists {
				t.Fatalf("missing identity field %s: %v", key, row)
			}
		}
		for _, key := range []string{"account", "account_id", "name", "nonce", "raw", "body", "error"} {
			if _, exists := row[key]; exists {
				t.Fatalf("private field %s in diagnostic: %v", key, row)
			}
		}
		records = append(records, row)
	}
	return records
}

func assertWeddingCancellationDiagnostic(t *testing.T, logs *bytes.Buffer, reason string, result int32, ackSent bool) {
	t.Helper()
	rows := weddingDiagnosticRecords(t, logs, "game-wedding-cancellation-result")
	if len(rows) != 1 || rows[0]["reason"] != reason || rows[0]["result"] != float64(result) ||
		rows[0]["ack_sent"] != ackSent || rows[0]["committed"] != (reason == "committed") || rows[0]["op"] != float64(5) {
		t.Fatalf("cancel diagnostic want %s/%d/ack=%t: %v", reason, result, ackSent, rows)
	}
	if reason == "npc_scene" && (rows[0]["scene_ready_char_id"] != float64(0) || rows[0]["position_valid"] != true) {
		t.Fatalf("scene readiness diagnostic %v", rows)
	}
}

type weddingCancellationPersistedSnapshot struct {
	characters []r.CharacterRecord
	quests     []r.QuestRecord
}

func captureWeddingCancellationState(t *testing.T, repos r.Group, peers []*gameSession) weddingCancellationPersistedSnapshot {
	t.Helper()
	var state weddingCancellationPersistedSnapshot
	for _, p := range peers {
		id := fmt.Sprint(p.selectedCharacterID)
		c, found, err := repos.Character.Load(context.Background(), id)
		if err != nil || !found {
			t.Fatalf("load character %s: found=%t err=%v", id, found, err)
		}
		q, found, err := repos.Quest.Load(context.Background(), id)
		if err != nil || !found {
			t.Fatalf("load quest %s: found=%t err=%v", id, found, err)
		}
		state.characters = append(state.characters, r.CloneCharacter(c))
		state.quests = append(state.quests, r.CloneQuest(q))
	}
	return state
}

func TestWeddingRequestDiagnosticWithoutPacketLogger(t *testing.T) {
	for _, op := range []byte{5, 6, 7} {
		t.Run(fmt.Sprint(op), func(t *testing.T) {
			s := &Service{options: options{gameUpperHeader: gameUpperHeaderChannel13, gameUpperBodyCodec: gameUpperBodyCodecPlain}}
			logs := captureWeddingDiagnostics(s)
			p := &gameSession{conn: &bufferConn{}, connID: "wedding-diagnostic", channel: channelcatalog.Channel{ID: 10}}
			// A selected character on an unbound auxiliary/stale transport must
			// remain distinguishable from its absent canonical binding.
			p.selectedCharacterID = 9
			body := bytes.Repeat([]byte{0xa5}, 20)
			for i := 13; i < len(body); i++ {
				body[i] = 0
			}
			body[17] = op
			if err := s.handleCurrentWeddingRequest(p, body); err != nil {
				t.Fatal(err)
			}
			rows := weddingDiagnosticRecords(t, logs, "game-wedding-request-received")
			if len(rows) != 1 || rows[0]["op"] != float64(op) || rows[0]["body_len"] != float64(20) ||
				rows[0]["decoded"] != true || rows[0]["target"] != float64(0) || rows[0]["kind"] != float64(0) || rows[0]["duration"] != float64(0) ||
				rows[0]["selected_char_id"] != float64(9) || rows[0]["char_id"] != float64(0) || rows[0]["bound"] != false {
				t.Fatalf("request diagnostic %v", rows)
			}
			if op == 5 {
				assertWeddingCancellationDiagnostic(t, logs, "not_bound", -1, true)
			}
		})
	}
	t.Run("decode-failed", func(t *testing.T) {
		s := &Service{options: options{gameUpperHeader: gameUpperHeaderChannel13, gameUpperBodyCodec: gameUpperBodyCodecPlain}}
		logs := captureWeddingDiagnostics(s)
		p := &gameSession{conn: &bufferConn{}, connID: "wedding-invalid"}
		if err := s.handleCurrentWeddingRequest(p, bytes.Repeat([]byte{0xa5}, 19)); err != nil {
			t.Fatal(err)
		}
		rows := weddingDiagnosticRecords(t, logs, "game-wedding-request-received")
		if len(rows) != 1 || rows[0]["body_len"] != float64(19) || rows[0]["decoded"] != false || rows[0]["reason"] != "decode_failed" {
			t.Fatalf("decode diagnostic %v", rows)
		}
		for _, key := range []string{"op", "target", "kind", "duration"} {
			if _, exists := rows[0][key]; exists {
				t.Fatalf("decoded field %s on malformed body", key)
			}
		}
	})
}

type weddingDiagnosticWriteFailure struct{ bufferConn }

func (*weddingDiagnosticWriteFailure) Write([]byte) (int, error) {
	return 0, errors.New("private transport failure")
}

func TestWeddingCancellationDiagnosticReportsFailedACKWithoutRawError(t *testing.T) {
	s := &Service{options: options{gameUpperHeader: gameUpperHeaderChannel13, gameUpperBodyCodec: gameUpperBodyCodecPlain}}
	logs := captureWeddingDiagnostics(s)
	p := &gameSession{conn: &weddingDiagnosticWriteFailure{}, connID: "wedding-write-failure"}
	if err := s.handleCurrentWeddingCancellation(p, currentWeddingRequest{Operation: 5}); err == nil {
		t.Fatal("failed ACK lost its transport error")
	}
	assertWeddingCancellationDiagnostic(t, logs, "not_bound", -1, false)
	if bytes.Contains(logs.Bytes(), []byte("private transport failure")) {
		t.Fatal("raw transport error leaked into INFO diagnostic")
	}
}
