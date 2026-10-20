package dnfbridge

import (
	"os"
	"strings"
	"testing"
)

func TestReview44TypedNilAcrossLoggerBoundaries(t *testing.T) {
	for _, boundary := range []string{"packet_event", "packet_payload", "service_info", "service_warn"} {
		t.Run(boundary, func(t *testing.T) {
			defer func() {
				if recover() != nil {
					t.Fatal("Review44: typed-nil ordinary diagnostic panicked at production logger boundary; values omitted")
				}
			}()
			s, _, logs := review43Logs(t)
			var ordinaryErr *os.PathError
			fields := []any{"error", ordinaryErr, "ordinary_context", "public-nil-boundary-control"}
			switch boundary {
			case "packet_event":
				s.logPacketEvent("review44-nil-event", fields...)
			case "packet_payload":
				s.logPacket("RECV", "review44-nil-payload", []byte("public-nil-payload-control"), fields...)
			case "service_info":
				s.logInfo("review44-nil-info", fields...)
			case "service_warn":
				s.logWarn("review44-nil-warn", fields...)
			}
			if !strings.Contains(logs(), "public-nil-boundary-control") {
				t.Fatal("Review44: safe ordinary diagnostic context lost")
			}
		})
	}
}
