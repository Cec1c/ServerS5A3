package dnfbridge

import (
	"context"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"time"

	"longheng.io/server/internal/modules/dnf/raid"
)

type currentRaidWeeklyIntent struct {
	Command     raid.WeeklyClearCommand
	Running     bool
	Committed   bool
	Attempts    uint32
	NextAttempt time.Time
}

// Only the actual completed-run reducer freezes this intent. A lobby start,
// first normal phase, timeout, reward-card request or disconnected player
// cannot manufacture a successful raid. Accounts and participants were frozen
// before dungeon entry; the repository owner rechecks character ownership.
func prepareCurrentRaidFinalWeeklyCommand(state runtimeRaidState) (raid.WeeklyClearCommand, error) {
	b := state.Battle
	if !state.Started || b == nil || !b.PhaseCompleted || b.TimedOut || b.PhaseCompletedAt.IsZero() ||
		b.PhaseCompletedAt.Before(b.EnterAfter) || !b.PhaseCompletedAt.Before(b.Deadline) ||
		b.Configuration.WeeklyClearLimit < 1 || b.Configuration.WeeklyClearLimit > 255 ||
		b.Mode < 1 || b.Mode > 2 || len(b.ClearedRuns) == 0 || len(b.ClearedRuns) > 4096 {
		return raid.WeeklyClearCommand{}, fmt.Errorf("raid weekly caller has no authoritative final clear")
	}
	instance, err := hex.DecodeString(b.InstanceID)
	if err != nil || len(instance) != 16 || hex.EncodeToString(instance) != b.InstanceID {
		return raid.WeeklyClearCommand{}, raid.ErrWeeklyClearState
	}
	minimum := uint32(1)
	if b.Single {
		if b.Phase != 0 || len(b.SingleSequence) == 0 || b.SingleIndex != len(b.SingleSequence) || len(b.MemberAccounts) != 1 {
			return raid.WeeklyClearCommand{}, fmt.Errorf("single raid weekly sequence incomplete")
		}
	} else {
		if !currentRaidHasTwoPhaseProgram(b) || b.Phase != 1 || !b.Anton.PhaseCleared() || b.AntonRewards == nil || len(b.AntonRewards.Phases) != 2 {
			return raid.WeeklyClearCommand{}, fmt.Errorf("normal raid weekly final phase unavailable")
		}
		minimum = b.AntonRewards.Phases[1].MinimumClears
		if minimum == 0 {
			return raid.WeeklyClearCommand{}, raid.ErrWeeklyClearState
		}
	}
	counts := map[uint16]uint32{}
	dungeons := map[uint32]bool{}
	for id, run := range b.ClearedRuns {
		if id == "" || run.DungeonID == 0 || run.Attempt == 0 || len(run.Participants) == 0 {
			return raid.WeeklyClearCommand{}, raid.ErrWeeklyClearState
		}
		if run.Phase != b.Phase {
			continue
		}
		seen := map[uint16]bool{}
		for _, id := range run.Participants {
			if id == 0 || seen[id] || b.MemberAccounts[id] == "" {
				return raid.WeeklyClearCommand{}, raid.ErrWeeklyClearState
			}
			seen[id] = true
			counts[id]++
		}
		dungeons[run.DungeonID] = true
	}
	if b.Single {
		if len(dungeons) != len(b.SingleSequence) {
			return raid.WeeklyClearCommand{}, raid.ErrWeeklyClearState
		}
		for _, id := range b.SingleSequence {
			if !dungeons[id] {
				return raid.WeeklyClearCommand{}, raid.ErrWeeklyClearState
			}
		}
	}
	members := []raid.ClearMember{}
	for id, clears := range counts {
		if clears >= minimum {
			members = append(members, raid.ClearMember{CharacterID: strconv.Itoa(int(id)), AccountID: b.MemberAccounts[id]})
		}
	}
	if len(members) == 0 || len(members) > raid.MaxAttackPartyMembers {
		return raid.WeeklyClearCommand{}, raid.ErrWeeklyClearState
	}
	sort.Slice(members, func(i, j int) bool { return members[i].CharacterID < members[j].CharacterID })
	policy, err := currentRaidWeeklyPolicy()
	if err != nil {
		return raid.WeeklyClearCommand{}, err
	}
	return raid.WeeklyClearCommand{Mode: b.Mode, Limit: b.Configuration.WeeklyClearLimit, InstanceID: b.InstanceID, Members: members, CompletedAt: b.PhaseCompletedAt, Policy: policy}, nil
}

