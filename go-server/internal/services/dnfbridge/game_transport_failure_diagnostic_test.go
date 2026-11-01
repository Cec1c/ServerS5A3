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

func TestFirstGameTransportFailureRetainsPartialWriteWithoutPacketLog(t *testing.T) {
	left, right := net.Pipe()
	defer left.Close()
	defer right.Close()
	var logs bytes.Buffer
	s := &Service{logger: slog.New(slog.NewJSONHandler(&logs, nil))}
	p := &gameSession{conn: left, connID: "partial-write-fixture", accountID: "PRIVATE_ACCOUNT"}
	read := make(chan error, 1)
	go func() { _, err := io.ReadFull(right, make([]byte, 3)); read <- err }()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	p.wireMu.Lock()
	finish, err := beginGameWriteBudgetLocked(p, ctx)
	if err != nil {
		t.Fatal(err)
	}
	n, err := s.writeGameSessionObserved(p, []byte("PRIVATE_PAYLOAD"), perfWireDirect)
	finish()
	p.wireMu.Unlock()
	if err == nil || n != 3 || p.gameTransportContext().Err() == nil {
		t.Fatalf("partial write n=%d err=%v transport=%v", n, err, p.gameTransportContext().Err())
	}
	if err := <-read; err != nil {
		t.Fatal(err)
	}
	_ = s.sendGameUpperRawClass(p, 1297, []byte("PRIVATE_SECOND"), 0)
	text := logs.String()
	for _, want := range []string{`"phase":"socket_write"`, `"written_bytes":3`, `"planned_bytes":15`, `"budget_state":"deadline_exceeded"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %s in %s", want, text)
		}
	}
	if strings.Count(text, "dnfbridge first game transport failure") != 1 || strings.Contains(text, "PRIVATE_") || s.packetLog != nil {
		t.Fatalf("unbounded or private transport diagnostic: %s", text)
	}
}
