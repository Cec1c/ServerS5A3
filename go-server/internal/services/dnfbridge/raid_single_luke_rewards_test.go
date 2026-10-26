package dnfbridge

import (
	"context"
	"reflect"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/raid"
	repo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestRealPVFSingleLukeRejectsMissingAndDisagreeingRewardSources(t *testing.T) {
	catalog, _ := actualAntonRewardFixture(t)
	sequence, err := raid.LoadSingleRaidSequence(catalog.source, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []singleAntonChangedRewardSource{
		{path: "stackable/10303001/10303050.stk", missing: true},
		{path: "stackable/10303001/10303050.stk", old: "礼盒内容与卢克单人模式卓越圣殿破坏奖励相同", replacement: "另一个奖励"},
		{path: "stackable/10304001/10304669.stk", old: "礼盒内容与单人卢克攻坚战卓越圣殿破坏奖励相同", replacement: "另一个奖励"},
		{path: "stackable/10304001/10304477.stk", old: "10304529 100 80", replacement: "10304529 100 81"},
		{path: "stackable/10304001/10304669.stk", old: "10159143 36500 5", replacement: "10159143 36501 5"},
	} {
		tc.Source = catalog.source
		changed, err := newPVFDungeonDropCatalog(tc)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := loadCurrentSingleLukeRewards(changed, sequence, time.Now()); err == nil {
			t.Fatal("invalid single Luke reward source accepted", tc.path, tc.old)
		}
	}
}

func TestRealPVFSingleLukeEverySquadBranchAndModeIsolation(t *testing.T) {
	catalog, _ := actualAntonRewardFixture(t)
	sequence, err := raid.LoadSingleRaidSequence(catalog.source, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	rewards, err := loadCurrentSingleLukeRewards(catalog, sequence, now)
	if err != nil {
		t.Fatal(err)
	}
	box, err := resolveCurrentBoosterDefinition(catalog, singleLukeSquadSourceID, currentBoosterRequestRandom)
	if err != nil {
		t.Fatal(err)
	}
	phase := rewards.Phases[0]
	var left int64
	for _, first := range box.Random.Groups[0].Entries {
		var right int64
		for _, second := range box.Random.Groups[1].Entries {
			for _, edge := range []bool{false, true} {
				draws := []int64{left, right}
				if edge {
					draws[0] += first.Weight - 1
					draws[1] += second.Weight - 1
				}
				calls := 0
				plan, err := resolveCurrentSingleLukeSquadPlan(catalog, phase, 3, func(total int64) (int64, error) {
					if calls >= 2 {
						calls++
						return 0, nil
					}
					value := draws[calls]
					calls++
					if value >= total {
						t.Fatal("boundary outside actual pool", value, total)
					}
					return value, nil
				}, now)
				if err != nil || calls < 2 {
					t.Fatal("single Luke two actual groups", calls, err, plan)
				}
				want := privateRaidExpectedZero(uint32(second.ItemID), uint32(second.Count))
				want[uint32(first.ItemID)] += uint32(first.Count)
				privateRaidAssertContents(t, plan.Rewards, want)
			}
			right += second.Weight
		}
		left += first.Weight
	}
	if _, err := resolveCurrentSingleAntonSquadPlan(catalog, phase, 0, zeroAntonRoll, now); err == nil {
		t.Fatal("Luke reward accepted by Anton-specific owner")
	}
	flow := &currentAntonRewardFlow{InstanceID: "2233445566778899aabbccddeeff0011", Mode: 2, Single: true, Rewards: phase, CompletedAt: now, Recipients: []currentAntonRewardRecipient{{CharacterID: 19, AccountID: "account-1", Group: 1, Clears: 6}}, Party: map[uint16]*currentAntonRewardCard{}, Squad: map[uint16]*currentAntonRewardCard{}}
	card, command, err := freezeCurrentAntonAutomaticCard(flow, flow.Recipients[0], 3, catalog, zeroAntonRoll)
	if err != nil || card == nil || len(card.Plan.Rewards) != 2 || command.Mode != 2 {
		t.Fatal("automatic card mode dispatch", card, command, err)
	}
}

// Completed run fixtures exercise reducer/transaction ownership, not native
// kills. The separate two-route bridge replay exercises actual entry/room/death.
func singleLukeCompletedRewardFixture(t *testing.T) (*raidStartGuardFixture, *gameSession, time.Time) {
	t.Helper()
	catalog, _ := actualAntonRewardFixture(t)
	a := catalog.source.(*platformpvf.Archive)
	config, err := raid.LoadBattleConfiguration(a, "etc/raid/luke/luke.etc")
	if err != nil {
		t.Fatal(err)
	}
	sequence, err := raid.LoadSingleRaidSequence(a, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	flow, err := raid.LoadLukeFlowConfiguration(a)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now().Add(-2 * time.Minute)
	rewards, err := loadCurrentSingleLukeRewards(catalog, sequence, start)
	if err != nil {
		t.Fatal(err)
	}
	f := raidSafetyFixture(t, a, 1)
	f.service.options.currentClientVersion = "official-24.9"
	session := f.session()
	session.channel.ID, session.channel.Type = 32, 32
	session.loginPacketSeen, session.accountFromToken = true, true
	f.service.bindGameSessionCharacter(session, 19)
	f.state.Started, f.state.CurrentLayout, f.state.RouteOrRaidType = true, true, 1
	f.state.CurrentUIFlags = [3]byte{1, 0, 0}
	f.state.Members[0].GroupIndex = 1
	b := &runtimeRaidBattle{InstanceID: "33445566778899aabbccddeeff001122", Mode: 2, Single: true, ChannelID: 32, ChannelType: 32, StartedAt: start, EnterAfter: start, Deadline: start.Add(time.Duration(config.PhaseLimitSeconds[0]) * time.Second), Configuration: config, SingleSequence: sequence, AntonRewards: &rewards, AntonFlowConfig: flow, MemberAccounts: map[uint16]string{19: "account-1"}, MemberGroups: map[uint16]byte{19: 1}}
	for i, id := range sequence {
		state := byte(1)
		if i == 0 {
			state = 0
		}
		b.Dungeons = append(b.Dungeons, raid.DungeonState{ID: id, State: state})
	}
	f.state.Battle = b
	for i, id := range sequence {
		r := raidCompletedRuntimeFixture(t, session, id)
		if err := f.service.freezeCurrentRaidDungeonGroupEntry(session, r, nil); err != nil {
			t.Fatal(err)
		}
		if _, changed, err := f.service.reduceCurrentRaidDungeonClearLocked(session, r, start.Add(time.Duration(i+1)*time.Second)); err != nil || !changed {
			t.Fatal(i, changed, err)
		}
		if i < 5 && (b.RewardFlow[0] != nil || len(f.service.raidWeeklyIntents) != 0) {
			t.Fatal("intermediate clear earned terminal rewards")
		}
	}
	return f, session, b.PhaseCompletedAt
}

func TestRealPVFSingleLukeWeeklyFailureRetryAndRewardIdempotency(t *testing.T) {
	f, session, at := singleLukeCompletedRewardFixture(t)
	flow := f.state.Battle.RewardFlow[0]
	if flow == nil || flow.Mode != 2 || !flow.Single {
		t.Fatal("missing terminal owner")
	}
	original := f.repos.CharacterTrade
	f.repos.CharacterTrade = raidFailGroupTransaction{CharacterTradeUnitOfWork: original, group: original.(repo.CharacterInventoryGroupUnitOfWork), commitFailure: true}
	if done, errs := f.service.commitCurrentRaidWeeklyIntents(at); len(done) != 0 || len(errs) != 1 {
		t.Fatal(done, errs)
	}
	if err := f.service.sendCurrentAntonTerminalResult(session, cloneRuntimeRaidState(*f.state)); err != nil || f.conn.write.Len() != 0 {
		t.Fatal("result escaped failed weekly", err)
	}
	if _, errs := f.service.advanceCurrentAntonRewardFlows(flow.AutoSelectAt, zeroAntonRoll); len(errs) != 0 || len(flow.Party) != 0 || len(flow.Squad) != 0 {
		t.Fatal("assets escaped failed weekly", errs)
	}
	f.repos.CharacterTrade = original
	if done, errs := f.service.commitCurrentRaidWeeklyIntents(at.Add(2 * time.Second)); len(done) != 1 || len(errs) != 0 {
		t.Fatal(done, errs)
	}
	if err := f.service.sendCurrentAntonTerminalResult(session, cloneRuntimeRaidState(*f.state)); err != nil {
		t.Fatal(err)
	}
	assertAntonResultStream(t, f.conn.write.Bytes(), 0, false)
	if _, errs := f.service.advanceCurrentAntonRewardFlows(flow.AutoSelectAt, zeroAntonRoll); len(errs) != 0 {
		t.Fatal(errs)
	}
	for id, want := range map[uint32]int64{10304529: 80, 10159143: 5, 10159623: 2} {
		if got := antonGrantedItemCount(t, f, "19", id); got != want {
			t.Fatal(id, got, want)
		}
	}
	for _, id := range []uint32{singleLukeSquadSourceID, singleLukeCombinedSourceID, singleAntonSquadSourceID} {
		if got := antonGrantedItemCount(t, f, "19", id); got != 0 {
			t.Fatal("compensation source granted", id, got)
		}
	}
	if _, errs := f.service.advanceCurrentAntonRewardFlows(flow.AutoSelectAt.Add(2*time.Minute), func(int64) (int64, error) { t.Fatal("receipt rerolled"); return 0, nil }); len(errs) != 0 || flow.FinishedAt.IsZero() || !flow.BreakUntil.IsZero() {
		t.Fatal("single flow not final", errs)
	}
	if _, errs := f.service.advanceCurrentRaidClocks(flow.AutoSelectAt.Add(time.Hour)); len(errs) != 0 || f.state.Battle.Phase != 0 || f.state.Battle.Anton != nil {
		t.Fatal("normal phase started", errs)
	}
	c, _, err := f.repos.Character.Load(context.Background(), "19")
	if err != nil {
		t.Fatal(err)
	}
	policy, _ := currentRaidWeeklyPolicy()
	used, err := raid.WeeklyClears(c, 2, at, policy)
	if err != nil || used != 1 {
		t.Fatal("weekly replay", used, err)
	}
	f.conn.write.Reset()
	if err := f.service.sendCurrentAntonTerminalResult(session, cloneRuntimeRaidState(*f.state)); err != nil || f.conn.write.Len() != 0 {
		t.Fatal("duplicate terminal packet", err)
	}
}

func TestRealPVFSingleLukeTwoGroupAssetFailurePreservesCommittedPartyAndFrozenDraw(t *testing.T) {
	f, session, at := singleLukeCompletedRewardFixture(t)
	if done, errs := f.service.commitCurrentRaidWeeklyIntents(at); len(done) != 1 || len(errs) != 0 {
		t.Fatal(done, errs)
	}
	if err := f.service.sendCurrentAntonTerminalResult(session, cloneRuntimeRaidState(*f.state)); err != nil {
		t.Fatal(err)
	}
	dispatchAntonCardRequest(t, f, session, 659, []byte{0})
	dispatchAntonCardRequest(t, f, session, 660, []byte{1, 0})
	flow := f.state.Battle.RewardFlow[0]
	if flow.Party[19] == nil || !flow.Party[19].Committed || antonGrantedItemCount(t, f, "19", 10304529) != 80 {
		t.Fatal("manual party card did not commit before squad failure")
	}
	beforeInventory, _, err := f.repos.Inventory.Load(context.Background(), "19")
	if err != nil {
		t.Fatal(err)
	}
	beforeCharacter, _, err := f.repos.Character.Load(context.Background(), "19")
	if err != nil {
		t.Fatal(err)
	}
	original := f.repos.AccountAssets
	f.repos.AccountAssets = antonCardFailLateAssets{original}
	calls := 0
	if _, errs := f.service.advanceCurrentAntonRewardFlows(flow.AutoSelectAt, func(int64) (int64, error) { calls++; return 0, nil }); len(errs) != 1 || calls != 2 {
		t.Fatal("expected failure after both group draws and transaction saves", errs, calls)
	}
	card := flow.Squad[19]
	if card == nil || card.Committed || len(card.Plan.Rewards) != 2 {
		t.Fatal("failed squad plan was not frozen", card)
	}
	fingerprint := card.Plan.Fingerprint
	afterInventory, _, err := f.repos.Inventory.Load(context.Background(), "19")
	if err != nil {
		t.Fatal(err)
	}
	afterCharacter, _, err := f.repos.Character.Load(context.Background(), "19")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(beforeInventory, afterInventory) || !reflect.DeepEqual(beforeCharacter, afterCharacter) || !flow.SquadShownAt.IsZero() || !flow.FinishedAt.IsZero() {
		t.Fatal("partial squad assets/receipt or premature display escaped rollback")
	}
	f.repos.AccountAssets = original
	noReroll := func(int64) (int64, error) { t.Fatal("failed squad retry rerolled"); return 0, nil }
	if _, errs := f.service.advanceCurrentAntonRewardFlows(flow.AutoSelectAt.Add(2*time.Second), noReroll); len(errs) != 0 || !card.Committed || card.Plan.Fingerprint != fingerprint {
		t.Fatal("retry did not commit original two-group plan", errs)
	}
	for id, want := range map[uint32]int64{10304529: 80, 10159143: 5, 10159623: 2} {
		if got := antonGrantedItemCount(t, f, "19", id); got != want {
			t.Fatal("partial grant or duplicate party", id, got, want)
		}
	}
	if _, errs := f.service.advanceCurrentAntonRewardFlows(flow.AutoSelectAt.Add(2*time.Minute), noReroll); len(errs) != 0 || flow.FinishedAt.IsZero() {
		t.Fatal("recovered flow failed to finish", errs)
	}
}
