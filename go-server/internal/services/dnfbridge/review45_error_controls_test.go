package dnfbridge

import (
	"bytes"
	"encoding/hex"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	p "longheng.io/server/internal/modules/dnf/protocol"
)

// These are public synthetic errors. No token or password is read or issued.
type review45PanickingError struct{}

func (*review45PanickingError) Error() string { panic("public-safe-error-panic-45") }

type review45SensitiveError struct{ body string }

func (e *review45SensitiveError) Error() string { return e.body }

// Self-contained fixture: no Review43/44 or other test helper is required.
func review45Logger(t *testing.T) (*Service, func() string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "public-synthetic45.log")
	packet, err := openPacketLogger(path)
	if err != nil {
		t.Fatal("Review45: offline packet logger unavailable")
	}
	var ordinary bytes.Buffer
	closed := false
	t.Cleanup(func() {
		if !closed {
			_ = packet.Close()
		}
	})
	s := &Service{packetLog: packet, logger: slog.New(slog.NewTextHandler(&ordinary, nil))}
	return s, func() string {
		if !closed {
			if packet.Close() != nil {
				t.Fatal("Review45: logger close failed")
			}
			closed = true
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal("Review45: temporary log read failed")
		}
		return string(data) + ordinary.String()
	}
}

func review45LogBoundary(s *Service, boundary string, fields []any) {
	switch boundary {
	case "packet_event":
		s.logPacketEvent("review45-public-event", fields...)
	case "packet_payload":
		s.logPacket("RECV", "review45-public-payload", []byte("public-payload45"), fields...)
	case "service_info":
		s.logInfo("review45-public-info", fields...)
	case "service_warn":
		s.logWarn("review45-public-warn", fields...)
	}
}

func TestReview45NonNilPanickingErrorAcrossLoggerBoundaries(t *testing.T) {
	for _, boundary := range []string{"packet_event", "packet_payload", "service_info", "service_warn"} {
		t.Run(boundary, func(t *testing.T) {
			defer func() {
				if recover() != nil {
					t.Fatal("Review45: non-nil Error panic escaped production logging; values omitted")
				}
			}()
			s, logs := review45Logger(t)
			original := &review45PanickingError{}
			var errorValue error = original
			fields := []any{"error", errorValue, "reason", errorValue, "ordinary_context", "public-safe-context45"}
			before := append([]any(nil), fields...)
			review45LogBoundary(s, boundary, fields)
			if !reflect.DeepEqual(fields, before) || fields[1] != original || fields[3] != original || !errors.Is(errorValue, original) {
				t.Fatal("Review45: logging changed caller slice or original error identity")
			}
			if !strings.Contains(logs(), "public-safe-context45") {
				t.Fatal("Review45: ordinary context lost")
			}
		})
	}
}

func TestReview45SensitiveErrorBodyAndCallerIdentity(t *testing.T) {
	for _, boundary := range []string{"packet_event", "packet_payload", "service_info", "service_warn"} {
		t.Run(boundary, func(t *testing.T) {
			s, logs := review45Logger(t)
			marker := "public-review45-sensitive-error-body"
			original := &review45SensitiveError{body: marker}
			var errorValue error = original
			fields := []any{"error", errorValue, "reason", errorValue, "ordinary_context", "public-safe-context45"}
			before := append([]any(nil), fields...)
			review45LogBoundary(s, boundary, fields)
			if !reflect.DeepEqual(fields, before) || fields[1] != original || fields[3] != original || !errors.Is(errorValue, original) || original.Error() != marker {
				t.Fatal("Review45: logging changed caller slice, error identity or body")
			}
			text := logs()
			if strings.Contains(text, marker) || strings.Contains(text, hex.EncodeToString([]byte(marker))) {
				t.Fatal("Review45: sensitive synthetic Error body entered logs; values omitted")
			}
			if !strings.Contains(text, "public-safe-context45") {
				t.Fatal("Review45: ordinary context lost")
			}
		})
	}
}

func TestReview45OrdinaryTCPAndClassZeroDiagnosticsRemain(t *testing.T) {
	s, logs := review45Logger(t)
	tcp, err := p.BuildLatestGameTCP(1, 60001, []byte("public-tcp-control45"), p.TransportOptions{Sequence: 45})
	if err != nil {
		t.Fatal("Review45: ordinary TCP construction failed")
	}
	notice, err := p.BuildChannelPacket(1, []byte("public-classzero-control45"), 1, p.DefaultChannelClassification)
	if err != nil {
		t.Fatal("Review45: classzero construction failed")
	}
	notice[0] = 0
	parsed, err := p.ParseChannelPacket(notice)
	if err != nil || parsed.Header.Classification != 0 {
		t.Fatal("Review45: classzero fixture invalid")
	}
	tcpBefore, noticeBefore := bytes.Clone(tcp), bytes.Clone(notice)
	s.logPacket("RECV", "game", tcp, "cmd", byte(1), "type", uint16(60001), "ordinary_context", "public-tcp-context45")
	s.logPacket("SEND", "game-upper", notice, "msg_id", uint16(1), "classification", byte(0))
	text := logs()
	if !strings.Contains(text, hex.EncodeToString(tcp)) || !strings.Contains(text, hex.EncodeToString(notice)) || !strings.Contains(text, "public-tcp-context45") {
		t.Fatal("Review45: ordinary TCP/classzero raw diagnostic lost")
	}
	if !bytes.Equal(tcp, tcpBefore) || !bytes.Equal(notice, noticeBefore) {
		t.Fatal("Review45: diagnostic mutated input")
	}
}
