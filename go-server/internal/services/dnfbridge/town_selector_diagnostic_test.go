package dnfbridge

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type townSelectorDiagnosticFailConn struct {
	bufferConn
	err    error
	failAt int
	writes int
}

func (c *townSelectorDiagnosticFailConn) Write(data []byte) (int, error) {
	c.writes++
	if c.writes == c.failAt {
		return 0, c.err
	}
	return c.bufferConn.Write(data)
}

func townSelectorDiagnosticEvents(t *testing.T, logs *bytes.Buffer) map[string]map[string]any {
	t.Helper()
	events := make(map[string]map[string]any)
	for _, line := range bytes.Split(bytes.TrimSpace(logs.Bytes()), []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		var event map[string]any
		if err := json.Unmarshal(line, &event); err != nil {
			t.Fatal(err)
		}
		if event["msg"] == "dnfbridge town selector diagnostic" {
			kind, _ := event["kind"].(string)
			if events[kind] != nil {
				t.Fatalf("duplicate selector diagnostic %s", kind)
			}
			events[kind] = event
		}
	}
	return events
}

// Use the real selector path with the production default (no packet log).
// Pin both the wire outcome and the distinction between intent and completed
// context writes, including an injected failure at the actual write boundary.
func TestTownSelectorDiagnosticTracksActualSendOutcome(t *testing.T) {
	for _, state := range []string{"ready", "not-ready", "ack-write-failed", "context-write-failed"} {
		t.Run(state, func(t *testing.T) {
			s, session, _ := newTownMoveTest(t)
			if state != "not-ready" {
				if err := s.handleTownSetUserArea(session, buildTownMoveRequest(38, 0, 900, 250, 5)); err != nil {
					t.Fatal(err)
				}
				sendTownSetUserPositionUpper(t, s, session, buildTownPositionRequest(884, 248, 6, 100))
			}
			conn := session.conn.(*bufferConn)
			conn.write.Reset()
			var logs bytes.Buffer
			s.logger = slog.New(slog.NewJSONHandler(&logs, nil))
			if s.packetLog != nil {
				t.Fatal("requires packet log disabled")
			}
			privateErr := errors.New("PRIVATE_SELECTOR_WRITE_ERROR")
			failedWrite := strings.HasSuffix(state, "write-failed")
			if failedWrite {
				failAt := 1
				if state == "context-write-failed" {
					failAt = 3
				}
				failed := &townSelectorDiagnosticFailConn{err: privateErr, failAt: failAt}
				session.conn = failed
				conn = &failed.bufferConn
			}
			err := s.sendEnterSelectDungeonState(session, "test_selector_diagnostic", false, true)
			if failedWrite {
				if !errors.Is(err, privateErr) {
					t.Fatalf("failed write result err=%v bytes=%d", err, conn.write.Len())
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if state == "ack-write-failed" {
				if conn.write.Len() != 0 {
					t.Fatal("failed ack wrote bytes")
				}
			} else {
				ack, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
				if ack.Header.Classification != 1 || ack.Header.MsgID != 15 || !bytes.Equal(ack.Body, []byte{1, 1, 0}) {
					t.Fatalf("selector ack=%+v body=%x", ack.Header, ack.Body)
				}
				fatigue, rest := splitGameServerUpperPacket(t, rest)
				if fatigue.Header.Classification != 0 || fatigue.Header.MsgID != currentFatigueMsgID || len(fatigue.Body) != 10 {
					t.Fatalf("fatigue=%+v body=%x", fatigue.Header, fatigue.Body)
				}
				if state == "ready" {
					context, trailing := splitGameServerUpperPacket(t, rest)
					if context.Header.Classification != 0 || context.Header.MsgID != currentDungeonContextMsgID || len(context.Body) != 37 || len(trailing) != 0 {
						t.Fatalf("context=%+v body=%x trailing=%x", context.Header, context.Body, trailing)
					}
				} else if len(rest) != 0 {
					t.Fatalf("unready selector emitted extra bytes %x", rest)
				}
			}
			events := townSelectorDiagnosticEvents(t, &logs)
			before := events["game-enter-select-dungeon-state-send"]
			if before["phase"] != "before_selector_ack" || before["town_context_ready"] != (state != "not-ready") || before["request_driven"] != true {
				t.Fatalf("wrong pre-write diagnostic %v", before)
			}
			written := events["game-upper-town-enter-select-origin-bound"]
			if state == "ready" {
				if written["phase"] != "context_write_returned" || written["origin_bound"] != true || written["town_id"] != float64(38) || !session.enterSelectDungeonContextSent {
					t.Fatalf("missing successful context write evidence %v", written)
				}
			} else if written != nil || session.enterSelectDungeonContextSent || session.townSelectorOriginBound {
				t.Fatalf("unsent context reported as successful: %v", written)
			}
			if state == "not-ready" {
				deferred := events["game-upper-town-enter-select-context-deferred"]
				if deferred["phase"] != "context_not_ready" || deferred["reason"] != "town_scene_location_owner_missing" {
					t.Fatalf("missing readiness reason %v", deferred)
				}
			}
			if strings.Contains(logs.String(), privateErr.Error()) {
				t.Fatal("diagnostic retained raw private error")
			}
		})
	}
}

func TestTownSelectorDiagnosticExplainsSuppressedReturnRequest(t *testing.T) {
	s, session, _ := newTownMoveTest(t)
	session.backToVillageEnterSelectPending = true
	session.backToVillageEnterSelectArmedAt = time.Now()
	var logs bytes.Buffer
	s.logger = slog.New(slog.NewJSONHandler(&logs, nil))
	if err := s.sendEnterSelectDungeonState(session, "test_return_finalizer", false, true); err != nil {
		t.Fatal(err)
	}
	if session.conn.(*bufferConn).write.Len() != 0 || session.enterSelectDungeonAckSent {
		t.Fatal("suppressed request changed wire behavior")
	}
	events := townSelectorDiagnosticEvents(t, &logs)
	event := events["game-upper-enter-select-dungeon-after-back-to-village-suppressed"]
	if event["phase"] != "request_suppressed" || event["response"] != "none" || event["reason"] != "current_exe_op132_scene_finalizer_op15_is_not_selector_intent" {
		t.Fatalf("missing suppression reason %v", event)
	}
}

func TestTownSelectorDiagnosticExplainsUnreadyFollower(t *testing.T) {
	_, s, _, member, _, conn := newTownCoPresenceTestService(t)
	storeRuntimePartyState(member, townCoPresencePartyState())
	member.townMu.Lock()
	member.townSceneReadyCharacterID = 0
	member.townMu.Unlock()
	conn.write.Reset()
	var logs bytes.Buffer
	s.logger = slog.New(slog.NewJSONHandler(&logs, nil))
	prepared, err := s.prepareRuntimePartyFollowerEnterSelect(member, 1001, 77, "test_follower_not_ready")
	if err != nil || prepared || conn.write.Len() != 0 {
		t.Fatalf("unready follower result prepared=%t err=%v bytes=%d", prepared, err, conn.write.Len())
	}
	event := townSelectorDiagnosticEvents(t, &logs)["game-party-enter-select-follower-deferred"]
	if event["phase"] != "follower_not_ready" || event["reason"] != "town_scene_player_state_not_finalized" || event["leader_char_id"] != float64(1001) || event["party_id"] != float64(77) {
		t.Fatalf("missing follower decision %v", event)
	}
}

type townSelectorDiagnosticPrivateValue struct{}

func (townSelectorDiagnosticPrivateValue) String() string { panic("must not format arbitrary values") }

func TestTownSelectorDiagnosticDropsPayloadAndUnexpectedValues(t *testing.T) {
	var logs bytes.Buffer
	s := &Service{logger: slog.New(slog.NewJSONHandler(&logs, nil))}
	s.logPacketEvent("game-upper-town-enter-select-context-deferred",
		"conn_id", "game-000001", "char_id", uint16(29), "reason", "town_scene_location_owner_missing",
		"body", []byte("PRIVATE"), "error", errors.New("PRIVATE"), "account_id", "PRIVATE",
		"source", townSelectorDiagnosticPrivateValue{}, "channel_id", "PRIVATE", "prepared", "PRIVATE",
		"member_area", strings.Repeat("x", 257), "source_area", "PRIVATE\nline", "position_x", 884,
		"phase", "forged_success", "dangling")
	event := townSelectorDiagnosticEvents(t, &logs)["game-upper-town-enter-select-context-deferred"]
	if len(event) != 8 || event["char_id"] != float64(29) || event["phase"] != "context_not_ready" || strings.Contains(logs.String(), "PRIVATE") {
		t.Fatalf("unexpected retained fields %v", event)
	}
	logs.Reset()
	s.logPacketEvent("game-unrelated-noise", "reason", "unrelated")
	if logs.Len() != 0 {
		t.Fatal("unrelated packet event acquired a normal-log fallback")
	}
}

func TestTownSelectorDiagnosticDoesNotDuplicateEnabledPacketLog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "packet.log")
	packetLog, err := openPacketLogger(path)
	if err != nil {
		t.Fatal(err)
	}
	defer packetLog.Close()
	var logs bytes.Buffer
	s := &Service{logger: slog.New(slog.NewJSONHandler(&logs, nil)), packetLog: packetLog}
	kind := "game-upper-town-enter-select-context-deferred"
	s.logPacketEvent(kind, "reason", "town_scene_location_owner_missing")
	if logs.Len() != 0 {
		t.Fatal("enabled packet logger also wrote the fallback")
	}
	if err := packetLog.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || !bytes.Contains(data, []byte(kind)) {
		t.Fatalf("original packet event missing: %v %s", err, data)
	}
}
