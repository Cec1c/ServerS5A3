package dnfbridge

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

const empireTeamSetupDiagnosticPrivate = "EMPIRE_TEAM_SETUP_PRIVATE"

func empireTeamSetupDiagnosticEvents(t *testing.T, logs *bytes.Buffer) map[string]map[string]any {
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
		if event["msg"] != "dnfbridge empire team setup diagnostic" {
			continue
		}
		kind, _ := event["kind"].(string)
		if events[kind] != nil {
			t.Fatalf("duplicate empire team setup diagnostic %q", kind)
		}
		events[kind] = event
	}
	return events
}

func TestEmpireTeamSetupDiagnosticTracksRealRosterRequestWithoutPacketLog(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		s, session, _ := newTagRosterBridgeTest(t)
		if s.packetLog != nil {
			t.Fatal("test requires packet logging to be disabled")
		}
		session.connID = "game-empire-team-setup-success"
		var logs bytes.Buffer
		s.logger = slog.New(slog.NewJSONHandler(&logs, nil))
		conn := session.conn.(*bufferConn)
		conn.write.Reset()

		if err := s.handleCurrentTagTournamentRosterList(session, nil); err != nil {
			t.Fatal(err)
		}
		packet, rest := splitCurrentGameServerUpperPacketAuto(t, conn.write.Bytes())
		if packet.Header.MsgID != currentTagTournamentCountsNotification || packet.Header.Classification != 0 || !bytes.Equal(packet.Body, []byte{0}) || len(rest) != 0 {
			t.Fatalf("roster response changed: packet=%+v body=%x rest=%x", packet.Header, packet.Body, rest)
		}

		events := empireTeamSetupDiagnosticEvents(t, &logs)
		received := events["帝国竞技场队伍框资料请求已收到"]
		if received["conn_id"] != session.connID || received["channel_id"] != float64(session.channel.ID) || received["body_len"] != float64(0) {
			t.Fatalf("successful request diagnostic = %v", received)
		}
		opened := events["帝国竞技场应组队设置请求打开队伍框"]
		if opened["conn_id"] != session.connID || opened["channel_id"] != float64(session.channel.ID) || opened["count"] != float64(3) || opened["other_servers"] != float64(0) {
			t.Fatalf("successful result diagnostic = %v", opened)
		}
		if events["帝国竞技场队伍框资料请求已忽略"] != nil {
			t.Fatal("successful roster request was diagnosed as ignored")
		}
	})

	t.Run("non-empty-body-rejected", func(t *testing.T) {
		s, session, _ := newTagRosterBridgeTest(t)
		if s.packetLog != nil {
			t.Fatal("test requires packet logging to be disabled")
		}
		session.connID = "game-empire-team-setup-rejected"
		var logs bytes.Buffer
		s.logger = slog.New(slog.NewJSONHandler(&logs, nil))
		conn := session.conn.(*bufferConn)
		conn.write.Reset()
		body := []byte(empireTeamSetupDiagnosticPrivate + "_BODY")

		if err := s.handleCurrentTagTournamentRosterList(session, body); err != nil {
			t.Fatal(err)
		}
		if conn.write.Len() != 0 {
			t.Fatalf("rejected roster request wrote %x", conn.write.Bytes())
		}

		events := empireTeamSetupDiagnosticEvents(t, &logs)
		received := events["帝国竞技场队伍框资料请求已收到"]
		if received["body_len"] != float64(len(body)) {
			t.Fatalf("received diagnostic = %v", received)
		}
		ignored := events["帝国竞技场队伍框资料请求已忽略"]
		if ignored["body_len"] != float64(len(body)) || ignored["reason"] != "角色未就绪或请求正文无效" {
			t.Fatalf("rejection diagnostic = %v", ignored)
		}
		if events["帝国竞技场应组队设置请求打开队伍框"] != nil {
			t.Fatal("rejected roster request was diagnosed as opened")
		}
		if strings.Contains(logs.String(), string(body)) {
			t.Fatal("roster diagnostic retained request body")
		}
	})
}

