package dnfbridge

import (
	"context"
	"fmt"
	"time"

	"longheng.io/server/internal/modules/dnf/raid"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

type currentRaidClearedRun struct {
	DungeonID    uint32
	Phase        byte
	Attempt      uint64
	Participants []uint16
}

// Caller owns the reporting session's dungeon mutex. raidMu protects only the
// aggregate mutation; neither transport nor other session callbacks run inside
// it. The run was frozen server-side before entry, never supplied by op117.
func (s *Service) reduceCurrentRaidDungeonClearLocked(session *gameSession, runtime *runtimeDungeonState, now time.Time) (runtimeRaidState, bool, error) {
	if runtime == nil || runtime.raidEntry == nil {
		return runtimeRaidState{}, false, nil
	}
	if !s.currentRaidDungeonBindingRequired(session) || !s.currentRaidAuthenticated(session) ||
		session.dungeon.runtime != runtime || runtime.Session == nil || !runtime.bossDieCheckAccepted ||
		!runtime.bossDieCheckResponseSent || !runtime.settlementEntrySent ||
		runtime.Session.Snapshot().Run.Status != worldmap.DungeonRunCompleted ||
		!dungeonRuntimeOwnsCharacter(runtime, session.selectedCharacterID) {
		return runtimeRaidState{}, false, fmt.Errorf("raid clear has no owned completed settlement")
	}
	s.raidMu.Lock()
	defer s.raidMu.Unlock()
	state := s.runtimeRaidByMemberLocked(session.selectedCharacterID)
	e := runtime.raidEntry
	if state == nil || !state.Started || state.Battle == nil || state.RaidKey != e.RaidKey {
		return runtimeRaidState{}, false, fmt.Errorf("raid clear instance unavailable")
	}
	b := state.Battle
	if b.Anton != nil && now.Before(b.Anton.LastTime()) {
		now = b.Anton.LastTime()
	}
	if b.InstanceID != e.InstanceID || b.Phase != e.Phase || e.RunID == "" ||
		session.channel.ID != e.ChannelID || session.channel.Type != e.ChannelType ||
		b.MemberAccounts[session.selectedCharacterID] == "" || b.MemberAccounts[session.selectedCharacterID] != runtime.AccountID ||
		runtime.AccountID != s.accountIDForSession(session) || b.TimedOut || now.Before(b.EnterAfter) || !now.Before(b.Deadline) ||
		runtime.Dungeon.ID <= 0 || runtime.Dungeon.ID > int64(^uint32(0)) {
		return runtimeRaidState{}, false, fmt.Errorf("raid clear binding/owner expired")
	}
	dungeonID := uint32(runtime.Dungeon.ID)
	if run, exists := b.ClearedRuns[e.RunID]; exists {
		if run.DungeonID != dungeonID || run.Phase != e.Phase || run.Attempt != e.Attempt || !sameRaidParticipants(run.Participants, e.Participants) {
			return runtimeRaidState{}, false, fmt.Errorf("raid clear replay does not match frozen run")
		}
		return cloneRuntimeRaidState(*state), false, nil
	}
	if b.PhaseCompleted || len(e.Participants) == 0 {
		return runtimeRaidState{}, false, fmt.Errorf("raid clear phase/participants unavailable")
	}
	seen := map[uint16]bool{}
	for _, id := range e.Participants {
		if id == 0 || seen[id] || b.MemberAccounts[id] == "" {
			return runtimeRaidState{}, false, fmt.Errorf("raid clear participants were not admitted")
		}
		seen[id] = true
	}
	if !seen[session.selectedCharacterID] {
		return runtimeRaidState{}, false, fmt.Errorf("raid clear reporter was not in this dungeon")
	}
	if b.Single {
		if e.Attempt != 1 || len(e.Participants) != 1 || b.SingleIndex < 0 || b.SingleIndex >= len(b.SingleSequence) || b.SingleSequence[b.SingleIndex] != dungeonID {
			return runtimeRaidState{}, false, fmt.Errorf("single raid clear sequence mismatch")
		}
		next := append([]raid.DungeonState(nil), b.Dungeons...)
		found := false
		for i := range next {
			if next[i].ID == dungeonID && next[i].State == 0 {
				next[i].State, found = 1, true
			}
		}
		if !found {
			return runtimeRaidState{}, false, fmt.Errorf("single raid clear dungeon is not open")
		}
		nextIndex := b.SingleIndex + 1
		if nextIndex < len(b.SingleSequence) {
			found = false
			for i := range next {
				if next[i].ID == b.SingleSequence[nextIndex] {
					next[i].State, found = 0, true
				}
			}
			if !found {
				return runtimeRaidState{}, false, fmt.Errorf("single raid next dungeon missing from instance")
			}
		}
		b.Dungeons, b.SingleIndex = next, nextIndex
		b.PhaseCompleted = nextIndex == len(b.SingleSequence)
	} else {
		if !currentRaidHasTwoPhaseProgram(b) {
			return runtimeRaidState{}, false, fmt.Errorf("raid clear progression program unavailable")
		}
		// Commit elapsed timer effects even when they invalidate this clear.
		// Discarding Tick on an old-attempt error would preserve a dead phase.
		priorEpoch := b.Anton.Epoch()
		ticked, err := b.Anton.Tick(now)
		if err != nil {
			return runtimeRaidState{}, false, err
		}
		b.Anton, b.Dungeons = ticked, ticked.Dungeons()
		if ticked.Epoch() != e.Attempt {
			// Only the owned completed run whose current attempt was invalidated
			// by this Tick receives an exit receipt. An already-stale attempt,
			// another phase, or an unverified Luke timer cannot mint one.
			if b.Mode == 1 && b.Phase == 0 && priorEpoch == e.Attempt && ticked.Epoch() > priorEpoch {
				if b.ExpiredCompletedRuns == nil {
					b.ExpiredCompletedRuns = map[string]currentRaidClearedRun{}
				}
				b.ExpiredCompletedRuns[e.RunID] = currentRaidClearedRun{DungeonID: dungeonID, Phase: e.Phase, Attempt: e.Attempt, Participants: append([]uint16(nil), e.Participants...)}
			}
			b.ProgressVersion++
			return cloneRuntimeRaidState(*state), true, fmt.Errorf("raid clear belongs to expired attempt")
		}
		var next *raid.AntonProgression
		var replay bool
		if b.Mode == 2 {
			var luke *raid.LukeProgression
			luke, replay, err = (&raid.LukeProgression{AntonProgression: ticked}).ClearDungeon(e.RunID, dungeonID, e.Attempt, now)
			if err == nil {
				next = luke.AntonProgression
			}
		} else {
			next, replay, err = ticked.ClearDungeon(e.RunID, dungeonID, e.Attempt, now)
		}
		if err != nil {
			b.ProgressVersion++
			return cloneRuntimeRaidState(*state), true, err
		}
		if replay {
			return runtimeRaidState{}, false, fmt.Errorf("raid domain/aggregate clear journal mismatch")
		}
		b.Anton, b.Dungeons, b.PhaseCompleted = next, next.Dungeons(), next.PhaseCleared()
	}
	if b.ClearedRuns == nil {
		b.ClearedRuns = map[string]currentRaidClearedRun{}
	}
	if b.MemberClears == nil {
		b.MemberClears = map[uint16]uint32{}
	}
	b.ClearedRuns[e.RunID] = currentRaidClearedRun{DungeonID: dungeonID, Phase: e.Phase, Attempt: e.Attempt, Participants: append([]uint16(nil), e.Participants...)}
	for _, id := range e.Participants {
		b.MemberClears[id]++
	}
	if b.PhaseCompleted {
		b.PhaseCompletedAt = now
		if err := s.freezeCurrentAntonRewardFlowLocked(*state); err != nil {
			return cloneRuntimeRaidState(*state), true, err
		}
	}
	b.ProgressVersion++
	if b.PhaseCompleted && (b.Single || b.Phase == 1) {
		if err := s.freezeCurrentRaidFinalWeeklyIntentLocked(*state); err != nil {
			return cloneRuntimeRaidState(*state), true, err
		}
	}
	return cloneRuntimeRaidState(*state), true, nil
}

func sameRaidParticipants(a, b []uint16) bool {
	if len(a) != len(b) {
		return false
	}
	set := make(map[uint16]bool, len(a))
	for _, id := range a {
		if set[id] {
			return false
		}
		set[id] = true
	}
	for _, id := range b {
		if !set[id] {
			return false
		}
		delete(set, id)
	}
	return len(set) == 0
}

// Map changes do not replay NOTI587's battle-start animation. Queue to each
// session's event loop after the boss result callback releases its mutex. A
// replaced instance/phase is skipped. Same-phase callbacks select the latest
// cumulative journal, so coalescing maps never drops transient symbol writes.
func (s *Service) queueCurrentRaidDungeonProgress(state runtimeRaidState) {
	if state.Battle == nil {
		return
	}
	s.queueCurrentRaidWeeklyClears()
	for _, member := range state.Members {
		peer, found := s.onlineGameSession(member.CharID)
		if !found {
			continue
		}
		id, generation := member.CharID, peer.characterGeneration
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
			defer cancel()
			err := s.callGameSession(ctx, peer, "raid-owned-dungeon-progress", func() error {
				if peer.selectedCharacterID != id || peer.characterGeneration != generation || !s.currentRaidAuthenticated(peer) {
					return nil
				}
				s.raidMu.Lock()
				live := s.runtimeRaidByMemberLocked(id)
				valid := live != nil && live.Started && live.RaidKey == state.RaidKey && live.Battle != nil &&
					live.Battle.InstanceID == state.Battle.InstanceID && live.Battle.Phase == state.Battle.Phase &&
					live.Battle.ProgressVersion >= state.Battle.ProgressVersion &&
					live.Battle.MemberAccounts[id] == s.accountIDForSession(peer)
				var latest runtimeRaidState
				if valid {
					latest = cloneRuntimeRaidState(*live)
				}
				s.raidMu.Unlock()
				if !valid {
					return nil
				}
				if latest.Battle.Phase == 1 && !latest.Battle.PhaseCompleted && !latest.Battle.TimedOut {
					if err := s.sendCurrentRaidBattleState(peer, latest); err != nil {
						return err
					}
				} else if err := s.sendCurrentRaidProgressProjection(peer, latest, false, false); err != nil {
					return err
				}
				if err := s.sendCurrentAntonTerminalResult(peer, latest); err != nil {
					return err
				}
				return s.sendCurrentAntonRewardFlowProjection(peer, latest)
			})
			if err != nil {
				s.logGameEvent(peer, "game-raid-dungeon-progress-send-failed", "raid_key", state.RaidKey, "error", err)
			}
		}()
	}
}
