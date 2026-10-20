package dnfbridge

import (
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestRealPVFAntonOwnedResultUsesFrozenServerDeathsAndActualRanks(t *testing.T) {
	_, state, _ := actualFinalAntonWeeklyFixture(t)
	var runID string
	for id := range state.Battle.ClearedRuns {
		runID = id
		break
	}
	// Serialization variations are isolated memory journals, with current
	// phase/owner/run authority from the actual completed callback fixture.
	for _, tc := range []struct {
		deaths uint32
		rank   byte
	}{{0, 0}, {2, 1}, {6, 2}, {10, 3}, {15, 3}, {65536, 3}, {math.MaxUint32, 3}} {
		copy := cloneRuntimeRaidState(state)
		copy.Battle.PhaseDeaths[1] = tc.deaths
		copy.Battle.MemberLives = map[currentRaidMemberRun]currentRaidMemberLife{}
		if tc.deaths > 0 {
			copy.Battle.MemberLives[currentRaidMemberRun{RunID: runID, CharacterID: 19}] = currentRaidMemberLife{Phase: 1, Attempt: 1, Deaths: tc.deaths}
		}
		result, err := prepareCurrentAntonResultProjection(copy, 0)
		if err != nil || result.Deaths != tc.deaths || result.DomainRank != tc.rank || !result.FinalWeeklyRequired || result.ClearSeconds != 6 {
			t.Fatal("result changed authoritative statistics", tc, result, err)
		}
		if len(result.Body) != 10 || result.Body[0] != 0 || result.Body[1] != 1 || binary.LittleEndian.Uint16(result.Body[6:8]) != uint16(min(tc.deaths, uint32(math.MaxUint16))) || result.Body[8] != tc.rank+2 {
			t.Fatal("native result wrapped deaths or confused rank", result.Body)
		}
		if dir := os.Getenv("DNF90_ANTON_NATIVE_DIR"); dir != "" {
			if err := os.MkdirAll(dir, 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "go-anton-owned-result-deaths-"+strconv.FormatUint(uint64(tc.deaths), 10)+".bin"), result.Body, 0644); err != nil {
				t.Fatal(err)
			}
		}
	}
	timeout, _, start := realAntonProjectionFixture(t, 1)
	timeout.state.Battle.Deadline = start.Add(time.Duration(timeout.state.Battle.Configuration.PhaseLimitSeconds[1]) * time.Second)
	expired, clockErrors := timeout.service.advanceCurrentRaidClocks(timeout.state.Battle.Deadline)
	if len(clockErrors) != 0 || len(expired) != 1 || !expired[0].Battle.TimedOut {
		t.Fatal("real phase clock did not expire", clockErrors)
	}
	failure := expired[0]
	result, err := prepareCurrentAntonResultProjection(failure, 1)
	if err != nil || !result.Failed || result.FinalWeeklyRequired || result.ClearSeconds != 2400 || result.Body[0] != 1 || result.Body[8] != 0 {
		t.Fatal("timeout prepared successful result", result, err)
	}
	if dir := os.Getenv("DNF90_ANTON_NATIVE_DIR"); dir != "" {
		if err := os.WriteFile(filepath.Join(dir, "go-anton-owned-result-failure.bin"), result.Body, 0644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRealPVFAntonOwnedResultRejectsIncompleteOrInconsistentStatistics(t *testing.T) {
	_, state, _ := actualFinalAntonWeeklyFixture(t)
	for _, name := range []string{"not_started", "single", "mode", "phase", "wrong_program_phase", "extended_clock", "still_running", "both_terminal", "cleared_timeout", "missing_time", "too_early", "after_deadline", "uncommitted_at_deadline", "no_program_clear", "no_rewards", "no_rank", "death_conflict", "foreign_death", "bad_run", "zero_death_receipt", "bad_option"} {
		t.Run(name, func(t *testing.T) {
			copy := cloneRuntimeRaidState(state)
			b := copy.Battle
			option := byte(0)
			switch name {
			case "not_started":
				copy.Started = false
			case "single":
				b.Single = true
			case "mode":
				b.Mode = 2
			case "phase":
				b.Phase = 2
			case "wrong_program_phase":
				b.Phase = 0
			case "extended_clock":
				b.Deadline = b.Deadline.Add(time.Second)
			case "still_running":
				b.PhaseCompleted = false
			case "both_terminal":
				b.TimedOut = true
			case "cleared_timeout":
				b.TimedOut, b.PhaseCompleted = true, false
			case "missing_time":
				b.PhaseCompletedAt = time.Time{}
			case "too_early":
				b.PhaseCompletedAt = b.EnterAfter.Add(-time.Second)
			case "after_deadline":
				b.PhaseCompletedAt = b.Deadline.Add(time.Second)
			case "uncommitted_at_deadline":
				b.PhaseCompletedAt = b.Deadline
			case "no_program_clear":
				b.Anton = nil
			case "no_rewards":
				b.AntonRewards = nil
			case "no_rank":
				b.AntonRewards.Phases[1].Ranks = nil
			case "death_conflict":
				b.PhaseDeaths[1] = 1
			case "foreign_death":
				b.MemberLives = map[currentRaidMemberRun]currentRaidMemberLife{{RunID: "112233445566778899aabbccddeeff00", CharacterID: 99}: {Phase: 1, Attempt: 1, Deaths: 1}}
			case "bad_run":
				b.MemberLives = map[currentRaidMemberRun]currentRaidMemberLife{{RunID: "invalid", CharacterID: 19}: {Phase: 1, Attempt: 1, Deaths: 1}}
			case "zero_death_receipt":
				b.MemberLives = map[currentRaidMemberRun]currentRaidMemberLife{{RunID: "112233445566778899aabbccddeeff00", CharacterID: 19}: {Phase: 1, Attempt: 1}}
			case "bad_option":
				option = 2
			}
			if _, err := prepareCurrentAntonResultProjection(copy, option); err == nil {
				t.Fatal("invalid result accepted", name)
			}
		})
	}
}
