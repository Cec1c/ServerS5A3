package dnfbridge

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// Public synthetic panic/error text. No live credential or token is used.
const supplement45ErrorMarker = "supplement45-public-error-body-not-for-log"

type supplement45PanicError struct{ cause error }

func (*supplement45PanicError) Error() string   { panic(supplement45ErrorMarker) }
func (e *supplement45PanicError) Unwrap() error { return e.cause }

// This fixture is self-contained; it does not depend on review43 helper names.
func supplement45LogSink(t *testing.T) (*Service, func() string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "supplement45-synthetic.log")
	packet, err := openPacketLogger(path)
	if err != nil {
		t.Fatal("synthetic logger unavailable")
	}
	var service bytes.Buffer
	s := &Service{packetLog: packet, logger: slog.New(slog.NewTextHandler(&service, nil))}
	t.Cleanup(func() { _ = packet.Close() })
	return s, func() string {
		if packet.Close() != nil {
			t.Fatal("synthetic logger close failed")
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal("synthetic log unavailable")
		}
		return string(data) + service.String()
	}
}

func supplement45Diagnostic(s *Service, boundary string, fields []any) {
	switch boundary {
	case "packet_event":
		s.logPacketEvent("supplement45-ordinary-event", fields...)
	case "packet_payload":
		s.logPacket("RECV", "supplement45-ordinary-payload", []byte("supplement45-public-payload"), fields...)
	case "service_info":
		s.logInfo("supplement45-ordinary-info", fields...)
	case "service_warn":
		s.logWarn("supplement45-ordinary-warn", fields...)
	}
}

// Runs on the formal baseline, sealed44 and candidate45. The formal logger
// supports these ordinary diagnostics; sealed44 must not introduce a panic.
func TestSupplement45DiagnosticErrorsNeverPanicAcrossProductionLoggers(t *testing.T) {
	for _, name := range []string{"typed_nil_path_error", "non_nil_panicking_error"} {
		for _, boundary := range []string{"packet_event", "packet_payload", "service_info", "service_warn"} {
			t.Run(name+"/"+boundary, func(t *testing.T) {
				defer func() {
					if recover() != nil {
						t.Fatal("ordinary error diagnostic panicked; values omitted")
					}
				}()
				s, logs := supplement45LogSink(t)
				var typedNil *os.PathError
				var original error = typedNil
				cause := errors.New("supplement45-public-cause")
				if name == "non_nil_panicking_error" {
					original = &supplement45PanicError{cause: cause}
				}
				fields := []any{"error", original, "ordinary_context", "supplement45-safe-context"}
				before := append([]any(nil), fields...)
				beforeIs := name == "non_nil_panicking_error" && errors.Is(original, cause)
				supplement45Diagnostic(s, boundary, fields)
				if !reflect.DeepEqual(fields, before) || fields[1] != original || (name == "non_nil_panicking_error" && errors.Is(original, cause) != beforeIs) {
					t.Fatal("logging changed input error, unwrap chain or caller argument slice")
				}
				if !strings.Contains(logs(), "supplement45-safe-context") {
					t.Fatal("ordinary context lost")
				}
			})
		}
	}
}

// Runs on sealed44 (RED) and candidate45 (GREEN). Hashing fmt's protected
// representation must never output the error or its panic payload as text.
func TestSupplement45SafeErrorRepresentationRemainsHashOnly(t *testing.T) {
	for _, name := range []string{"typed_nil_path_error", "non_nil_panicking_error", "ordinary_error"} {
		for _, boundary := range []string{"packet_event", "packet_payload", "service_info", "service_warn"} {
			t.Run(name+"/"+boundary, func(t *testing.T) {
				defer func() {
					if recover() != nil {
						t.Fatal("redacting diagnostic panicked; values omitted")
					}
				}()
				s, logs := supplement45LogSink(t)
				var typedNil *os.PathError
				var original error = typedNil
				if name == "non_nil_panicking_error" {
					original = &supplement45PanicError{}
				}
				if name == "ordinary_error" {
					original = errors.New(supplement45ErrorMarker)
				}
				formatted := fmt.Sprint(original)
				fields := []any{"reason", original, "ordinary_context", "supplement45-safe-context"}
				before := append([]any(nil), fields...)
				supplement45Diagnostic(s, boundary, fields)
				text := logs()
				if strings.Contains(text, supplement45ErrorMarker) || strings.Contains(text, "PANIC=") || strings.Contains(text, "Error method:") {
					t.Fatal("diagnostic exposed error or panic body; values omitted")
				}
				if !strings.Contains(text, fmt.Sprintf("reason_sha256=%x", sha256.Sum256([]byte(formatted)))) || !strings.Contains(text, fmt.Sprintf("reason_len=%d", len(formatted))) {
					t.Fatal("protected representation length/hash missing")
				}
				if !reflect.DeepEqual(fields, before) || fields[1] != original {
					t.Fatal("caller argument slice or error changed")
				}
				if !strings.Contains(text, "supplement45-safe-context") {
					t.Fatal("ordinary context lost")
				}
			})
		}
	}
}

func TestSupplement45FmtProtectsNilAndPanickingErrorFormatting(t *testing.T) {
	var typedNil *os.PathError
	if fmt.Sprint(error(typedNil)) != "<nil>" {
		t.Fatal("stdlib typed-nil formatting contract changed")
	}
	text := fmt.Sprint(&supplement45PanicError{})
	if !strings.Contains(text, "PANIC=Error method:") {
		t.Fatal("stdlib panic formatting protection changed")
	}
}
