package raid

import (
	"fmt"
	"time"
)

// The bridge must additionally finish this phase's persisted reward flow and
// enforce leader/break-time authorization. This reducer accepts only a real
// cleared first phase and preserves the actual program and instance RNG.
func (s *AntonProgression) BeginNextPhase(at time.Time) (*AntonProgression, error) {
	if s == nil || s.program == nil || s.phase != 0 || !s.phaseCleared || at.IsZero() || at.Before(s.last) {
		return nil, fmt.Errorf("Anton next phase requires completed first phase and monotonic start")
	}
	return s.program.BeginPhase(1, at, s.random)
}
