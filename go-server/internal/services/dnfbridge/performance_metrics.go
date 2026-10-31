package dnfbridge

import (
	"net"
	"strconv"
	"sync/atomic"
	"time"

	"longheng.io/server/internal/platform/metrics"
)

// Fixed series bound memory independently of players, connections and raw input.
// Hot observations allocate no maps and never acquire the registry mutex.
const (
	perfEventQueue = iota
	perfEventRun
	perfWireLock
	perfWireDirect
	perfWireScene
	perfWireNative
	perfWireOverflow
	perfUpper39
	perfUpperOther
	perfMonsterTotal
	perfMonsterExperience
	perfMonsterDeathWrite
	perfMonsterTasks
	perfMonsterClear
	perfMonsterEnqueueToDeath
	perfGuildRanking
	perfGuildHongbao
	perfSelectQueue
	perfSelectResolve
	perfSelectTotal
	perfSelectAck
	perfSelectLoadingReport
	perfCount
)

var bridgePerformanceNames = [perfCount]string{
	"event_queue", "event_run", "wire_lock", "wire_direct", "wire_scene",
	"wire_native", "wire_overflow", "upper_39", "upper_other", "monster_total",
	"monster_experience", "monster_death_write", "monster_tasks", "monster_clear",
	"monster_enqueue_to_death", "guild_ranking", "guild_hongbao",
	"select_queue", "select_resolve", "select_total", "select_ack_write",
	"select_client_loading_report",
}
var bridgePerformanceLimits = [...]time.Duration{5 * time.Millisecond, 10 * time.Millisecond, 25 * time.Millisecond, 50 * time.Millisecond, 100 * time.Millisecond, 200 * time.Millisecond, 300 * time.Millisecond, 500 * time.Millisecond, time.Second, 3 * time.Second}

type bridgePerformanceSeries struct {
	count   atomic.Int64
	nanos   atomic.Int64
	errors  atomic.Int64
	buckets [len(bridgePerformanceLimits) + 1]atomic.Int64
}
type bridgePerformanceMetrics struct {
	series    [perfCount]bridgePerformanceSeries
	slowAfter [perfCount]atomic.Int64
	slowEpoch time.Time
}

func newBridgePerformanceMetrics(registry *metrics.Registry) *bridgePerformanceMetrics {
	if registry == nil {
		return nil
	}
	m := &bridgePerformanceMetrics{slowEpoch: time.Now()}
	registry.RegisterObserver("dnfbridge_performance", m.observe)
	return m
}
func (m *bridgePerformanceMetrics) record(stage int, duration time.Duration, failed bool) {
	if m == nil || stage < 0 || stage >= perfCount {
		return
	}
	if duration < 0 {
		duration = 0
	}
	s := &m.series[stage]
	s.count.Add(1)
	s.nanos.Add(int64(duration))
	if failed {
		s.errors.Add(1)
	}
	bucket := len(bridgePerformanceLimits)
	for i, limit := range bridgePerformanceLimits {
		if duration <= limit {
			bucket = i
			break
		}
	}
	s.buckets[bucket].Add(1)
}
func (m *bridgePerformanceMetrics) observe(registry *metrics.Registry) {
	for i, name := range bridgePerformanceNames {
		s := &m.series[i]
		labels := map[string]string{"stage": name}
		registry.SetHistogramSample("dnfbridge_stage_seconds_sum", labels, 0, true, float64(s.nanos.Load())/1e9)
		registry.SetCounter("dnfbridge_stage_errors_total", labels, s.errors.Load())
		var cumulative int64
		for j := range s.buckets {
			cumulative += s.buckets[j].Load()
			bound := "+Inf"
			if j < len(bridgePerformanceLimits) {
				bound = strconv.FormatFloat(bridgePerformanceLimits[j].Seconds(), 'g', -1, 64)
			}
			registry.SetHistogramSample("dnfbridge_stage_seconds_bucket", map[string]string{"stage": name, "le": bound}, cumulative, false, 0)
		}
		registry.SetHistogramSample("dnfbridge_stage_seconds_count", labels, cumulative, false, 0)
	}
}
func (s *Service) performanceStart() time.Time {
	if s == nil || s.performance == nil {
		return time.Time{}
	}
	return time.Now()
}
func (s *Service) performanceEnd(stage int, start time.Time, err error) {
	if s == nil || s.performance == nil || start.IsZero() {
		return
	}
	s.recordPerformance(stage, time.Since(start), err != nil)
}

func (m *bridgePerformanceMetrics) admitSlowSample(stage int, duration time.Duration, now time.Time) bool {
	if m == nil || stage < 0 || stage >= perfCount || duration < 300*time.Millisecond {
		return false
	}
	elapsed := now.Sub(m.slowEpoch)
	deadline := m.slowAfter[stage].Load()
	if int64(elapsed) < deadline {
		return false
	}
	return m.slowAfter[stage].CompareAndSwap(deadline, int64(elapsed+10*time.Second))
}

func (s *Service) recordPerformance(stage int, duration time.Duration, failed bool) {
	if s == nil || s.performance == nil {
		return
	}
	s.performance.record(stage, duration, failed)
	if s.performance.admitSlowSample(stage, duration, time.Now()) {
		s.logInfo("dnfbridge slow stage sample", "stage", bridgePerformanceNames[stage], "duration_ms", duration.Milliseconds(), "failed", failed)
	}
}

// Preserves actual write count, bytes, errors and existing caller short-write policy.
func (s *Service) writeGameConnectionObserved(conn net.Conn, data []byte, stage int) (int, error) {
	start := s.performanceStart()
	n, err := conn.Write(data)
	if s != nil && s.performance != nil {
		s.recordPerformance(stage, time.Since(start), err != nil || n != len(data))
	}
	return n, err
}
