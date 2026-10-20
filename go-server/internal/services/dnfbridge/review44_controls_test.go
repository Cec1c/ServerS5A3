package dnfbridge

import (
	"bytes"
	"encoding/hex"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	p "longheng.io/server/internal/modules/dnf/protocol"
)

func TestReview44OrdinaryTCPDiagnosticAndDispatchRemain(t *testing.T) {
	s, session, logs := review43Logs(t)
	body := []byte("public-ordinary-control-44")
	raw, err := p.BuildLatestGameTCP(1, 60001, body, p.TransportOptions{Sequence: 44})
	if err != nil {
		t.Fatal("Review44: ordinary TCP construction failed")
	}
	before := bytes.Clone(raw)
	if s.handleGameFrame(session, raw) != nil {
		t.Fatal("Review44: ordinary dispatch failed")
	}
	text := logs()
	if !strings.Contains(text, hex.EncodeToString(raw)) || !strings.Contains(text, "type=60001") || !strings.Contains(text, "game-transport-meta") {
		t.Fatal("Review44: ordinary whole-frame diagnostic or metadata lost")
	}
	if !bytes.Equal(before, raw) {
		t.Fatal("Review44: logging changed ordinary business frame")
	}
	if session.accountFromToken || session.loginPacketSeen {
		t.Fatal("Review44: ordinary record altered authentication")
	}
	review43NoCredential(t, text)
}

func TestReview44LoggingFieldTypesAndNilErrorsRemainSafe(t *testing.T) {
	for _, name := range []string{"nil_value", "typed_nil_path_error", "numeric_error_field", "ordinary_string_field"} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() != nil {
					t.Fatal("Review44: diagnostic logging panicked on a previously supported ordinary field type; values omitted")
				}
			}()
			s, _, logs := review43Logs(t)
			var value any
			switch name {
			case "typed_nil_path_error":
				var e *os.PathError
				value = e
			case "numeric_error_field":
				value = 44
			case "ordinary_string_field":
				value = "public-ordinary-field"
			}
			fields := []any{"error", value, "ordinary_context", "public-safe-context"}
			before := append([]any(nil), fields...)
			s.logPacketEvent("review44-ordinary-event", fields...)
			s.logInfo("review44-ordinary-info", fields...)
			s.logWarn("review44-ordinary-warn", fields...)
			if !reflect.DeepEqual(fields, before) {
				t.Fatal("Review44: logging mutated caller field slice")
			}
			text := logs()
			for _, event := range []string{"review44-ordinary-event", "review44-ordinary-info", "review44-ordinary-warn", "public-safe-context"} {
				if !strings.Contains(text, event) {
					t.Fatal("Review44: ordinary diagnostic event/context lost")
				}
			}
			if name == "numeric_error_field" && !strings.Contains(text, "error=44") {
				t.Fatal("Review44: non-string/error value altered")
			}
			review43NoCredential(t, text)
		})
	}
}

func TestReview44ErrorIdentityAndFieldSliceUnchanged(t *testing.T) {
	s, _, logs := review43Logs(t)
	original := errors.New("public-error-control")
	wrapped := errors.Join(original, errors.New("public-second-error-control"))
	beforeText := wrapped.Error()
	fields := []any{"reason", wrapped, "ordinary_context", "public-safe-context"}
	before := append([]any(nil), fields...)
	s.logPacketEvent("review44-error-control", fields...)
	s.logWarn("review44-error-control", fields...)
	if !reflect.DeepEqual(fields, before) || fields[1] != wrapped || !errors.Is(wrapped, original) || wrapped.Error() != beforeText {
		t.Fatal("Review44: log redaction mutated original error, unwrap chain or caller fields")
	}
	if !strings.Contains(logs(), "public-safe-context") {
		t.Fatal("Review44: safe error context lost")
	}
}
