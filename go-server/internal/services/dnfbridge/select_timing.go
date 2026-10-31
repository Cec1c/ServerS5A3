package dnfbridge

import "time"

// One correlated sample per slow selection, with fixed metric stage names.
// The wire timestamp is recorded by the actual writer, including batch flush.
type currentSelectTiming struct {
	started                                    time.Time
	resolve, admission, preparation            time.Duration
	ackWritten                                 time.Time // selectTimingMu
	queue                                      time.Duration
	identityRead, selectedRead, initialization time.Duration
	batched                                    bool
}

type currentSelectLoadingTiming struct {
	started, ackWritten time.Time
	character           uint16
	generation          uint64
}

func (s *Service) beginSelectTiming(p *gameSession) *currentSelectTiming {
	timing := &currentSelectTiming{started: time.Now()}
	if !p.performanceEventQueuedAt.IsZero() {
		timing.queue = timing.started.Sub(p.performanceEventQueuedAt)
	}
	p.selectTimingMu.Lock()
	p.selectTiming = timing
	p.selectLoadingTiming = nil
	p.selectTimingMu.Unlock()
	return timing
}

func (s *Service) finishSelectTiming(p *gameSession, timing *currentSelectTiming, err error) {
	p.selectTimingMu.Lock()
	written := timing.ackWritten
	p.selectTiming = nil
	if err == nil && !written.IsZero() && p.selectedCharacterID != 0 {
		p.selectLoadingTiming = &currentSelectLoadingTiming{timing.started, written, p.selectedCharacterID, p.characterGeneration}
	}
	p.selectTimingMu.Unlock()
	total := time.Since(timing.started)
	s.performance.record(perfSelectQueue, timing.queue, false)
	s.performance.record(perfSelectResolve, timing.resolve, err != nil)
	s.performance.record(perfSelectTotal, total, err != nil)
	if !written.IsZero() {
		s.performance.record(perfSelectAck, written.Sub(timing.started), false)
	}
	if total >= 300*time.Millisecond || err != nil {
		// Reserve one correlated sample, without consuming the limiter first
		// through generic stage logging. Failures share the same bounded rate.
		if s.performance == nil || s.performance.admitSlowSample(perfSelectTotal, max(total, 300*time.Millisecond), time.Now()) {
			s.logInfo("dnfbridge select character timing", "conn_id", p.connID, "char_id", p.selectedCharacterID,
				"request_started", timing.started.UTC().Format(time.RFC3339Nano), "queue_ms", timing.queue.Milliseconds(),
				"resolve_ms", timing.resolve.Milliseconds(), "admission_ms", timing.admission.Milliseconds(),
				"identity_read_ms", timing.identityRead.Milliseconds(), "selected_read_ms", timing.selectedRead.Milliseconds(),
				"initialization_ms", timing.initialization.Milliseconds(), "batched", timing.batched,
				"prepare_ms", timing.preparation.Milliseconds(), "ack_written", !written.IsZero(),
				"ack_write_ms", selectElapsed(timing.started, written), "total_ms", total.Milliseconds(), "failed", err != nil)
		}
	}
}

// This is the first accepted client loading report after selection, not proof
// that the client's screen rendered. Never attribute another character's
// loading cycle or duplicate op37 to the preceding selection.
func (s *Service) noteSelectClientLoadingReport(p *gameSession) {
	p.selectTimingMu.Lock()
	timing := p.selectLoadingTiming
	p.selectLoadingTiming = nil
	p.selectTimingMu.Unlock()
	if timing == nil || timing.character != p.selectedCharacterID || timing.generation != p.characterGeneration {
		return
	}
	elapsed := time.Since(timing.started)
	s.performance.record(perfSelectLoadingReport, elapsed, false)
	if elapsed >= 300*time.Millisecond && (s.performance == nil || s.performance.admitSlowSample(perfSelectLoadingReport, elapsed, time.Now())) {
		s.logInfo("dnfbridge select client loading report", "conn_id", p.connID, "char_id", timing.character,
			"request_started", timing.started.UTC().Format(time.RFC3339Nano), "total_ms", elapsed.Milliseconds(),
			"after_ack_ms", time.Since(timing.ackWritten).Milliseconds())
	}
}

func noteSelectReadTiming(p *gameSession, stage string, started time.Time) {
	p.selectTimingMu.Lock()
	defer p.selectTimingMu.Unlock()
	if timing := p.selectTiming; timing != nil {
		switch stage {
		case "identity":
			timing.identityRead += time.Since(started)
		case "selected":
			timing.selectedRead += time.Since(started)
		case "initialization":
			timing.initialization += time.Since(started)
		}
	}
}

func selectElapsed(start, end time.Time) int64 {
	if end.IsZero() {
		return -1
	}
	return end.Sub(start).Milliseconds()
}

func noteSelectAckWrittenLocked(p *gameSession, inner []byte) {
	class, op, ok := upperPacketClassMsgID(inner)
	p.selectTimingMu.Lock()
	defer p.selectTimingMu.Unlock()
	if p.selectTiming != nil && p.selectTiming.ackWritten.IsZero() && ok && class == 1 && op == 4 {
		p.selectTiming.ackWritten = time.Now()
	}
}
