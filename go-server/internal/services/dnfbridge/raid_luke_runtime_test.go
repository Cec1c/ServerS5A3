package dnfbridge

import (
	"context"
	"encoding/binary"
	"fmt"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/raid"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func normalLukeFixture(t *testing.T) (*raidStartGuardFixture, *gameSession, time.Time) {
	t.Helper()
	catalog, _ := actualAntonRewardFixture(t)
	a := catalog.source.(*platformpvf.Archive)
	config, err := raid.LoadBattleConfiguration(a, "etc/raid/luke/luke.etc")
	if err != nil {
		t.Fatal(err)
	}
	rewards, err := raid.LoadLukeRewardConfiguration(a)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateCurrentAntonRewardDependencies(catalog, rewards, time.Now()); err != nil {
		t.Fatal(err)
	}
	flow, err := raid.LoadLukeFlowConfiguration(a)
	if err != nil {
		t.Fatal(err)
	}
	program, err := raid.LoadLukeProgram(a)
	if err != nil {
		t.Fatal(err)
	}
	f := raidSafetyFixture(t, a, 2)
	f.service.options.currentClientVersion = "official-24.9"
	session := f.session()
	session.channel.ID, session.channel.Type = 32, 32
	session.loginPacketSeen, session.accountFromToken = true, true
	f.service.bindGameSessionCharacter(session, 19)
	start := time.Now().Add(-16 * time.Minute)
	progress, err := program.BeginPhase(start, 8172)
	if err != nil {
		t.Fatal(err)
	}
	f.state.Started, f.state.CurrentLayout, f.state.RouteOrRaidType = true, true, 1
	f.state.TailFlag, f.state.CurrentUIFlags = 0, [3]byte{}
	f.state.Members[0].GroupIndex, f.state.Members[1].GroupIndex = 1, 2
	f.state.Battle = &runtimeRaidBattle{InstanceID: "22446688aacc001133557799bbddeeff", Mode: 2, ChannelID: 32, ChannelType: 32, StartedAt: start, EnterAfter: start, Deadline: start.Add(2100 * time.Second), Configuration: config, Anton: progress.AntonProgression, AntonRewards: &rewards, AntonFlowConfig: flow, Dungeons: progress.Dungeons(), MemberAccounts: map[uint16]string{19: "account-1", 20: "account-20"}, MemberGroups: map[uint16]byte{19: 1, 20: 2}}
	return f, session, start.Add(time.Minute)
}