// Caller holds raidMu. The copied entitlement survives lobby deletion and
// disconnection during a retry. It is an in-process pending intent; persistence
// of the weekly result and replay marker is owned by CommitWeeklyClear.
func (s *Service) freezeCurrentRaidFinalWeeklyIntentLocked(state runtimeRaidState) error {
	command, err := prepareCurrentRaidFinalWeeklyCommand(state)
	if err != nil {
		return err
	}
	if s.raidWeeklyIntents == nil {
		s.raidWeeklyIntents = map[string]*currentRaidWeeklyIntent{}
	}
	if old := s.raidWeeklyIntents[command.InstanceID]; old != nil {
		if old.Command.Mode != command.Mode || old.Command.Limit != command.Limit || old.Command.Policy != command.Policy || !old.Command.CompletedAt.Equal(command.CompletedAt) || len(old.Command.Members) != len(command.Members) {
			return raid.ErrWeeklyClearState
		}
		for i, m := range command.Members {
			if old.Command.Members[i] != m {
				return raid.ErrWeeklyClearState
			}
		}
		return nil
	}
	s.raidWeeklyIntents[command.InstanceID] = &currentRaidWeeklyIntent{Command: command}
	return nil
}

func (s *Service) commitCurrentRaidWeeklyIntents(now time.Time) ([]raid.WeeklyClearCommand, []error) {
	if s == nil || now.IsZero() {
		return nil, nil
	}
	s.raidMu.Lock()
	type pending struct {
		key     string
		command raid.WeeklyClearCommand
	}
	jobs := []pending{}
	for key, intent := range s.raidWeeklyIntents {
		if intent == nil || intent.Committed || intent.Running || now.Before(intent.NextAttempt) {
			continue
		}
		intent.Running = true
		command := intent.Command
		command.Members = append([]raid.ClearMember(nil), command.Members...)
		jobs = append(jobs, pending{key, command})
	}
	s.raidMu.Unlock()
	var completed []raid.WeeklyClearCommand
	var errs []error
	for _, job := range jobs {
		repositories, available := s.repositoryGroup()
		var err error
		if !available {
			err = raid.ErrWeeklyClearState
		} else {
			ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
			_, err = raid.CommitWeeklyClear(ctx, repositories, job.command)
			cancel()
		}
		s.raidMu.Lock()
		intent := s.raidWeeklyIntents[job.key]
		if intent != nil {
			intent.Running = false
			if err == nil {
				intent.Committed = true
			} else {
				if intent.Attempts < 5 {
					intent.Attempts++
				}
				intent.NextAttempt = now.Add(time.Second * time.Duration(uint32(1)<<intent.Attempts))
			}
		}
		s.raidMu.Unlock()
		if err != nil {
			errs = append(errs, fmt.Errorf("raid final weekly commit %s: %w", job.key, err))
		} else {
			completed = append(completed, job.command)
		}
	}
	return completed, errs
}

// DB work and other-session transport run after the dungeon/raid mutexes are
// released. One bounded worker batch is admitted at a time. The process raid
// clock retries failures even if no maps have changed or a lobby was deleted.
func (s *Service) queueCurrentRaidWeeklyClears() {
	if s == nil || s.options.currentClientVersion != "official-24.9" {
		return
	}
	s.raidMu.Lock()
	if s.raidWeeklyWorkRunning || len(s.raidWeeklyIntents) == 0 {
		s.raidMu.Unlock()
		return
	}
	s.raidWeeklyWorkRunning = true
	s.raidMu.Unlock()
	go func() {
		defer func() { s.raidMu.Lock(); s.raidWeeklyWorkRunning = false; s.raidMu.Unlock() }()
		completed, errs := s.commitCurrentRaidWeeklyIntents(time.Now())
		for _, err := range errs {
			s.logWarn("raid weekly commit deferred", "error", err)
		}
		for _, command := range completed {
			s.queueCurrentAntonResultsAfterWeeklyCommit(command)
			for _, member := range command.Members {
				id, err := strconv.ParseUint(member.CharacterID, 10, 16)
				if err != nil {
					continue
				}
				peer, found := s.onlineGameSession(uint16(id))
				if !found {
					continue
				}
				generation := peer.characterGeneration
				ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
				err = s.callGameSession(ctx, peer, "raid-final-weekly-committed", func() error {
					if peer.selectedCharacterID != uint16(id) || peer.characterGeneration != generation || s.accountIDForSession(peer) != member.AccountID {
						return nil
					}
					return s.sendCurrentRaidWeeklyAvailability(peer, "authoritative_final_raid_clear")
				})
				cancel()
				if err != nil {
					s.logGameEvent(peer, "game-raid-final-weekly-notice-failed", "error", err)
				}
			}
		}
	}()
}
