package dnfbridge

import (
	"context"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/raid"
	repo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// Real PVF mechanics and the owned completed-runtime callback are exercised
// with in-memory characters; these are not physical client clears.
func actualFinalAntonWeeklyFixture(t *testing.T) (*raidStartGuardFixture, runtimeRaidState, time.Time) {
	t.Helper()
	catalog, rewards := actualAntonRewardFixture(t)
	archive, ok := catalog.source.(*platformpvf.Archive)
	if !ok {
		t.Fatal("actual archive required")
	}
	config, err := raid.LoadBattleConfiguration(archive, "etc/raid/anton.etc")
	if err != nil {
		t.Fatal(err)
	}
	program, err := raid.LoadAntonProgram(archive)
	if err != nil {
		t.Fatal(err)
	}
	f := raidSafetyFixture(t, archive, 3)
	f.service.options.currentClientVersion = "official-24.9"
	leader, peer := f.session(), f.session()
	leader.loginPacketSeen, leader.accountFromToken = true, true
	peer.selectedCharacterID, peer.accountID = 20, "account-20"
	peer.loginPacketSeen, peer.accountFromToken = true, true
	f.service.bindGameSessionCharacter(leader, 19)
	f.service.bindGameSessionCharacter(peer, 20)
	for i := range f.state.Members {
		f.state.Members[i].GroupIndex = 1
	}
	now := time.Now()
	base := now.Add(-20 * time.Second)
	anton, err := program.BeginPhase(1, base, 91321)
	if err != nil {
		t.Fatal(err)
	}
	b := &runtimeRaidBattle{InstanceID: "00112233445566778899aabbccddeeff", Mode: 1, Phase: 1, ChannelID: leader.channel.ID, ChannelType: leader.channel.Type, StartedAt: base, EnterAfter: base, Deadline: base.Add(time.Duration(config.PhaseLimitSeconds[1]) * time.Second), Configuration: config, Anton: anton, AntonRewards: &rewards, Dungeons: anton.Dungeons(), MemberAccounts: map[uint16]string{19: "account-1", 20: "account-20", 21: "account-21"}}
	f.state.Started, f.state.Battle = true, b
	var final runtimeRaidState
	for n, id := range []uint32{219, 220, 220, 220, 220, 220} {
		l, p := raidCompletedRuntimeFixture(t, leader, id), raidCompletedRuntimeFixture(t, peer, id)
		if err := f.service.freezeCurrentRaidDungeonGroupEntry(leader, l, []*runtimePartyPreparedDungeonEntry{{session: peer, runtime: p}}); err != nil {
			t.Fatal(n, id, err)
		}
		at := base.Add(time.Duration(n+1) * time.Second)
		snapshot, changed, err := f.service.reduceCurrentRaidDungeonClearLocked(leader, l, at)
		if err != nil || !changed {
			t.Fatal("actual final reducer", n, id, changed, err)
		}
		if _, changed, err := f.service.reduceCurrentRaidDungeonClearLocked(peer, p, at); err != nil || changed {
			t.Fatal("duplicate peer clear", n, changed, err)
		}
		if n < 5 && (b.PhaseCompleted || len(f.service.raidWeeklyIntents) != 0) {
			t.Fatal("early heart/volcano counted final")
		}
		final = snapshot
	}
	if !final.Battle.PhaseCompleted || final.Battle.PhaseCompletedAt.IsZero() || len(f.service.raidWeeklyIntents) != 1 {
		t.Fatal("actual final did not freeze weekly intent")
	}
	return f, final, now
}

func TestRealPVFAntonFinalClearCommitsWeeklyOnceAfterLobbyDeletion(t *testing.T) {
	f, state, now := actualFinalAntonWeeklyFixture(t)
	state.Battle.MemberAccounts[19] = "other"
	state.Battle.ClearedRuns = map[string]currentRaidClearedRun{}
	f.service.raidMu.Lock()
	delete(f.service.raids, f.state.RaidKey)
	f.service.raidMu.Unlock()
	completed, errs := f.service.commitCurrentRaidWeeklyIntents(now)
	if len(errs) != 0 || len(completed) != 1 || len(completed[0].Members) != 2 {
		t.Fatal("frozen final commit", completed, errs)
	}
	policy, _ := currentRaidWeeklyPolicy()
	for id, want := range map[string]int{"19": 1, "20": 1, "21": 0} {
		character, _, _ := f.repos.Character.Load(context.Background(), id)
		used, err := raid.WeeklyClears(character, 1, now, policy)
		if err != nil || used != want {
			t.Fatal("participant count", id, used, want, err)
		}
		if other, err := raid.WeeklyClears(character, 2, now, policy); err != nil || other != 0 {
			t.Fatal("other mode changed", id, other, err)
		}
	}
	if completed, errs := f.service.commitCurrentRaidWeeklyIntents(now.Add(time.Second)); len(completed) != 0 || len(errs) != 0 {
		t.Fatal("finished intent replayed", completed, errs)
	}
}

func TestRealPVFAntonFinalWeeklyTransactionFailureBackoffAndRetry(t *testing.T) {
	f, _, now := actualFinalAntonWeeklyFixture(t)
	original := f.repos.CharacterTrade
	f.repos.CharacterTrade = raidFailGroupTransaction{CharacterTradeUnitOfWork: original, group: original.(repo.CharacterInventoryGroupUnitOfWork), commitFailure: true}
	completed, errs := f.service.commitCurrentRaidWeeklyIntents(now)
	if len(completed) != 0 || len(errs) != 1 {
		t.Fatal("failure accepted", completed, errs)
	}
	policy, _ := currentRaidWeeklyPolicy()
	for _, id := range []string{"19", "20"} {
		character, _, _ := f.repos.Character.Load(context.Background(), id)
		used, err := raid.WeeklyClears(character, 1, now, policy)
		if err != nil || used != 0 {
			t.Fatal("failed group leaked count", id, used, err)
		}
	}
	f.repos.CharacterTrade = original
	if completed, errs := f.service.commitCurrentRaidWeeklyIntents(now.Add(time.Second)); len(completed) != 0 || len(errs) != 0 {
		t.Fatal("backoff ignored", completed, errs)
	}
	if completed, errs := f.service.commitCurrentRaidWeeklyIntents(now.Add(2 * time.Second)); len(completed) != 1 || len(errs) != 0 {
		t.Fatal("frozen retry failed", completed, errs)
	}
}

func TestRealPVFAntonFinalWeeklyRejectsUncompletedOrForeignProjection(t *testing.T) {
	_, state, _ := actualFinalAntonWeeklyFixture(t)
	for _, name := range []string{"not_started", "unfinished", "timeout", "missing_time", "before_enter", "at_deadline", "first_phase", "missing_program", "missing_reward_policy", "no_runs", "empty_owner", "invalid_instance", "no_limit"} {
		t.Run(name, func(t *testing.T) {
			v := cloneRuntimeRaidState(state)
			b := v.Battle
			switch name {
			case "not_started":
				v.Started = false
			case "unfinished":
				b.PhaseCompleted = false
			case "timeout":
				b.TimedOut = true
			case "missing_time":
				b.PhaseCompletedAt = time.Time{}
			case "before_enter":
				b.PhaseCompletedAt = b.EnterAfter.Add(-time.Second)
			case "at_deadline":
				b.PhaseCompletedAt = b.Deadline
			case "first_phase":
				b.Phase = 0
			case "missing_program":
				b.Anton = nil
			case "missing_reward_policy":
				b.AntonRewards = nil
			case "no_runs":
				b.ClearedRuns = nil
			case "empty_owner":
				b.MemberAccounts[19] = ""
			case "invalid_instance":
				b.InstanceID = "invalid"
			case "no_limit":
				b.Configuration.WeeklyClearLimit = 0
			}
			if _, err := prepareCurrentRaidFinalWeeklyCommand(v); err == nil {
				t.Fatal("invalid final projection admitted")
			}
		})
	}
}

func TestRealPVFAntonSingleFinalWeeklyRequiresWholeActualSequence(t *testing.T) {
	catalog, _ := actualAntonRewardFixture(t)
	archive := catalog.source.(*platformpvf.Archive)
	config, err := raid.LoadBattleConfiguration(archive, "etc/raid/anton.etc")
	if err != nil {
		t.Fatal(err)
	}
	sequence, err := raid.LoadSingleRaidSequence(archive, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	f := raidSafetyFixture(t, archive, 1)
	f.service.options.currentClientVersion = "official-24.9"
	session := f.session()
	session.loginPacketSeen, session.accountFromToken = true, true
	f.service.bindGameSessionCharacter(session, 19)
	now := time.Now()
	base := now.Add(-20 * time.Second)
	b := &runtimeRaidBattle{InstanceID: "10112233445566778899aabbccddeeff", Mode: 1, Single: true, ChannelID: session.channel.ID, ChannelType: session.channel.Type, StartedAt: base, EnterAfter: base, Deadline: now.Add(time.Hour), Configuration: config, SingleSequence: sequence, MemberAccounts: map[uint16]string{19: "account-1"}}
	for i, id := range sequence {
		state := byte(1)
		if i == 0 {
			state = 0
		}
		b.Dungeons = append(b.Dungeons, raid.DungeonState{ID: id, State: state})
	}
	f.state.Started, f.state.Battle = true, b
	for n, id := range sequence {
		r := raidCompletedRuntimeFixture(t, session, id)
		if err := f.service.freezeCurrentRaidDungeonGroupEntry(session, r, nil); err != nil {
			t.Fatal(n, id, err)
		}
		at := base.Add(time.Duration(n+1) * time.Second)
		if _, changed, err := f.service.reduceCurrentRaidDungeonClearLocked(session, r, at); err != nil || !changed {
			t.Fatal(n, id, changed, err)
		}
		if n < len(sequence)-1 && len(f.service.raidWeeklyIntents) != 0 {
			t.Fatal("single intermediate dungeon counted raid clear")
		}
		if _, changed, err := f.service.reduceCurrentRaidDungeonClearLocked(session, r, at); err != nil || changed {
			t.Fatal("single replay", n, changed, err)
		}
	}
	if len(f.service.raidWeeklyIntents) != 1 {
		t.Fatal("single final intent missing")
	}
	if completed, errs := f.service.commitCurrentRaidWeeklyIntents(now); len(completed) != 1 || len(errs) != 0 {
		t.Fatal(completed, errs)
	}
	character, _, _ := f.repos.Character.Load(context.Background(), "19")
	policy, _ := currentRaidWeeklyPolicy()
	if used, err := raid.WeeklyClears(character, 1, now, policy); err != nil || used != 1 {
		t.Fatal("single final weekly", used, err)
	}
}
