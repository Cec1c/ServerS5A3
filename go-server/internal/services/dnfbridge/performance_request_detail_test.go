package dnfbridge

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	"longheng.io/server/internal/platform/metrics"
)

func TestSlowRequestDetailSharesExistingBudgetAndKeepsPrivacy(t *testing.T) {
	var logs bytes.Buffer
	s := &Service{logger: slog.New(slog.NewJSONHandler(&logs, nil)), performance: newBridgePerformanceMetrics(metrics.New())}
	started := time.Now().Add(-time.Second)
	err := errors.New("PRIVATE_ERROR_PAYLOAD")
	s.performanceUpperEnd(perfUpperOther, 140, 1, started, err)
	s.performanceUpperEnd(perfUpperOther, 593, 1, started, nil)
	s.performanceEventEnd("PRIVATE_SOURCE_ACCOUNT", started, err)
	s.performanceEventEnd("game-stream-packet", started, nil)
	lines := bytes.Split(bytes.TrimSpace(logs.Bytes()), []byte{'\n'})
	if len(lines) != 2 {
		t.Fatalf("budget emitted %d records, want 2 fixed stage samples", len(lines))
	}
	var upper, event map[string]any
	if err := json.Unmarshal(lines[0], &upper); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(lines[1], &event); err != nil {
		t.Fatal(err)
	}
	if upper["opcode"] != float64(140) || upper["classification"] != float64(1) || upper["failed"] != true || event["source"] != "other" {
		t.Fatalf("missing fixed detail: upper=%v event=%v", upper, event)
	}
	if strings.Contains(logs.String(), "PRIVATE_") {
		t.Fatal("private diagnostic value escaped")
	}
	for _, stage := range []int{perfUpperOther, perfEventRun} {
		if s.performance.series[stage].count.Load() != 2 || s.performance.series[stage].errors.Load() != 1 {
			t.Fatal("sampling changed actual observation/error count")
		}
	}
}

func TestSlowRequestDetailCountsActualLegacyDeathsAndBurstCommit(t *testing.T) {
	s, p, _, packets := prepareDungeonDeathBurstTest(t, true)
	s.performance = newBridgePerformanceMetrics(metrics.New())
	if err := s.handleCurrentDungeonDeathBurstPackets(p, packets, false); err != nil {
		t.Fatal(err)
	}
	if got := s.performance.series[perfUpper39].count.Load(); got != int64(len(packets)) {
		t.Fatalf("actual legacy deaths counted=%d want=%d", got, len(packets))
	}
	if got := s.performance.series[perfDeathBurstCommit].count.Load(); got != 1 {
		t.Fatalf("actual burst commit count=%d want=1", got)
	}
	if got := s.performance.series[perfLegacyOther].count.Load(); got != 0 {
		t.Fatalf("deaths polluted noncombat series: %d", got)
	}
	before := s.performance.series[perfUpperOther].count.Load()
	raw, err := dnfproto.BuildChannelPacket(593, make([]byte, 8), 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.handleGameUpper(p, raw); err != nil {
		t.Fatal(err)
	}
	if s.performance.series[perfUpperOther].count.Load() != before+1 {
		t.Fatal("actual upper handler lost its observation")
	}
}

func TestSlowRequestDetailFastNilAndKnownSource(t *testing.T) {
	var absent *Service
	absent.performanceUpperEnd(perfUpperOther, 1, 1, time.Now(), nil)
	absent.performanceEventEnd("game-stream-packet", time.Now(), nil)
	var logs bytes.Buffer
	s := &Service{logger: slog.New(slog.NewJSONHandler(&logs, nil)), performance: newBridgePerformanceMetrics(metrics.New())}
	s.performanceUpperEnd(perfUpperOther, 1, 1, time.Time{}, nil)
	s.performanceEventEnd("game-stream-packet", time.Time{}, nil)
	s.performanceUpperEnd(perfUpperOther, 1, 1, time.Now(), nil)
	if logs.Len() != 0 || s.performance.series[perfUpperOther].count.Load() != 1 {
		t.Fatal("fast/disabled sample changed")
	}
	s.performanceEventEnd("game-stream-packet", time.Now().Add(-time.Second), nil)
	if !strings.Contains(logs.String(), `"source":"game-stream-packet"`) {
		t.Fatal("known event source lost")
	}
}
