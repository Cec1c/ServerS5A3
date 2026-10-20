package dnfbridge

import (
	"context"
	"encoding/binary"
	"fmt"
	"strings"
	"testing"
	"time"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	"longheng.io/server/internal/modules/dnf/raid"
	repo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

type singleAntonChangedRewardSource struct {
	dnfpvf.Source
	path, old, replacement string
	missing                bool
}

func (s singleAntonChangedRewardSource) ReadText(path string) (string, error) {
	text, err := s.Source.ReadText(path)
	if !strings.EqualFold(path, s.path) {
		return text, err
	}
	if s.missing {
		return "", fmt.Errorf("explicit missing single reward test source")
	}
	return strings.Replace(text, s.old, s.replacement, 1), err
}

func TestRealPVFSingleAntonRejectsMissingOrDisagreeingActualRewardSources(t *testing.T) {
	catalog, _ := actualAntonRewardFixture(t)
	sequence, err := raid.LoadSingleRaidSequence(catalog.source, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, path, old, replacement string
		missing                      bool
	}{
		{"missing_squad", "stackable/10304001/10304671.stk", "", "", true},
		{"equivalence_not_present", "stackable/10304001/10304671.stk", "礼盒内容与安徒恩单人模式灭杀奖励相同", "另一个奖励", false},
		{"party_quantity_disagrees", "stackable/10304001/10304476.stk", "10304529 100 60", "10304529 100 61", false},
		{"combined_weight_disagrees", "stackable/10303001/10303189.stk", "10093974 550100 25", "10093974 550101 25", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := singleAntonChangedRewardSource{Source: catalog.source, path: tc.path, old: tc.old, replacement: tc.replacement, missing: tc.missing}
			changed, err := newPVFDungeonDropCatalog(source)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := loadCurrentSingleAntonRewards(changed, sequence, time.Now()); err == nil {
				t.Fatal("bad actual single source accepted")
			}
		})
	}
}

// Actual PVF rules/items, but explicitly in-memory completed dungeon/asset
// fixtures. Physical boss kills, result rendering and socket transport are not
// proved by this test; the repaired all-room test owns map dependency evidence.
func singleAntonCompletedRewardFixture(t *testing.T) (*raidStartGuardFixture, *gameSession, time.Time) {
	t.Helper()
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
	flow, err := raid.LoadAntonFlowConfiguration(archive)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now().Add(-2 * time.Minute)
	rewards, err := loadCurrentSingleAntonRewards(catalog, sequence, start)
	if err != nil {
		t.Fatal(err)
	}
	f := raidSafetyFixture(t, archive, 1)
	f.service.options.currentClientVersion = "official-24.9"
	session := f.session()
	session.loginPacketSeen, session.accountFromToken = true, true
	f.service.bindGameSessionCharacter(session, 19)
	f.state.Members[0].GroupIndex = 1
	b := &runtimeRaidBattle{InstanceID: "33445566778899aabbccddeeff001122", Mode: 1, Single: true, ChannelID: session.channel.ID, ChannelType: session.channel.Type,
		StartedAt: start, EnterAfter: start, Deadline: start.Add(time.Duration(config.PhaseLimitSeconds[0]) * time.Second), Configuration: config,
		SingleSequence: sequence, AntonRewards: &rewards, AntonFlowConfig: flow, MemberAccounts: map[uint16]string{19: "account-1"}, MemberGroups: map[uint16]byte{19: 1}}
	for i, id := range sequence {
		status := byte(1)
		if i == 0 {
			status = 0
		}
		b.Dungeons = append(b.Dungeons, raid.DungeonState{ID: id, State: status})
	}
	f.state.Started, f.state.Battle = true, b
	for n, id := range sequence {
		r := raidCompletedRuntimeFixture(t, session, id)
		if err := f.service.freezeCurrentRaidDungeonGroupEntry(session, r, nil); err != nil {
			t.Fatal(n, err)
		}
		at := start.Add(time.Duration(n+1) * time.Second)
		if _, changed, err := f.service.reduceCurrentRaidDungeonClearLocked(session, r, at); err != nil || !changed {
			t.Fatal(n, changed, err)
		}
		if n < len(sequence)-1 && (b.RewardFlow[0] != nil || len(f.service.raidWeeklyIntents) != 0) {
			t.Fatal("intermediate single map produced terminal entitlement")
		}
		if _, changed, err := f.service.reduceCurrentRaidDungeonClearLocked(session, r, at); err != nil || changed {
			t.Fatal("single clear replay", n, changed, err)
		}
	}
	return f, session, b.PhaseCompletedAt
}

