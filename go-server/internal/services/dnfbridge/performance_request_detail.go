package dnfbridge

import (
	"strings"
	"time"
)

// Keep the same fixed histogram series and shared ten-second sample budgets.
// Only a sampled slow request gains its numeric opcode or fixed event source;
// no payload, character/account identifier, address or error text is copied.
func (s *Service) performanceUpperEnd(stage int, opcode uint16, classification byte, started time.Time, err error) {
	if s == nil || s.performance == nil || started.IsZero() {
		return
	}
	duration := time.Since(started)
	s.performance.record(stage, duration, err != nil)
	if s.performance.admitSlowSample(stage, duration, time.Now()) {
		s.logInfo("dnfbridge slow stage sample", "stage", bridgePerformanceNames[stage], "duration_ms", duration.Milliseconds(), "failed", err != nil, "opcode", opcode, "classification", classification)
	}
}

func (s *Service) performanceEventEnd(source string, started time.Time, err error) {
	if s == nil || s.performance == nil || started.IsZero() {
		return
	}
	duration := time.Since(started)
	s.performance.record(perfEventRun, duration, err != nil)
	if s.performance.admitSlowSample(perfEventRun, duration, time.Now()) {
		s.logInfo("dnfbridge slow stage sample", "stage", bridgePerformanceNames[perfEventRun], "duration_ms", duration.Milliseconds(), "failed", err != nil, "source", currentPerformanceEventSource(source))
	}
}

func currentPerformanceEventSource(source string) string {
	switch source {
	case "game-stream-packet", "game-session-shutdown", "guild-presence-refresh",
		"town-peer-enter", "town-peer-leave", "town-peer-position", "party-completed-return",
		"dungeon-party-loading-release", "dungeon-party-monster-death", "quest-material-recovery":
		return source
	default:
		if strings.HasPrefix(source, "town_copresence_") || strings.HasPrefix(source, "town-peer-") {
			return "town_peer_delivery"
		}
		return "other"
	}
}
