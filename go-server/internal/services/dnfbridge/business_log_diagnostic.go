package dnfbridge

import (
	"fmt"
	"log/slog"
	"reflect"
	"strings"
)

// businessLogDiagnostic marks only an audited gameplay/domain diagnostic field.
// It must never wrap raw receive, login, parser or codec/provider errors. Session
// authentication and event names do not establish this boundary. The original
// value is retained exclusively for logging; returns and unwrap chains stay intact.
func businessLogDiagnostic(value any) businessLogValue {
	return businessLogValue{value: value}
}

type businessLogValue struct{ value any }

// Fail closed if a marked field reaches a sink without the diagnostic policy.
func (businessLogValue) String() string       { return "redacted" }
func (businessLogValue) LogValue() slog.Value { return slog.StringValue("redacted") }

func (v businessLogValue) text() (string, bool) {
	switch value := v.value.(type) {
	case string:
		return value, !strings.Contains(value, "(PANIC=")
	case error:
		if value == nil {
			return "", false
		}
		reflected := reflect.ValueOf(value)
		switch reflected.Kind() {
		case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
			if reflected.IsNil() {
				return "", false
			}
		}
		// fmt contains panics from Error/Format methods. Its panic diagnostic may
		// contain private panic text, so it must still go through length/hash.
		text := fmt.Sprint(value)
		return text, !strings.Contains(text, "(PANIC=")
	default:
		return "", false
	}
}
