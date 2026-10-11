package dnfbridge

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// Self-contained public provider-error control, including its own logger sink.
func TestReview45PlainProviderErrorHashAndFields(t *testing.T) {
	for _, boundary := range []string{"packet_event", "packet_payload", "service_info", "service_warn"} {
		t.Run(boundary, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "public-provider45.log")
			packet, err := openPacketLogger(path)
			if err != nil {
				t.Fatal("Review45: logger unavailable")
			}
			t.Cleanup(func() { _ = packet.Close() })
			var service bytes.Buffer
			s := &Service{packetLog: packet, logger: slog.New(slog.NewTextHandler(&service, nil))}
			marker := "public-provider-sensitive-body45"
			original := errors.New(marker)
			fields := []any{"error", original, "reason", original, "ordinary_context", "public-provider-context45"}
			before := append([]any(nil), fields...)
			switch boundary {
			case "packet_event":
				s.logPacketEvent("review45-provider-event", fields...)
			case "packet_payload":
				s.logPacket("RECV", "review45-provider-payload", []byte("public-provider-payload45"), fields...)
			case "service_info":
				s.logInfo("review45-provider-info", fields...)
			case "service_warn":
				s.logWarn("review45-provider-warn", fields...)
			}
			if !reflect.DeepEqual(fields, before) || fields[1] != original || fields[3] != original || !errors.Is(fields[1].(error), original) || original.Error() != marker {
				t.Fatal("Review45: provider error or caller fields changed")
			}
			if packet.Close() != nil {
				t.Fatal("Review45: logger close failed")
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal("Review45: temporary log read failed")
			}
			text := string(data) + service.String()
			if strings.Contains(text, marker) || strings.Contains(text, hex.EncodeToString([]byte(marker))) {
				t.Fatal("Review45: synthetic provider body leaked; values omitted")
			}
			for _, key := range []string{"error", "reason"} {
				if !strings.Contains(text, fmt.Sprintf("%s_sha256=%x", key, sha256.Sum256([]byte(marker)))) || !strings.Contains(text, fmt.Sprintf("%s_len=%d", key, len(marker))) {
					t.Fatal("Review45: plain provider protected hash/length missing")
				}
			}
			if !strings.Contains(text, "public-provider-context45") {
				t.Fatal("Review45: ordinary context lost")
			}
		})
	}
}
