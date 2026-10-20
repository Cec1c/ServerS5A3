package dnfbridge

import (
	"fmt"

	"longheng.io/server/internal/modules/dnf/raid"
)

// Read actual accepted run participants for this phase. MemberClears is a
// cumulative UI field and cannot establish per-phase reward eligibility.
func currentAntonPhaseClearCount(b *runtimeRaidBattle, id uint16) uint32 {
	var count uint32
	for _, run := range b.ClearedRuns {
		if run.Phase != b.Phase {
			continue
		}
		for _, member := range run.Participants {
			if member == id {
				count++
				break
			}
		}
	}
	return count
}

// Caller holds raidMu. Compare the committed receipt's frozen command with
// this exact final instance, rather than relying on a boolean from another run.
func (s *Service) currentAntonFinalWeeklyCommittedLocked(state runtimeRaidState) bool {
	command, err := prepareCurrentRaidFinalWeeklyCommand(state)
	if err != nil {
		return false
	}
	intent := s.raidWeeklyIntents[command.InstanceID]
	if intent == nil || !intent.Committed || intent.Command.Mode != command.Mode || intent.Command.Limit != command.Limit || intent.Command.Policy != command.Policy || !intent.Command.CompletedAt.Equal(command.CompletedAt) || len(intent.Command.Members) != len(command.Members) {
		return false
	}
	for i, member := range command.Members {
		if member != intent.Command.Members[i] {
			return false
		}
	}
	return true
}

// Called in the recipient event loop, after current map/symbol projection.
// The raid projection mutex serializes the result and phase transition with
// starts/rejoins; no repository or transport runs while raidMu is held.
func (s *Service) sendCurrentAntonTerminalResult(session *gameSession, captured runtimeRaidState) error {
	if s == nil || s.options.currentClientVersion != "official-24.9" || captured.Battle == nil || (captured.Battle.Mode != 1 && captured.Battle.Mode != 2) || (!captured.Battle.PhaseCompleted && !captured.Battle.TimedOut) {
		return nil
	}
	if !s.currentRaidAuthenticated(session) {
		return fmt.Errorf("Anton result current owner unavailable")
	}
	o := &session.raidProjection
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.failed {
		return fmt.Errorf("Anton result transport previously failed")
	}
	state, err := s.latestCurrentRaidProjectionState(session, captured)
	if err != nil {
		return err
	}
	b := state.Battle
	identity := currentRaidProjectionIdentity{InstanceID: b.InstanceID, Phase: b.Phase, CharID: session.selectedCharacterID, Generation: session.characterGeneration}
	if o.resultSent && o.resultIdentity == identity {
		return nil
	}
	option := byte(1)
	if !b.TimedOut && b.AntonRewards != nil && int(b.Phase) < len(b.AntonRewards.Phases) && currentAntonPhaseClearCount(b, session.selectedCharacterID) >= b.AntonRewards.Phases[b.Phase].MinimumClears {
		option = 0
	}
	result, err := prepareCurrentAntonResultProjection(state, option)
	if err != nil {
		return err
	}
	if result.FinalWeeklyRequired {
		s.raidMu.Lock()
		live := s.raids[state.RaidKey]
		committed := live != nil && live.Battle != nil && live.Battle.InstanceID == b.InstanceID && live.Battle.Phase == b.Phase && s.currentAntonFinalWeeklyCommittedLocked(*live)
		s.raidMu.Unlock()
		if !committed {
			return nil
		}
	}
	if !result.Failed && option == 0 {
		if err := s.applyCurrentAntonPrerequisiteQuestResult(session, state); err != nil {
			return err
		}
	}
	packets := []currentRaidProjectionPacket{{opcode: 601, body: result.Body}}
	if !result.Failed {
		// NOTI587's second field is stateArgument. In battle state2 it
		// selects the phase animation; successful result state3 uses zero.
		// NOTI601 carries the actual result phase independently.
		packets = append(packets, currentRaidProjectionPacket{opcode: 587, body: []byte{3, 0}})
	}
	for _, packet := range packets {
		if err := s.sendGameUpperRawClassCodec(session, packet.opcode, packet.body, 0, true); err != nil {
			o.failed = true
			if session.conn != nil {
				_ = session.conn.Close()
			}
			return err
		}
	}
	o.resultIdentity, o.resultSent = identity, true
	s.logGameEvent(session, "game-raid-authoritative-result-sent", "raid_key", state.RaidKey, "phase", b.Phase, "failed", result.Failed, "seconds", result.ClearSeconds, "deaths", result.Deaths, "rank", result.DomainRank, "reward_option", option)
	return nil
}

// A successful DB batch releases all frozen members' final result, including
// an observer who lacked reward eligibility. Never publish on a failed batch.
func (s *Service) queueCurrentAntonResultsAfterWeeklyCommit(command raid.WeeklyClearCommand) {
	if command.Mode != 1 && command.Mode != 2 {
		return
	}
	s.raidMu.Lock()
	var states []runtimeRaidState
	for _, state := range s.raids {
		if state != nil && state.Battle != nil && state.Battle.InstanceID == command.InstanceID && (state.Battle.Single || state.Battle.Phase == 1) && s.currentAntonFinalWeeklyCommittedLocked(*state) {
			states = append(states, cloneRuntimeRaidState(*state))
		}
	}
	s.raidMu.Unlock()
	for _, state := range states {
		s.queueCurrentRaidDungeonProgress(state)
	}
}