func TestRealPVFSingleAntonActualEquivalentRewardSourcesAndEverySquadBranch(t *testing.T) {
	catalog, _ := actualAntonRewardFixture(t)
	sequence, err := raid.LoadSingleRaidSequence(catalog.source, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	config, err := loadCurrentSingleAntonRewards(catalog, sequence, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(config.Phases) != 1 || config.Phases[0].MinimumClears != 6 || len(config.Phases[0].Choices) != 2 {
		t.Fatal(config)
	}
	party, err := resolveCurrentAntonRewardPlan(catalog, config.Phases[0], "party_card", 0, zeroAntonRoll, now)
	if err != nil || len(party.Rewards) != 1 || party.Rewards[0].Definition.ItemID != 10304529 || party.Rewards[0].Count != 60 {
		t.Fatal(party, err)
	}
	box, err := resolveCurrentBoosterDefinition(catalog, singleAntonSquadSourceID, currentBoosterRequestRandom)
	if err != nil {
		t.Fatal(err)
	}
	var boundary int64
	for _, entry := range box.Random.Groups[0].Entries {
		for _, draw := range []int64{boundary, boundary + entry.Weight - 1} {
			calls := 0
			plan, err := resolveCurrentSingleAntonSquadPlan(catalog, config.Phases[0], 3, func(total int64) (int64, error) {
				calls++
				if total != 1000000 {
					t.Fatal(total)
				}
				return draw, nil
			}, now)
			if err != nil || calls != 1 || len(plan.Rewards) != 1 || plan.Rewards[0].Definition.ItemID != uint32(entry.ItemID) || plan.Rewards[0].Count != uint32(entry.Count) {
				t.Fatal(draw, calls, entry, plan, err)
			}
		}
		boundary += entry.Weight
	}
	if boundary != 1000000 {
		t.Fatal(boundary)
	}
	for _, bad := range []int64{-1, 1000000} {
		if _, err := resolveCurrentSingleAntonSquadPlan(catalog, config.Phases[0], 0, func(int64) (int64, error) { return bad, nil }, now); err == nil {
			t.Fatal("out-of-pool single draw accepted", bad)
		}
	}
}

func TestRealPVFSingleAntonFinalResultAndAutomaticRewardsWaitForWeeklyAndNeverStartNormalPhase(t *testing.T) {
	f, session, at := singleAntonCompletedRewardFixture(t)
	b := f.state.Battle
	flow := b.RewardFlow[0]
	if flow == nil || !flow.Single || len(flow.Recipients) != 1 || flow.Weekly.Mode != 1 {
		t.Fatal(flow)
	}
	state := cloneRuntimeRaidState(*f.state)
	if err := f.service.sendCurrentAntonTerminalResult(session, state); err != nil || f.conn.write.Len() != 0 {
		t.Fatal("single result before weekly", err)
	}
	if _, errs := f.service.advanceCurrentAntonRewardFlows(flow.AutoSelectAt, zeroAntonRoll); len(errs) != 0 || len(flow.Party) != 0 || len(flow.Squad) != 0 {
		t.Fatal("single assets before weekly", errs)
	}
	if done, errs := f.service.commitCurrentRaidWeeklyIntents(at); len(done) != 1 || len(errs) != 0 {
		t.Fatal(done, errs)
	}
	if err := f.service.sendCurrentAntonTerminalResult(session, state); err != nil {
		t.Fatal(err)
	}
	assertAntonResultStream(t, f.conn.write.Bytes(), 0, false)
	packet, _ := splitGameServerUpperPacket(t, f.conn.write.Bytes())
	if packet.Body[1] != 0 || binary.LittleEndian.Uint32(packet.Body[2:6]) != 6 {
		t.Fatal("single whole sequence time/phase wrong", packet.Body)
	}
	writeAntonNativeCardFixture(t, "go-single-anton-final-result.bin", packet.Body)
	f.conn.write.Reset()
	if _, errs := f.service.advanceCurrentAntonRewardFlows(flow.AutoSelectAt, zeroAntonRoll); len(errs) != 0 {
		t.Fatal(errs)
	}
	if antonGrantedItemCount(t, f, "19", 10304529) != 60 || antonGrantedItemCount(t, f, "19", 10093974) != 25 || antonGrantedItemCount(t, f, "19", singleAntonSquadSourceID) != 0 {
		t.Fatal("single actual reward contents missing or compensation source awarded")
	}
	if err := f.service.sendCurrentAntonRewardFlowProjection(session, cloneRuntimeRaidState(*f.state)); err != nil {
		t.Fatal(err)
	}
	body := antonNativeCardBodies(t, f)[3]
	if len(body) != 14 || body[1] != 1 || binary.LittleEndian.Uint32(body[7:11]) != 10093974 {
		t.Fatal(body)
	}
	writeAntonNativeCardFixture(t, "go-single-anton-final-squad.bin", body)
	f.conn.write.Reset()
	if _, errs := f.service.advanceCurrentAntonRewardFlows(flow.AutoSelectAt.Add(20*time.Second), func(int64) (int64, error) { t.Fatal("single receipt rerolled"); return 0, nil }); len(errs) != 0 || flow.FinishedAt.IsZero() || !flow.BreakUntil.IsZero() {
		t.Fatal("single finished into normal break", errs)
	}
	if _, errs := f.service.advanceCurrentRaidClocks(flow.AutoSelectAt.Add(time.Hour)); len(errs) != 0 || b.Phase != 0 || b.Anton != nil {
		t.Fatal("single started normal phase", errs)
	}
	if metadata := currentRuntimeRaidMetadata(cloneRuntimeRaidState(*f.state)); metadata.State != 4 || metadata.Phase != 0 {
		t.Fatal("single final metadata became normal phase break", metadata)
	}
	character, _, err := f.repos.Character.Load(context.Background(), "19")
	policy, _ := currentRaidWeeklyPolicy()
	if used, e := raid.WeeklyClears(character, 1, at, policy); err != nil || e != nil || used != 1 {
		t.Fatal("single weekly counted more than once", used, err, e)
	}
	if err := f.service.sendCurrentAntonTerminalResult(session, cloneRuntimeRaidState(*f.state)); err != nil || f.conn.write.Len() != 0 {
		t.Fatal("single result repeated", err)
	}
}

func TestRealPVFSingleAntonWeeklyFailureRetainsEntitlementAndBlocksResultAndAssets(t *testing.T) {
	f, session, at := singleAntonCompletedRewardFixture(t)
	original := f.repos.CharacterTrade
	f.repos.CharacterTrade = raidFailGroupTransaction{CharacterTradeUnitOfWork: original, group: original.(repo.CharacterInventoryGroupUnitOfWork), commitFailure: true}
	flow := f.state.Battle.RewardFlow[0]
	if done, errs := f.service.commitCurrentRaidWeeklyIntents(at); len(done) != 0 || len(errs) != 1 {
		t.Fatal(done, errs)
	}
	if err := f.service.sendCurrentAntonTerminalResult(session, cloneRuntimeRaidState(*f.state)); err != nil || f.conn.write.Len() != 0 {
		t.Fatal("failed weekly published single success", err)
	}
	if _, errs := f.service.advanceCurrentAntonRewardFlows(flow.AutoSelectAt, zeroAntonRoll); len(errs) != 0 || len(flow.Party) != 0 || len(flow.Squad) != 0 {
		t.Fatal("failed weekly released single assets", errs)
	}
	f.repos.CharacterTrade = original
	if done, errs := f.service.commitCurrentRaidWeeklyIntents(at.Add(2 * time.Second)); len(done) != 1 || len(errs) != 0 {
		t.Fatal(done, errs)
	}
	if err := f.service.sendCurrentAntonTerminalResult(session, cloneRuntimeRaidState(*f.state)); err != nil {
		t.Fatal(err)
	}
	assertAntonResultStream(t, f.conn.write.Bytes(), 0, false)
	if _, errs := f.service.advanceCurrentAntonRewardFlows(flow.AutoSelectAt, zeroAntonRoll); len(errs) != 0 || antonGrantedItemCount(t, f, "19", 10304529) != 60 || antonGrantedItemCount(t, f, "19", 10093974) != 25 {
		t.Fatal("single retry lost earned grant", errs)
	}
}
