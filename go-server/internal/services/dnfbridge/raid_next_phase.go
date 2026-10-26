package dnfbridge

import (
	"fmt"
	"math"
	"time"

	"longheng.io/server/internal/modules/dnf/raid"
)

func currentRaidHasTwoPhaseProgram(b *runtimeRaidBattle) bool {
	return b != nil && b.Anton != nil && ((b.Mode == 1 && !b.Anton.IsLuke()) || (b.Mode == 2 && b.Anton.IsLuke()))
}

// raidMu is held. No admission material or weekly count is touched here.
func (s *Service) beginCurrentAntonNextPhaseLocked(state *runtimeRaidState, now time.Time) (bool, error) {
	if state == nil || !state.Started || state.Battle == nil {
		return false, nil
	}
	b := state.Battle
	if !currentRaidHasTwoPhaseProgram(b) || b.Single || b.Phase != 0 || !b.PhaseCompleted || b.TimedOut || len(b.Configuration.PhaseLimitSeconds) != 2 {
		return false, nil
	}
	f := b.RewardFlow[0]
	if f == nil || f.InstanceID != b.InstanceID || f.Phase != 0 || !currentRaidRewardFlowSettlementReady(f, now) {
		return false, nil
	}
	if len(f.Recipients) == 0 || !f.CompletedAt.Equal(b.PhaseCompletedAt) {
		return false, nil
	}
	for _, owner := range f.Recipients {
		party, squad := f.Party[owner.CharacterID], f.Squad[owner.CharacterID]
		if party == nil || !party.Committed || f.squadRequired() && (squad == nil || !squad.Committed) {
			return false, nil
		}
	}
	if len(state.Members) == 0 || runtimeRaidHasOversizedGroup(*state) {
		return false, fmt.Errorf("Anton next phase party unavailable")
	}
	seen := map[uint16]bool{}
	for _, member := range state.Members {
		if member.CharID == 0 || seen[member.CharID] || b.MemberAccounts[member.CharID] == "" || member.GroupIndex < 1 || member.GroupIndex > runtimeRaidMaxGroups {
			return false, fmt.Errorf("Anton next phase member not admitted")
		}
		seen[member.CharID] = true
	}
	// Luke's accepted two-phase flow resumes only after every current member
	// has confirmed return to this battle's waiting area. Scene registration
	// is restored by the selector-return path; online identity alone is not
	// proof that the client has left its previous dungeon/selector.
	if b.Mode == 2 {
		leader, online := s.onlineGameSession(state.LeaderID)
		if !online {
			return false, errCurrentRaidMembersOutsideWaitingArea
		}
		waitingScope, err := s.prepareCurrentRaidWaitingBattleScope(leader, *state, b)
		if err != nil {
			return false, fmt.Errorf("%w: %v", errCurrentRaidMembersOutsideWaitingArea, err)
		}
		if err := s.onlinePlayers.requireRaidStartWaitingArea(waitingScope, b.InstanceID); err != nil {
			return false, err
		}
	}
	enter := now.Add(time.Duration(b.Configuration.StartDelaySeconds) * time.Second)
	deadline := enter.Add(time.Duration(b.Configuration.PhaseLimitSeconds[1]) * time.Second)
	if deadline.Unix() > math.MaxUint32 || deadline.Unix() <= 0 {
		return false, fmt.Errorf("Anton next phase native deadline invalid")
	}
	var next *raid.AntonProgression
	var err error
	if b.Mode == 2 {
		var luke *raid.LukeProgression
		luke, err = (&raid.LukeProgression{AntonProgression: b.Anton}).NextPhase(enter)
		if err == nil {
			next = luke.AntonProgression
		}
	} else {
		next, err = b.Anton.BeginNextPhase(enter)
	}
	if err != nil {
		return false, err
	}
	b.Phase, b.StartedAt, b.EnterAfter, b.Deadline = 1, now, enter, deadline
	b.Anton, b.Dungeons = next, next.Dungeons()
	b.PhaseCompleted, b.PhaseCompletedAt, b.TimedOut, b.ProgressVersion = false, time.Time{}, false, 0
	b.MemberClears = map[uint16]uint32{}
	b.RunCoins = map[string]uint32{}
	if b.MemberGroups == nil {
		b.MemberGroups = map[uint16]byte{}
	}
	for _, member := range state.Members {
		b.MemberGroups[member.CharID] = member.GroupIndex
	}
	return true, nil
}
