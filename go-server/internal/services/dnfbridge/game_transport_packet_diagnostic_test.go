package dnfbridge

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"
)

func TestGameTransportFailureIdentifiesInnerOpcodeWithoutPayload(t *testing.T) {
	left, right := net.Pipe()
	t.Cleanup(func() { _ = left.Close(); _ = right.Close() })
	var logs bytes.Buffer
	s := &Service{logger: slog.New(slog.NewJSONHandler(&logs, nil))}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	p := &gameSession{conn: left, connID: "packet-diagnostic-fixture", ctx: ctx, accountID: "PRIVATE_ACCOUNT"}
	done := make(chan error, 1)
	go func() { _, err := io.ReadFull(right, make([]byte, 3)); done <- err }()
	if err := s.sendGameUpperRawClass(p, 600, []byte("PRIVATE_PAYLOAD"), 0); err == nil {
		t.Fatal("partial write succeeded")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	text := logs.String()
	for _, field := range []string{`"inner_msg_id":600`, `"inner_class":0`, `"written_bytes":3`} {
		if !strings.Contains(text, field) {
			t.Fatalf("missing %s from %s", field, text)
		}
	}
	if strings.Contains(text, "PRIVATE_") || strings.Count(text, "dnfbridge first game transport failure") != 1 {
		t.Fatalf("unbounded/private diagnostic: %s", text)
	}
}