func TestEmpireTeamSetupDiagnosticWhitelistDropsPrivateContentAndUnmatchedKinds(t *testing.T) {
	var logs bytes.Buffer
	service := &Service{logger: slog.New(slog.NewJSONHandler(&logs, nil))}
	if service.packetLog != nil {
		t.Fatal("test requires packet logging to be disabled")
	}
	privateBody := empireTeamSetupDiagnosticPrivate + "_BODY"
	service.logPacketEvent("帝国竞技场队伍框资料请求已收到",
		"conn_id", "game-empire-team-setup-whitelist",
		"channel_id", byte(88),
		"body_len", len(privateBody),
		"account", empireTeamSetupDiagnosticPrivate+"_ACCOUNT",
		"body", privateBody,
		"token", empireTeamSetupDiagnosticPrivate+"_TOKEN",
		"error", errors.New(empireTeamSetupDiagnosticPrivate+"_ERROR"),
		"reason", empireTeamSetupDiagnosticPrivate+"_REASON",
		"stage", empireTeamSetupDiagnosticPrivate+"_STAGE",
		"rejection", empireTeamSetupDiagnosticPrivate+"_REJECTION",
		"unlisted", empireTeamSetupDiagnosticPrivate+"_UNLISTED",
		"dangling")

	event := empireTeamSetupDiagnosticEvents(t, &logs)["帝国竞技场队伍框资料请求已收到"]
	if event["conn_id"] != "game-empire-team-setup-whitelist" || event["channel_id"] != float64(88) || event["body_len"] != float64(len(privateBody)) || event["failed"] != true {
		t.Fatalf("bounded diagnostic fields = %v", event)
	}
	for _, key := range []string{"account", "body", "token", "error", "reason", "stage", "rejection", "unlisted"} {
		if _, found := event[key]; found {
			t.Fatalf("diagnostic retained non-whitelisted field %q: %v", key, event)
		}
	}
	if strings.Contains(logs.String(), empireTeamSetupDiagnosticPrivate) {
		t.Fatalf("diagnostic leaked private content: %s", logs.String())
	}

	logs.Reset()
	service.logPacketEvent("game-unrelated-noise", "reason", empireTeamSetupDiagnosticPrivate+"_REASON")
	if logs.Len() != 0 {
		t.Fatalf("unmatched kind wrote diagnostic: %s", logs.String())
	}
}

func TestEmpireRosterRejectionDiagnosticIdentifiesFailedValidation(t *testing.T) {
	for _, tc := range []struct {
		name, stage, rejection string
		body                   []byte
	}{
		{"short", "decode", "other", []byte{0}},
		{"server", "selector", "other", []byte{1, 0, 1, 2}},
		{"same", "current_member", "other", []byte{0, 2, 0, 2}},
		{"level", "register_transaction", "level", []byte{0, 1, 0, 2}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, p, _ := newTagRosterBridgeTest(t)
			var logs bytes.Buffer
			s.logger = slog.New(slog.NewJSONHandler(&logs, nil))
			if err := s.handleCurrentTagTournamentSetRoster(p, tc.body); err != nil {
				t.Fatal(err)
			}
			event := empireTeamSetupDiagnosticEvents(t, &logs)["帝国竞技场队员设置已拒绝，未扣费"]
			if event["stage"] != tc.stage || event["rejection"] != tc.rejection || event["body_len"] != float64(len(tc.body)) {
				t.Fatalf("wrong rejection diagnostic: %v", event)
			}
			packet, rest := splitCurrentGameServerUpperPacketAuto(t, p.conn.(*bufferConn).write.Bytes())
			if packet.Header.MsgID != 1457 || !bytes.Equal(packet.Body, []byte{0, 3}) || len(rest) != 0 {
				t.Fatal("diagnostic changed failure reply", packet, rest)
			}
		})
	}
	if currentEmpireRosterRejection(errors.New(empireTeamSetupDiagnosticPrivate)) != "other" {
		t.Fatal("unknown error was not redacted")
	}
}
