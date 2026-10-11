package dnfbridge

import (
	"encoding/hex"
	"fmt"
	"math"
	"time"

	"longheng.io/server/internal/modules/dnf/raid"
)

type currentAntonResultProjection struct {
	Phase               byte
	Failed              bool
	ClearSeconds        uint32
	Deaths              uint32
	DomainRank          byte
	FinalWeeklyRequired bool
	Body                []byte
}

// This only freezes the completed/expired instance's server-owned statistics.
// It does not publish a result or grant rewards. The eventual final-success
// caller must additionally confirm its frozen weekly intent was committed.
func prepareCurrentAntonResultProjection(state runtimeRaidState, rewardOption byte) (result currentAntonResultProjection, err error) {
	b := state.Battle
	if !state.Started || b == nil || (b.Mode != 1 && b.Mode != 2) || b.Phase > 1 || b.EnterAfter.IsZero() || b.Deadline.IsZero() || !b.Deadline.After(b.EnterAfter) || rewardOption > 1 || (!b.Single && !currentRaidHasTwoPhaseProgram(b)) {
		return result, fmt.Errorf("Anton result instance unavailable")
	}
	if b.Single && (b.Phase != 0 || b.Anton != nil || len(b.SingleSequence) != 6 || len(b.MemberAccounts) != 1 || b.AntonRewards == nil || len(b.AntonRewards.Phases) != 1 || b.AntonRewards.Phases[0].MinimumClears != uint32(len(b.SingleSequence))) {
		return result, fmt.Errorf("single Anton result actual sequence/reward owner unavailable")
	}
	if b.PhaseCompleted == b.TimedOut {
		return result, fmt.Errorf("Anton result must be one terminal success or timeout")
	}
	if b.TimedOut && b.Anton != nil && b.Anton.PhaseCleared() {
		return result, fmt.Errorf("Anton timeout conflicts with cleared phase")
	}
	if (!b.Single && b.Anton.Phase() != int(b.Phase)) || len(b.Configuration.PhaseLimitSeconds) != 2 || b.Configuration.PhaseLimitSeconds[b.Phase] == 0 || b.Deadline.Sub(b.EnterAfter) != time.Duration(b.Configuration.PhaseLimitSeconds[b.Phase])*time.Second {
		return result, fmt.Errorf("Anton result program/configured phase clock mismatch")
	}
	var total uint64
	for key, life := range b.MemberLives {
		if life.Phase != b.Phase {
			continue
		}
		id, decodeErr := hex.DecodeString(key.RunID)
		if decodeErr != nil || len(id) != 16 || key.CharacterID == 0 || b.MemberAccounts[key.CharacterID] == "" || life.Attempt == 0 || life.Deaths == 0 {
			return result, fmt.Errorf("Anton death journal authority invalid")
		}
		total += uint64(life.Deaths)
	}
	deaths := uint32(min(total, uint64(math.MaxUint32)))
	if deaths != b.PhaseDeaths[b.Phase] {
		return result, fmt.Errorf("Anton death total/journal conflict")
	}
	completed := b.PhaseCompletedAt
	if b.TimedOut {
		completed = b.Deadline
	}
	if completed.IsZero() || completed.Before(b.EnterAfter) || completed.After(b.Deadline) || b.PhaseCompleted && !completed.Before(b.Deadline) {
		return result, fmt.Errorf("Anton result completion timestamp invalid")
	}
	elapsed := completed.Sub(b.EnterAfter) / time.Second
	if elapsed < 0 || uint64(elapsed) > math.MaxUint32 {
		return result, fmt.Errorf("Anton result elapsed time invalid")
	}
	result = currentAntonResultProjection{Phase: b.Phase, Failed: b.TimedOut, ClearSeconds: uint32(elapsed), Deaths: deaths}
	wireDeaths := uint16(min(deaths, uint32(math.MaxUint16)))
	if b.TimedOut {
		result.Body, err = raid.BuildResultBody(true, b.Phase, result.ClearSeconds, wireDeaths, 0, rewardOption)
		return result, err
	}
	if b.Single {
		if _, err := prepareCurrentRaidFinalWeeklyCommand(state); err != nil {
			return result, err
		}
	} else if !b.Anton.PhaseCleared() || b.AntonRewards == nil || len(b.AntonRewards.Phases) != 2 {
		return result, fmt.Errorf("Anton success result has no cleared program/reward configuration")
	}
	result.DomainRank, err = b.AntonRewards.Phases[b.Phase].RankForDeaths(deaths)
	if err != nil {
		return result, err
	}
	result.FinalWeeklyRequired = b.Single || b.Phase == 1
	result.Body, err = raid.BuildAntonResultBody(false, b.Phase, result.ClearSeconds, wireDeaths, result.DomainRank, rewardOption)
	return result, err
}
