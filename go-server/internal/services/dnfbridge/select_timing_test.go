package dnfbridge

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"longheng.io/server/internal/platform/metrics"
)

func TestSelectTimingCountsOnlyActualACKWrites(t *testing.T) {
	for _, mode := range []string{"direct", "batch", "failed"} {
		t.Run(mode, func(t *testing.T) {
			s := &Service{performance: newBridgePerformanceMetrics(metrics.New())}
			p := &gameSession{conn: &bufferConn{}}
			timing := s.beginSelectTiming(p)
			if mode == "batch" {
				p.channelSceneBatch = &currentChannelSceneBatch{started: time.Now()}
			}
			if mode == "failed" {
				p.conn = &sharedDiagnosticFaultConn{}
			}
			err := s.sendGameUpperRawClass(p, 4, []byte{1}, 1)
			if mode == "failed" {
				if err == nil || !timing.ackWritten.IsZero() {
					t.Fatal("failed write marked ACK", err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if mode == "batch" {
					if !timing.ackWritten.IsZero() {
						t.Fatal("buffered ACK claimed sent")
					}
					if err := s.finishCurrentOrdinaryChannelSceneBatch(p, nil); err != nil {
						t.Fatal(err)
					}
				}
				if timing.ackWritten.IsZero() {
					t.Fatal("actual ACK write missing")
				}
			}
			s.finishSelectTiming(p, timing, err)
			want := int64(1)
			if mode == "failed" {
				want = 0
			}
			if s.performance.series[perfSelectAck].count.Load() != want {
				t.Fatal("ACK count")
			}
		})
	}
}

func TestSelectTimingSlowLogIsCorrelatedAndRateLimited(t *testing.T) {
	var logs bytes.Buffer
	s := &Service{performance: newBridgePerformanceMetrics(metrics.New()), logger: slog.New(slog.NewJSONHandler(&logs, nil))}
	p := &gameSession{conn: &bufferConn{}, connID: "synthetic-select", selectedCharacterID: 9}
	for i := 0; i < 2; i++ {
		timing := s.beginSelectTiming(p)
		timing.started = timing.started.Add(-time.Second)
		s.finishSelectTiming(p, timing, errors.New("synthetic failure"))
	}
	if got := strings.Count(logs.String(), "dnfbridge select character timing"); got != 1 {
		t.Fatalf("samples=%d logs=%s", got, logs.String())
	}
	for _, field := range []string{"synthetic-select", "ack_written", "identity_read_ms", "initialization_ms"} {
		if !strings.Contains(logs.String(), field) {
			t.Fatal("missing correlation", field)
		}
	}
}

func TestSelectTimingLoadingReportBelongsToOneSelection(t *testing.T) {
	for _, changed := range []bool{false, true} {
		s := &Service{performance: newBridgePerformanceMetrics(metrics.New())}
		p := &gameSession{conn: &bufferConn{}, selectedCharacterID: 9, characterGeneration: 1}
		timing := s.beginSelectTiming(p)
		if err := s.sendGameUpperRawClass(p, 4, []byte{1}, 1); err != nil {
			t.Fatal(err)
		}
		s.finishSelectTiming(p, timing, nil)
		if changed {
			p.characterGeneration++
		}
		s.noteSelectClientLoadingReport(p)
		s.noteSelectClientLoadingReport(p)
		want := int64(1)
		if changed {
			want = 0
		}
		if got := s.performance.series[perfSelectLoadingReport].count.Load(); got != want {
			t.Fatalf("changed=%v count=%d want=%d", changed, got, want)
		}
	}
}
