package dnfbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"syscall"
	"testing"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
)

type gameReadCloseTestConn struct {
	bufferConn
	err error
}

func (c *gameReadCloseTestConn) Read([]byte) (int, error) {
	return 0, c.err
}

func gameReadCloseEvents(t *testing.T, logs *bytes.Buffer) []map[string]any {
	t.Helper()
	var events []map[string]any
	for _, line := range bytes.Split(bytes.TrimSpace(logs.Bytes()), []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		var event map[string]any
		if err := json.Unmarshal(line, &event); err != nil {
			t.Fatal(err)
		}
		if event["msg"] == "dnfbridge game read close" {
			events = append(events, event)
		}
	}
	return events
}

func TestGameReadCloseSummaryIsBoundedAndAuxiliarySafe(t *testing.T) {
	var logs bytes.Buffer
	canonical := &gameSession{
		conn:                &bufferConn{},
		connID:              "game-main-000001",
		selectedCharacterID: 3033,
	}
	privateErr := fmt.Errorf("read tcp 10.0.0.1:41000->10.0.0.2:10028 PRIVATE_ACCOUNT: %w", syscall.ECONNRESET)
	auxiliary := &gameSession{
		conn:                &gameReadCloseTestConn{err: privateErr},
		connID:              "game-aux-000002",
		channel:             channelcatalog.Channel{ID: 28, Port: 10028},
		selectedCharacterID: 3033,
		characterGeneration: 786,
		accountID:           "PRIVATE_ACCOUNT",
		clientIP:            "10.0.0.1",
		ctx:                 context.Background(),
	}
	s := &Service{
		logger:       slog.New(slog.NewJSONHandler(&logs, nil)),
		gameSessions: map[uint16]*gameSession{3033: canonical},
		liveGameSessions: map[string]*gameSession{
			canonical.connID: canonical,
			auxiliary.connID: auxiliary,
		},
	}

	s.handleGameStreamInput(context.Background(), auxiliary)
	// The per-connection Once is the durable one-record guard for the read loop.
	s.logGameReadClose(auxiliary, 0, "eof")
	events := gameReadCloseEvents(t, &logs)
	if len(events) != 1 {
		t.Fatalf("game-read-close records=%d, want exactly one: %s", len(events), logs.String())
	}
	event := events[0]
	if event["kind"] != "game-read-close" || event["error_kind"] != "connection_reset" ||
		event["socket_scope"] != "connection_terminated" || event["disconnect_root_cause"] != false {
		t.Fatalf("unexpected bounded close evidence: %v", event)
	}
	if event["character_binding"] != "bound" || event["char_id"] != float64(3033) ||
		event["character_generation"] != float64(786) ||
		event["character_session_role"] != "auxiliary" ||
		event["same_character_other_connections"] != float64(1) {
		t.Fatalf("auxiliary close lost the live role context: %v", event)
	}
	for _, forbidden := range []string{"remote", "local", "error", "account_id", "client_ip", "offline"} {
		if _, exists := event[forbidden]; exists {
			t.Fatalf("close summary forwarded forbidden field %q: %v", forbidden, event)
		}
	}
	if strings.Contains(logs.String(), "PRIVATE") || strings.Contains(logs.String(), "10.0.0.") {
		t.Fatalf("close summary leaked private error/address/account data: %s", logs.String())
	}
}

func TestGameReadCloseSummarySkipsLogoutAndContextStop(t *testing.T) {
	for _, tc := range []struct {
		name   string
		logout bool
		cancel bool
	}{
		{name: "explicit_logout", logout: true},
		{name: "context_stop", cancel: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.cancel {
				cancel()
			}
			session := &gameSession{
				conn:                &gameReadCloseTestConn{err: fmt.Errorf("reader shut down: %w", net.ErrClosed)},
				connID:              "game-stop-000003",
				channel:             channelcatalog.Channel{ID: 28, Port: 10028},
				selectedCharacterID: 3033,
				ctx:                 ctx,
			}
			session.loginRevoked.Store(tc.logout)
			s := &Service{logger: slog.New(slog.NewJSONHandler(&logs, nil))}
			s.handleGameStreamInput(ctx, session)
			if events := gameReadCloseEvents(t, &logs); len(events) != 0 {
				t.Fatalf("expected no close summary, got %v", events)
			}
		})
	}
}

func TestGameReadCloseSummarySkipsCharacterSelectReturn(t *testing.T) {
	var logs bytes.Buffer
	session := &gameSession{
		conn:                &gameReadCloseTestConn{err: io.EOF},
		connID:              "game-select-000004",
		channel:             channelcatalog.Channel{ID: 28, Port: 10028},
		selectedCharacterID: 0,
		ctx:                 context.Background(),
	}
	s := &Service{logger: slog.New(slog.NewJSONHandler(&logs, nil))}
	s.handleGameStreamInput(context.Background(), session)
	if events := gameReadCloseEvents(t, &logs); len(events) != 0 {
		t.Fatalf("character-select return close was attributed to a role: %v", events)
	}
}

func TestGameReadCloseErrorKindsDoNotFormatProviderErrors(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{err: nil, want: "reader_exited"},
		{err: fmt.Errorf("private eof: %w", io.EOF), want: "eof"},
		{err: fmt.Errorf("private closed: %w", net.ErrClosed), want: "net_closed"},
		{err: fmt.Errorf("private reset: %w", syscall.ECONNRESET), want: "connection_reset"},
		{err: fmt.Errorf("private cancel: %w", context.Canceled), want: "context_canceled"},
		{err: fmt.Errorf("private deadline: %w", context.DeadlineExceeded), want: "deadline_exceeded"},
		{err: fmt.Errorf("private address and credential: %w", syscall.EPIPE), want: "socket_read_error"},
	} {
		if got := gameReadCloseErrorKind(tc.err); got != tc.want {
			t.Fatalf("error kind=%q want=%q for %T", got, tc.want, tc.err)
		}
	}
}
