package dnfbridge

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/raid"
)

var errCurrentAntonRewardFrozenGroupCapacity = errors.New("Anton qualified reward group exceeds four native cards")

// raidMu is held. The existing caller keeps its error/retry behavior; this
// bounded record adds the instance and phase that redacted error hashes omit.
func (s *Service) logCurrentAntonRewardFreezeFailureLocked(state runtimeRaidState, err error, now time.Time) {
	b := state.Battle
	if s == nil || err == nil || b == nil || b.FiendWar != nil || b.Phase > 1 || now.IsZero() {
		return
	}
	phase := b.Phase
	last := b.RewardFreezeDiagnosticAt[phase]
	if !last.IsZero() && now.Before(last.Add(10*time.Second)) {
		b.RewardFreezeDiagnosticSuppressed[phase]++
		return
	}
	reason := "freeze_failure"
	switch {
	case errors.Is(err, errCurrentAntonRewardFrozenGroupCapacity):
		reason = "frozen_group_capacity"
	case errors.Is(err, raid.ErrRewardOwnership):
		reason = "frozen_owner_invalid"
	case errors.Is(err, raid.ErrRewardIneligible):
		reason = "reward_ineligible"
	}
	var groups [runtimeRaidMaxGroups + 1]int
	qualified, invalid := 0, 0
	if b.AntonRewards != nil && int(phase) < len(b.AntonRewards.Phases) {
		for id := range b.MemberAccounts {
			if currentAntonPhaseClearCount(b, id) < b.AntonRewards.Phases[phase].MinimumClears {
				continue
			}
			qualified++
			group := b.MemberGroups[id]
			if group < 1 || group > runtimeRaidMaxGroups {
				invalid++
				continue
			}
			groups[group]++
		}
	}
	parts := make([]string, 0, runtimeRaidMaxGroups)
	for group := 1; group <= runtimeRaidMaxGroups; group++ {
		parts = append(parts, fmt.Sprintf("%d:%d", group, groups[group]))
	}
	suppressed := b.RewardFreezeDiagnosticSuppressed[phase]
	b.RewardFreezeDiagnosticAt[phase], b.RewardFreezeDiagnosticSuppressed[phase] = now, 0
	s.logWarn("dnfbridge raid reward freeze rejected",
		"kind", "game-raid-reward-freeze-rejected", "raid_key", state.RaidKey,
		"instance_id", b.InstanceID, "mode", b.Mode, "phase", phase, "reason", reason,
		"qualified_recipients", qualified, "live_members", len(state.Members),
		"frozen_groups", strings.Join(parts, ","), "invalid_groups", invalid, "suppressed", suppressed)
}
