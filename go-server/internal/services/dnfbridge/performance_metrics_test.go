package dnfbridge

import (
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"longheng.io/server/internal/platform/metrics"
)

type performanceWriteConn struct {
	net.Conn
	n      int
	err    error
	writes int
}

func (c *performanceWriteConn) Write([]byte) (int, error) {
	c.writes++
	return c.n, c.err
}

func TestBridgePerformanceRecordKeepsFixedSeriesAndBuckets(t *testing.T) {
	m := newBridgePerformanceMetrics(metrics.New())
	if m == nil {
		t.Fatal("newBridgePerformanceMetrics returned nil for a live registry")
	}
	if len(m.series) != perfCount || len(bridgePerformanceNames) != perfCount {
		t.Fatalf("series lengths = %d/%d, want %d", len(m.series), len(bridgePerformanceNames), perfCount)
	}
	if got, want := len(m.series[0].buckets), len(bridgePerformanceLimits)+1; got != want {
		t.Fatalf("bucket length = %d, want %d", got, want)
	}

	const workers = 32
	durations := make([]time.Duration, len(bridgePerformanceLimits)+1)
	for i := range bridgePerformanceLimits {
		durations[i] = bridgePerformanceLimits[i]
	}
	durations[len(durations)-1] = bridgePerformanceLimits[len(bridgePerformanceLimits)-1] + time.Nanosecond

	var wg sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for stage := 0; stage < perfCount; stage++ {
				for bucket, duration := range durations {
					m.record(stage, duration, bucket%2 == 0)
				}
			}
		}()
	}
	wg.Wait()

	m.record(-1, time.Second, true)
	m.record(perfCount, time.Second, true)
	var total time.Duration
	for _, duration := range durations {
		total += duration
	}
	wantErrors := int64(workers * ((len(durations) + 1) / 2))
	for stage := 0; stage < perfCount; stage++ {
		series := &m.series[stage]
		if got, want := series.count.Load(), int64(workers*len(durations)); got != want {
			t.Errorf("stage %d count = %d, want %d", stage, got, want)
		}
		if got, want := series.nanos.Load(), int64(workers)*int64(total); got != want {
			t.Errorf("stage %d nanos = %d, want %d", stage, got, want)
		}
		if got := series.errors.Load(); got != wantErrors {
			t.Errorf("stage %d errors = %d, want %d", stage, got, wantErrors)
		}
		for bucket := range series.buckets {
			if got, want := series.buckets[bucket].Load(), int64(workers); got != want {
				t.Errorf("stage %d bucket %d = %d, want %d", stage, bucket, got, want)
			}
		}
	}
}

func TestBridgePerformanceNilSafe(t *testing.T) {
	var m *bridgePerformanceMetrics
	m.record(perfEventQueue, time.Second, true)
	if got := newBridgePerformanceMetrics(nil); got != nil {
		t.Fatalf("newBridgePerformanceMetrics(nil) = %p, want nil", got)
	}

	service := &Service{}
	if start := service.performanceStart(); !start.IsZero() {
		t.Fatalf("performanceStart without metrics = %v, want zero", start)
	}
	conn := &performanceWriteConn{n: 2}
	n, err := service.writeGameConnectionObserved(conn, []byte("nil"), perfWireDirect)
	if n != 2 || err != nil || conn.writes != 1 {
		t.Fatalf("write without metrics = (%d, %v), writes %d", n, err, conn.writes)
	}
	var nilService *Service
	n, err = nilService.writeGameConnectionObserved(conn, []byte("nil service"), perfWireDirect)
	if n != 2 || err != nil || conn.writes != 2 {
		t.Fatalf("write with nil service = (%d, %v), writes %d", n, err, conn.writes)
	}
}

func TestWriteGameConnectionObservedReturnsOriginalWriteResult(t *testing.T) {
	writeErr := errors.New("write failed")
	tests := []struct {
		name       string
		conn       *performanceWriteConn
		wantN      int
		wantErr    error
		wantErrors int64
	}{
		{name: "complete", conn: &performanceWriteConn{n: 4}, wantN: 4},
		{name: "short write", conn: &performanceWriteConn{n: 2}, wantN: 2, wantErrors: 1},
		{name: "write error", conn: &performanceWriteConn{n: 3, err: writeErr}, wantN: 3, wantErr: writeErr, wantErrors: 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			service := &Service{performance: newBridgePerformanceMetrics(metrics.New())}
			n, err := service.writeGameConnectionObserved(tc.conn, []byte("data"), perfWireDirect)
			if n != tc.wantN || err != tc.wantErr {
				t.Fatalf("write result = (%d, %v), want (%d, %v)", n, err, tc.wantN, tc.wantErr)
			}
			if tc.conn.writes != 1 {
				t.Fatalf("underlying writes = %d, want 1", tc.conn.writes)
			}
			series := &service.performance.series[perfWireDirect]
			if got := series.count.Load(); got != 1 {
				t.Errorf("count = %d, want 1", got)
			}
			if got := series.errors.Load(); got != tc.wantErrors {
				t.Errorf("errors = %d, want %d", got, tc.wantErrors)
			}
		})
	}
}

func BenchmarkBridgePerformanceRecord(b *testing.B) {
	m := newBridgePerformanceMetrics(metrics.New())
	duration := bridgePerformanceLimits[len(bridgePerformanceLimits)/2]
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.record(perfWireDirect, duration, i%2 == 0)
	}
}
