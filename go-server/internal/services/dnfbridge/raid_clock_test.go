package dnfbridge

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/raid"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestRaidClockUsesGameplayQueueAndStopsAfterCancellation(t *testing.T) {
	q := newFakeCurrentDungeonDeathTimerQueue()
	s := &Service{options: options{currentClientVersion: "official-24.9"}, gameplayTimers: q}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.startCurrentRaidClock(ctx)
	first := q.task(t, 0)
	if first.name != currentRaidClockName || first.delay != time.Second {
		t.Fatal("raid clock not owned by gameplay queue", first)
	}
	q.fire(first, false)
	if count, _, _ := q.counts(); count != 2 {
		t.Fatal("raid clock did not reschedule once", count)
	}
	cancel()
	q.fire(q.task(t, 1), false)
	if count, _, _ := q.counts(); count != 2 {
		t.Fatal("cancelled raid clock rescheduled", count)
	}
	legacy := &Service{gameplayTimers: q}
	legacy.startCurrentRaidClock(context.Background())
	if count, _, _ := q.counts(); count != 2 {
		t.Fatal("legacy profile scheduled raid clock", count)
	}
}

func TestRaidClockExpirationNeverCreatesClearOrRewardEligibility(t *testing.T) {
	f := raidSafetyFixture(t, nil, 1)
	f.service.options.currentClientVersion = "official-24.9"
	now := time.Now()
	b := &runtimeRaidBattle{InstanceID: "00112233445566778899aabbccddeeff", Single: true, EnterAfter: now.Add(-time.Minute), Deadline: now,
		Dungeons: []raid.DungeonState{{ID: 5314, State: 0}}, SingleSequence: []uint32{5314}, MemberAccounts: map[uint16]string{19: "account-1"}}
	f.state.Started, f.state.Battle = true, b
	states, errs := f.service.advanceCurrentRaidClocks(now)
	if len(errs) != 0 || len(states) != 1 || !b.TimedOut || b.PhaseCompleted || b.Dungeons[0].State != 2 || len(b.ClearedRuns) != 0 || len(b.MemberClears) != 0 {
		t.Fatal("expiration changed clear/reward state", states, errs, b)
	}
	states, errs = f.service.advanceCurrentRaidClocks(now.Add(time.Second))
	if len(errs) != 0 || len(states) != 0 || b.ProgressVersion != 1 {
		t.Fatal("timeout notification repeated", states, errs)
	}
}

func TestRealPVFRaidClockReplaysDeadlinesWithoutDuplicateProjection(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("requires current production PVF")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	program, err := raid.LoadAntonProgram(archive)
	if err != nil {
		t.Fatal(err)
	}
	f := raidSafetyFixture(t, archive, 1)
	f.service.options.currentClientVersion = "official-24.9"
	now := time.Now()
	anton, err := program.BeginPhase(0, now, 1837)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		anton, _, err = anton.ClearDungeon(fmt.Sprint(i), 210, anton.Epoch(), now)
		if err != nil {
			t.Fatal(err)
		}
	}
	b := &runtimeRaidBattle{InstanceID: "00112233445566778899aabbccddeeff", Mode: 1, Anton: anton, EnterAfter: now, Deadline: now.Add(time.Hour), Dungeons: anton.Dungeons()}
	f.state.Started, f.state.Battle = true, b
	if states, errs := f.service.advanceCurrentRaidClocks(now.Add(time.Second)); len(states) != 0 || len(errs) != 0 {
		t.Fatal("clock-only advance emitted projection", states, errs)
	}
	states, errs := f.service.advanceCurrentRaidClocks(now.Add(20 * time.Second))
	if len(states) != 1 || len(errs) != 0 {
		t.Fatal("reserved cannon did not publish", states, errs)
	}
	if timer := b.Anton.Timers()[raid.AntonTimerKey{Kind: 1, DungeonID: 216}]; timer.Duration != 360 {
		t.Fatal("cannon timer not from actual PVF", timer)
	}
	if states, errs := f.service.advanceCurrentRaidClocks(now.Add(21 * time.Second)); len(states) != 0 || len(errs) != 0 {
		t.Fatal("unchanged cannon projection repeated", states, errs)
	}
	states, errs = f.service.advanceCurrentRaidClocks(now.Add(301 * time.Second))
	if len(states) != 1 || len(errs) != 0 || b.Anton.Epoch() != 2 || b.Anton.ClearCount(210) != 0 {
		t.Fatal("delayed clock failed phase reset", states, errs)
	}
	if states, errs := f.service.advanceCurrentRaidClocks(now.Add(300 * time.Second)); len(states) != 0 || len(errs) != 0 {
		t.Fatal("earlier callback mutated current clock", states, errs)
	}
}
