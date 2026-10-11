package dnfbridge

import (
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
)

// Depends on the permanently retained native_log_supplement45_test.go fixture
// and its four production entry adapters. Only public synthetic text is used.
func TestBusiness47AuditedDiagnosticRestoresArbitraryText(t *testing.T) {
	for _, boundary := range []string{"packet_event", "packet_payload", "service_info", "service_warn"} {
		for _, field := range []string{"reason", "error", "save_error"} {
			for _, asString := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/string_%t", boundary, field, asString), func(t *testing.T) {
					s, logs := supplement45LogSink(t)
					// Text deliberately unrelated to the ten failed assertions.
					text := "public-domain-rule47 arbitrary detail=937 missing owner evidence"
					cause := errors.New("public-business47-cause")
					original := fmt.Errorf("%s: %w", text, cause)
					var value any = original
					if asString {
						value = original.Error()
					}
					fields := []any{field, businessLogDiagnostic(value), "ordinary_context", "business47-context"}
					before := append([]any(nil), fields...)
					supplement45Diagnostic(s, boundary, fields)
					logged := logs()
					if !strings.Contains(logged, text) || strings.Contains(logged, field+"_sha256=") {
						t.Fatal("audited business diagnostic lost its ordinary text")
					}
					if !reflect.DeepEqual(before, fields) || !errors.Is(original, cause) {
						t.Fatal("logging mutated arguments or original error unwrap chain")
					}
				})
			}
		}
	}
}

func TestBusiness47UnknownErrorsRemainPrivateDespiteGameplayContext(t *testing.T) {
	for _, session := range []*gameSession{nil, {}} {
		t.Run(fmt.Sprintf("session_%t", session != nil), func(t *testing.T) {
			s, logs := supplement45LogSink(t)
			s.logGameEvent(session, "public-business47-provider-event", "error", errors.New(supplement45ErrorMarker))
			text := logs()
			if strings.Contains(text, supplement45ErrorMarker) || !strings.Contains(text, "error_sha256=") {
				t.Fatal("game session or event label incorrectly trusted an unclassified error")
			}
		})
	}
}

func TestBusiness47MarkedNilAndPanicStillHashOnly(t *testing.T) {
	var typedNil *os.PathError
	for _, value := range []any{typedNil, &supplement45PanicError{}, fmt.Sprint(&supplement45PanicError{})} {
		for _, boundary := range []string{"packet_event", "packet_payload", "service_info", "service_warn"} {
			t.Run(fmt.Sprintf("%T/%s", value, boundary), func(t *testing.T) {
				s, logs := supplement45LogSink(t)
				fields := []any{"reason", businessLogDiagnostic(value)}
				before := append([]any(nil), fields...)
				supplement45Diagnostic(s, boundary, fields)
				text := logs()
				if strings.Contains(text, supplement45ErrorMarker) || strings.Contains(text, "PANIC=") || !strings.Contains(text, "reason_sha256=") {
					t.Fatal("marked nil/panic diagnostic was not safely hashed")
				}
				if !reflect.DeepEqual(fields, before) {
					t.Fatal("logging mutated caller fields")
				}
			})
		}
	}
}

func TestBusiness47LoginIdentityOverridesBusinessMarker(t *testing.T) {
	for _, payload := range []bool{false, true} {
		t.Run(fmt.Sprintf("payload_%t", payload), func(t *testing.T) {
			s, logs := supplement45LogSink(t)
			fields := []any{"msg_id", uint16(1), "classification", uint8(1), "error", businessLogDiagnostic(supplement45ErrorMarker)}
			if payload {
				s.logPacket("RECV", "game-upper", []byte{1, 1, 0}, fields...)
			} else {
				s.logPacketEvent("business47-marked-event", fields...)
			}
			text := logs()
			if strings.Contains(text, supplement45ErrorMarker) || !strings.Contains(text, "payload_redacted=true") {
				t.Fatal("login identity did not override marked business field")
			}
		})
	}
}