func TestRealPVFLukeClosedReplacedSessionRetiresOnlyOwnedRun(t *testing.T) {
	f, session, _ := normalLukeFixture(t)
	_, r := prepareTutorialScopeRuntime(t, tutorialScopeFixtureOptions{disableTutorial: true, singleMonster: true})
	r.Dungeon.ID, r.AccountID = 5054, session.accountID
	r.Character = repo.CharacterRecord{CharacterID: "19", AccountID: session.accountID}
	session.dungeon.runtime = r
	if err := f.service.freezeCurrentRaidDungeonGroupEntry(session, r, nil); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.service.reduceCurrentLukeSceneLocked(session, r, "enter", time.Now()); err != nil {
		t.Fatal(err)
	}
	replacement := f.session()
	replacement.channel, replacement.loginPacketSeen, replacement.accountFromToken = session.channel, true, true
	f.service.bindGameSessionCharacter(replacement, 19)
	if f.service.currentRaidAuthenticated(session) {
		t.Fatal("replacement left old canonical binding")
	}
	if err := r.Session.Abandon(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.service.reduceCurrentLukeSceneLocked(session, r, "giveup", time.Now()); err == nil {
		t.Fatal("replaced socket accepted packet-driven event")
	}
	session.events = &gameSessionEvents{}
	if _, _, err := f.service.reduceCurrentLukeSceneLocked(session, r, "retire", time.Now()); err != nil {
		t.Fatal("serialized close retained party slot", err)
	}
	if r.Session.Snapshot().Run.Status != worldmap.DungeonRunAbandoned {
		t.Fatal("uncommitted retirement")
	}
	luke := &raid.LukeProgression{AntonProgression: f.state.Battle.Anton}
	if luke.HasEnteredRun(r.raidEntry.RunID, 5054) {
		t.Fatal("closed run retained occupancy")
	}
	if _, err := luke.ReserveDungeon("replacement-run", 5054, luke.Epoch(), time.Now()); err != nil {
		t.Fatal("replacement cannot enter released slot", err)
	}
	if replacement.dungeon.runtime != nil {
		t.Fatal("old close modified replacement runtime")
	}
}

func TestRealPVFLukePreparedFollowerSharesGroupReservation(t *testing.T) {
	f, leader, _ := normalLukeFixture(t)
	peer := f.session()
	peer.selectedCharacterID, peer.accountID = 20, "account-20"
	peer.channel, peer.loginPacketSeen, peer.accountFromToken = leader.channel, true, true
	f.service.bindGameSessionCharacter(peer, 20)
	f.state.Members[1].GroupIndex, f.state.Battle.MemberGroups[20] = 1, 1
	runtimes := []*runtimeDungeonState{}
	for _, owner := range []*gameSession{leader, peer} {
		_, r := prepareTutorialScopeRuntime(t, tutorialScopeFixtureOptions{disableTutorial: true, singleMonster: true})
		r.Dungeon.ID, r.AccountID = 5054, owner.accountID
		r.Character = repo.CharacterRecord{CharacterID: fmt.Sprint(owner.selectedCharacterID), AccountID: owner.accountID}
		runtimes = append(runtimes, r)
	}
	prepared := &runtimePartyPreparedDungeonEntry{session: peer, runtime: runtimes[1]}
	if err := f.service.prepareCurrentRaidDungeonEntry(leader, runtimes[0], nil, []*runtimePartyPreparedDungeonEntry{prepared}); err != nil {
		t.Fatal("leader group preflight", err)
	}
	run := runtimes[0].raidEntry.RunID
	if run != runtimes[1].raidEntry.RunID || len(runtimes[1].raidEntry.Participants) != 2 {
		t.Fatal("follower lost frozen group")
	}
	leader.dungeon.runtime, peer.dungeon.runtime = runtimes[0], runtimes[1]
	prepared.committed = true
	if err := f.service.prepareCurrentRaidDungeonEntry(peer, runtimes[1], prepared, nil); err != nil {
		t.Fatal("committed follower cannot stage op28", err)
	}
	if runtimes[1].raidEntry.RunID != run || len(runtimes[1].raidEntry.Participants) != 2 {
		t.Fatal("follower staging rebound the run")
	}
	if err := f.service.prepareCurrentRaidDungeonEntry(leader, runtimes[0], prepared, nil); err == nil {
		t.Fatal("foreign prepared follower accepted")
	}
	f.state.Members[1].GroupIndex = 2
	if err := f.service.prepareCurrentRaidDungeonEntry(peer, runtimes[1], prepared, nil); err == nil {
		t.Fatal("changed raid group accepted")
	}
	f.state.Members[1].GroupIndex = 1
	for i, owner := range []*gameSession{leader, peer} {
		if _, _, err := f.service.reduceCurrentLukeSceneLocked(owner, runtimes[i], "enter", time.Now()); err != nil {
			t.Fatal("shared group entry", err)
		}
	}
	luke := &raid.LukeProgression{AntonProgression: f.state.Battle.Anton}
	if _, err := luke.ReserveDungeon("another-party", 5054, luke.Epoch(), time.Now()); err == nil {
		t.Fatal("second party bypassed actual PARTY MAX 1")
	}
	for i, owner := range []*gameSession{leader, peer} {
		if err := runtimes[i].Session.Abandon(); err != nil {
			t.Fatal(err)
		}
		if _, _, err := f.service.reduceCurrentLukeSceneLocked(owner, runtimes[i], "giveup", time.Now()); err != nil {
			t.Fatal(err)
		}
		luke = &raid.LukeProgression{AntonProgression: f.state.Battle.Anton}
		if luke.HasEnteredRun(run, 5054) != (i == 0) {
			t.Fatal("party slot released before final departure or retained afterward")
		}
	}
	if _, err := luke.ReserveDungeon("next-party", 5054, luke.Epoch(), time.Now()); err != nil {
		t.Fatal("fully departed group retained reservation", err)
	}
}

func TestLukeTimerRestoreUsesRemainingNativeDuration(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	end := now.Add(601 * time.Second)
	if got := currentRaidNativeDuration(2, end, 2403, now); got != 601 {
		t.Fatal("Luke reconnect reset phase time", got)
	}
	if got := currentRaidNativeDuration(1, end, 2403, now); got != 2403 {
		t.Fatal("Anton native clock changed", got)
	}
	if got := currentRaidNativeDuration(2, now.Add(-time.Second), 2403, now); got != 0 {
		t.Fatal("expired phase resumed", got)
	}
	key := raid.AntonTimerKey{Kind: 2, DungeonID: 5063}
	// Field names are the shared literal timer format; Luke interprets duration
	// relative to receive time in the actual channel-32 native manager.
	timers := map[raid.AntonTimerKey]raid.AntonTimer{key: {End: end, Duration: 900}}
	packets, err := currentRaidTimerProjectionForMode(2, nil, timers, now)
	if err != nil || len(packets) != 1 {
		t.Fatal(packets, err)
	}
	if binary.LittleEndian.Uint32(packets[0].body[9:13]) != 601 {
		t.Fatal("Luke restored timer restarted full duration")
	}
}

func clearOwnedLuke(t *testing.T, f *raidStartGuardFixture, session *gameSession, id uint32, at time.Time) {
	t.Helper()
	_, r := prepareTutorialScopeRuntime(t, tutorialScopeFixtureOptions{disableTutorial: true, singleMonster: true})
	r.Dungeon.ID, r.AccountID = int64(id), session.accountID
	r.Character = repo.CharacterRecord{CharacterID: fmt.Sprint(session.selectedCharacterID), AccountID: session.accountID}
	session.dungeon.runtime = r
	if err := f.service.freezeCurrentRaidDungeonGroupEntry(session, r, nil); err != nil {
		t.Fatal(id, err)
	}
	if _, _, err := f.service.reduceCurrentLukeSceneLocked(session, r, "enter", at); err != nil {
		t.Fatal(id, err)
	}
	if err := r.Session.CompleteCurrentRoom(); err != nil {
		t.Fatal(err)
	}
	r.bossDieCheckAccepted, r.bossDieCheckResponseSent, r.settlementEntrySent = true, true, true
	if _, changed, err := f.service.reduceCurrentRaidDungeonClearLocked(session, r, at); err != nil || !changed {
		t.Fatal(id, changed, err)
	}
	if _, changed, err := f.service.reduceCurrentRaidDungeonClearLocked(session, r, at); err != nil || changed {
		t.Fatal("clear replay", id, changed, err)
	}
}

func TestRealPVFLukeOwnedTwoPhasesRewardsAndWeekly(t *testing.T) {
	f, session, at := normalLukeFixture(t)
	for _, id := range []uint32{5054, 5055, 5056} {
		for n := 0; n < 3; n++ {
			clearOwnedLuke(t, f, session, id, at)
			at = at.Add(time.Second)
		}
	}
	b := f.state.Battle
	result, err := prepareCurrentAntonResultProjection(cloneRuntimeRaidState(*f.state), 0)
	if err != nil {
		t.Fatal(err)
	}
	writeAntonNativeCardFixture(t, "go-luke-phase0-result.bin", result.Body)
	mapBody, err := raid.BuildDungeonStateBody(b.Dungeons, 0)
	if err != nil {
		t.Fatal(err)
	}
	writeAntonNativeCardFixture(t, "luke-map.bin", mapBody)
	writeAntonNativeCardFixture(t, "luke-timer.bin", raid.BuildTimerBody(0, 0, uint32(b.Deadline.Unix()), currentRaidNativeDuration(2, b.Deadline, 2103, time.Now())))
	writeAntonNativeCardFixture(t, "luke-remain.bin", raid.BuildRemainingTimeBody(0, uint32(time.Until(b.Deadline).Seconds())))
	first := b.RewardFlow[0]
	if !b.PhaseCompleted || first == nil || first.Mode != 2 || len(first.Recipients) != 2 || first.squadRequired() || len(f.service.raidWeeklyIntents) != 0 {
		t.Fatal("Luke first phase eligibility/weekly", first)
	}
	if first.Recipients[1].Clears != 0 {
		t.Fatal("observer fixture unexpectedly cleared")
	}
	if _, errs := f.service.advanceCurrentAntonRewardFlows(first.AutoSelectAt, zeroAntonRoll); len(errs) != 0 {
		t.Fatal(errs)
	}
	if len(first.Squad) != 0 || first.Party[20] == nil || !first.Party[20].Committed {
		t.Fatal("zero-clear admitted member lost actual phase-zero reward")
	}
	writeLukeCommittedCards(t, "go-luke-phase0-party.bin", 1, first, first.Party)
	if _, errs := f.service.advanceCurrentAntonRewardFlows(first.AutoSelectAt.Add(65*time.Second), zeroAntonRoll); len(errs) != 0 || first.FinishedAt.IsZero() || first.BreakUntil.IsZero() {
		t.Fatal("Luke no-squad flow stalled", errs)
	}
	states, errs := f.service.advanceCurrentRaidClocks(first.BreakUntil)
	if len(errs) != 0 || len(states) != 1 || b.Phase != 1 || !b.Anton.IsLuke() || b.PhaseCompleted {
		t.Fatal("Luke next phase", states, errs)
	}
	at = b.EnterAfter.Add(time.Second)
	peer := f.session()
	peer.selectedCharacterID, peer.accountID = 20, "account-20"
	peer.channel, peer.loginPacketSeen, peer.accountFromToken = session.channel, true, true
	f.service.bindGameSessionCharacter(peer, 20)
	for _, id := range []uint32{5311, 5313, 5057, 5058, 5059, 5060, 5064, 5065, 5061, 5062} {
		owner := session
		if id == 5313 || id == 5058 || id == 5060 || id == 5065 || id == 5062 {
			owner = peer
		}
		clearOwnedLuke(t, f, owner, id, at)
		at = at.Add(time.Second)
	}
	final := b.RewardFlow[1]
	if !b.PhaseCompleted || final == nil || final.Mode != 2 || final.Rewards.MinimumClears != 2 || len(final.Recipients) != 2 || final.Recipients[0].CharacterID != 19 || final.Recipients[1].CharacterID != 20 {
		t.Fatal("Luke final owned minimum", final)
	}
	result, err = prepareCurrentAntonResultProjection(cloneRuntimeRaidState(*f.state), 0)
	if err != nil {
		t.Fatal(err)
	}
	writeAntonNativeCardFixture(t, "go-luke-phase1-result.bin", result.Body)
	if _, errs := f.service.advanceCurrentAntonRewardFlows(final.AutoSelectAt, zeroAntonRoll); len(errs) != 0 || len(final.Party) != 0 {
		t.Fatal("weekly transaction not required", errs)
	}
	done, errs := f.service.commitCurrentRaidWeeklyIntents(at)
	if len(done) != 1 || len(errs) != 0 || done[0].Mode != 2 || len(done[0].Members) != 2 {
		t.Fatal("Luke weekly owner", done, errs)
	}
	if _, errs := f.service.advanceCurrentAntonRewardFlows(final.AutoSelectAt, zeroAntonRoll); len(errs) != 0 || !final.Party[19].Committed || !final.Squad[19].Committed {
		t.Fatal("Luke final atomic rewards", errs)
	}
	writeLukeCommittedCards(t, "go-luke-phase1-party.bin", 1, final, final.Party)
	writeLukeCommittedCards(t, "go-luke-phase1-squad.bin", 3, final, final.Squad)
	if _, errs := f.service.advanceCurrentAntonRewardFlows(final.AutoSelectAt.Add(65*time.Second), func(int64) (int64, error) { t.Fatal("committed reward rerolled"); return 0, nil }); len(errs) != 0 || final.FinishedAt.IsZero() {
		t.Fatal("Luke final replay", errs)
	}
	for _, id := range []string{"19", "20"} {
		character, _, err := f.repos.Character.Load(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if character.Stats["raid_reward_v1_2_"+b.InstanceID+"_0_1"] == 0 || character.Stats["raid_reward_v1_1_"+b.InstanceID+"_0_1"] != 0 {
			t.Fatal("Luke receipt mode mixup", id, character.Stats)
		}
		if character.Stats["raid_weekly_clears_v1_2"] != 1 {
			t.Fatal("Luke weekly participation", id, character.Stats)
		}
	}
}

func writeLukeCommittedCards(t *testing.T, name string, kind byte, flow *currentAntonRewardFlow, cards map[uint16]*currentAntonRewardCard) {
	t.Helper()
	var values []raid.RewardDisplayCard
	for _, owner := range flow.Recipients {
		card := cards[owner.CharacterID]
		if card == nil || !card.Committed {
			t.Fatal("uncommitted native fixture")
		}
		values = append(values, raid.RewardDisplayCard{CharacterID: owner.CharacterID, CardType: card.Card, Items: currentAntonPartyDisplayItems(card.Plan)})
	}
	body, err := raid.BuildRewardListBody(kind, values)
	if err != nil {
		t.Fatal(err)
	}
	writeAntonNativeCardFixture(t, name, body)
}

func TestRealPVFLukeTimeoutNeverEarnsRewardsOrWeekly(t *testing.T) {
	f, _, _ := normalLukeFixture(t)
	b := f.state.Battle
	states, errs := f.service.advanceCurrentRaidClocks(b.Deadline)
	if len(states) != 1 || len(errs) != 0 || !b.TimedOut || b.PhaseCompleted || !b.Anton.PhaseFailed() || len(f.service.antonRewardFlows) != 0 || len(f.service.raidWeeklyIntents) != 0 {
		t.Fatal("Luke actual failure rule", states, errs)
	}
}
