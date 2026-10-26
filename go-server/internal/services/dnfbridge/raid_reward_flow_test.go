package dnfbridge

import (
	"context"
	"encoding/binary"
	"reflect"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/raid"
	repo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func firstAntonFlowFixture(t *testing.T) (*raidStartGuardFixture, *gameSession, time.Time) {
	t.Helper()
	catalog, rewards := actualAntonRewardFixture(t)
	archive := catalog.source.(*platformpvf.Archive)
	program, err := raid.LoadAntonProgram(archive)
	if err != nil {
		t.Fatal(err)
	}
	config, err := raid.LoadBattleConfiguration(archive, "etc/raid/anton.etc")
	if err != nil {
		t.Fatal(err)
	}
	flow, err := raid.LoadAntonFlowConfiguration(archive)
	if err != nil {
		t.Fatal(err)
	}
	f := raidSafetyFixture(t, archive, 2)
	f.service.options.currentClientVersion = "official-24.9"
	session, peer := f.session(), f.session()
	session.loginPacketSeen, session.accountFromToken = true, true
	peer.selectedCharacterID, peer.accountID, peer.loginPacketSeen, peer.accountFromToken = 20, "account-20", true, true
	f.service.bindGameSessionCharacter(session, 19)
	f.service.bindGameSessionCharacter(peer, 20)
	for i := range f.state.Members {
		f.state.Members[i].GroupIndex = 1
	}
	start := time.Now().Add(-2 * time.Minute)
	progress, err := program.BeginPhase(0, start, 91321)
	if err != nil {
		t.Fatal(err)
	}
	f.state.Started = true
	f.state.TailFlag, f.state.CurrentLayout, f.state.CurrentUIFlags = 0, true, [3]byte{}
	f.state.Battle = &runtimeRaidBattle{InstanceID: "2233445566778899aabbccddeeff0011", Mode: 1, ChannelID: session.channel.ID, ChannelType: session.channel.Type, StartedAt: start, EnterAfter: start, Deadline: start.Add(time.Duration(config.PhaseLimitSeconds[0]) * time.Second), Configuration: config, Anton: progress, AntonRewards: &rewards, AntonFlowConfig: flow, Dungeons: progress.Dungeons(), MemberAccounts: map[uint16]string{19: "account-1", 20: "account-20"}, MemberGroups: map[uint16]byte{19: 1, 20: 1}}
	for n, id := range []uint32{210, 210, 210, 210, 213, 213, 215, 215} {
		leaderRun, peerRun := raidCompletedRuntimeFixture(t, session, id), raidCompletedRuntimeFixture(t, peer, id)
		if err := f.service.freezeCurrentRaidDungeonGroupEntry(session, leaderRun, []*runtimePartyPreparedDungeonEntry{{session: peer, runtime: peerRun}}); err != nil {
			t.Fatal(err)
		}
		if _, changed, err := f.service.reduceCurrentRaidDungeonClearLocked(session, leaderRun, start.Add(time.Duration(n+1)*time.Second)); err != nil || !changed {
			t.Fatal(n, changed, err)
		}
	}
	if f.state.Battle.RewardFlow[0] == nil {
		t.Fatal("accepted true completion did not freeze reward flow")
	}
	// Existing reward-flow cases model the native 659 show at one movie after
	// the completion clock. The r167 show/selection regression clears this
	// marker when it specifically tests a late or absent natural 659.
	rewardFlow := f.state.Battle.RewardFlow[0]
	rewardFlow.PartyShownAt = rewardFlow.CompletedAt.Add(currentAntonMovieWindow(rewardFlow))
	return f, session, start.Add(8 * time.Second)
}

func zeroAntonRoll(int64) (int64, error) { return 0, nil }

func TestRealPVFAntonAutomaticPartyAndSquadKeepOwnedRecipientsAfterLobbyRemoval(t *testing.T) {
	f, _, at := firstAntonFlowFixture(t)
	flow := f.state.Battle.RewardFlow[0]
	if !flow.AutoSelectAt.Equal(at.Add(40*time.Second)) || len(flow.Recipients) != 2 || len(f.service.raidWeeklyIntents) != 0 {
		t.Fatal("real timing/eligibility not frozen", flow)
	}
	if _, errs := f.service.advanceCurrentAntonRewardFlows(flow.AutoSelectAt.Add(-time.Nanosecond), zeroAntonRoll); len(errs) != 0 || antonGrantedItemCount(t, f, "19", 10304529) != 0 {
		t.Fatal("automatic card granted before grace", errs)
	}
	delete(f.service.raids, f.state.RaidKey)
	if states, errs := f.service.advanceCurrentAntonRewardFlows(flow.AutoSelectAt, zeroAntonRoll); len(errs) != 0 || len(states) != 0 {
		t.Fatal("offline earned grants failed", states, errs)
	}
	for _, id := range []string{"19", "20"} {
		if antonGrantedItemCount(t, f, id, 10304529) != 30 || antonGrantedItemCount(t, f, id, 10093974) != 0 {
			t.Fatal("actual phase0 party reward missing before its display window", id)
		}
	}
	if len(flow.Squad) != 0 || !flow.SquadShownAt.IsZero() {
		t.Fatal("automatic party cards were replaced by squad before one movie window")
	}
	if _, errs := f.service.advanceCurrentAntonRewardFlows(flow.AutoSelectAt.Add(20*time.Second), zeroAntonRoll); len(errs) != 0 {
		t.Fatal(errs)
	}
	for _, id := range []string{"19", "20"} {
		if antonGrantedItemCount(t, f, id, 10304529) != 30 || antonGrantedItemCount(t, f, id, 10093974) != 19 {
			t.Fatal("actual phase0 party/squad missing", id)
		}
	}
	if flow.Party[19].Card == flow.Party[20].Card || flow.SquadShownAt.IsZero() || !flow.FinishedAt.IsZero() {
		t.Fatal("card ownership/squad presentation window incorrect")
	}
	if _, errs := f.service.advanceCurrentAntonRewardFlows(flow.AutoSelectAt.Add(40*time.Second), func(int64) (int64, error) { t.Fatal("cached reward rerolled"); return 0, nil }); len(errs) != 0 || flow.FinishedAt.IsZero() || !flow.BreakUntil.Equal(flow.FinishedAt.Add(600*time.Second)) {
		t.Fatal("phase finish/break timing incorrect", errs)
	}
	for _, id := range []string{"19", "20"} {
		if antonGrantedItemCount(t, f, id, 10304529) != 30 || antonGrantedItemCount(t, f, id, 10093974) != 19 {
			t.Fatal("repeat duplicated reward")
		}
	}
}

func TestRealPVFAntonAutomaticRewardsLateFailureRetainsPlanAndRollsBack(t *testing.T) {
	f, _, _ := firstAntonFlowFixture(t)
	flow := f.state.Battle.RewardFlow[0]
	original := f.repos.AccountAssets
	f.repos.AccountAssets = antonCardFailLateAssets{original}
	f.service.repositoryProvider = func() (repo.Group, bool) { return f.repos, true }
	if _, errs := f.service.advanceCurrentAntonRewardFlows(flow.AutoSelectAt, zeroAntonRoll); len(errs) != 1 || len(flow.Party) != 1 || flow.Party[19].Committed || !flow.SquadShownAt.IsZero() {
		t.Fatal("failed grant advanced/revealed", errs)
	}
	if antonGrantedItemCount(t, f, "19", 10304529) != 0 || antonGrantedItemCount(t, f, "19", 10093974) != 0 {
		t.Fatal("late failure leaked assets")
	}
	frozen := cloneCurrentAntonRewardPlan(flow.Party[19].Plan)
	f.repos.AccountAssets = original
	if _, errs := f.service.advanceCurrentAntonRewardFlows(flow.AutoSelectAt.Add(time.Second), zeroAntonRoll); len(errs) != 0 || flow.Party[19].Committed {
		t.Fatal("backoff ignored", errs)
	}
	if _, errs := f.service.advanceCurrentAntonRewardFlows(flow.AutoSelectAt.Add(2*time.Second), zeroAntonRoll); len(errs) != 0 || !flow.Party[19].Committed || !reflect.DeepEqual(flow.Party[19].Plan, frozen) {
		t.Fatal("retry lost frozen plan", errs)
	}
}

func TestRealPVFAntonNextPhaseRequiresRewardsThenLeaderStartNoNewTicket(t *testing.T) {
	f, session, _ := firstAntonFlowFixture(t)
	enterRaidStartWaitingFixture(t, f, f.state.Battle.InstanceID)
	if _, ok, err := f.service.startRuntimeRaidWithEntryCost(session); ok || err != nil {
		t.Fatal("next phase admitted before rewards", ok, err)
	}
	flow := f.state.Battle.RewardFlow[0]
	if _, errs := f.service.advanceCurrentAntonRewardFlows(flow.AutoSelectAt, zeroAntonRoll); len(errs) != 0 {
		t.Fatal(errs)
	}
	if _, errs := f.service.advanceCurrentAntonRewardFlows(flow.AutoSelectAt.Add(20*time.Second), zeroAntonRoll); len(errs) != 0 {
		t.Fatal(errs)
	}
	if _, errs := f.service.advanceCurrentAntonRewardFlows(flow.AutoSelectAt.Add(40*time.Second), zeroAntonRoll); len(errs) != 0 {
		t.Fatal(errs)
	}
	before, _, _ := f.repos.Inventory.Load(context.Background(), "19")
	state, ok, err := f.service.startRuntimeRaidWithEntryCost(session)
	if !ok || err != nil || state.Battle.Phase != 1 || state.Battle.PhaseCompleted || len(state.Battle.MemberClears) != 0 || len(state.Battle.ClearedRuns) != 8 || len(f.service.raidWeeklyIntents) != 0 {
		t.Fatal("completed phase did not prepare actual next phase", ok, err)
	}
	after, _, _ := f.repos.Inventory.Load(context.Background(), "19")
	if !reflect.DeepEqual(before, after) {
		t.Fatal("second phase charged/changed assets")
	}
	if _, ok, err := f.service.startRuntimeRaidWithEntryCost(session); ok || err != nil {
		t.Fatal("duplicate second-phase start accepted", ok, err)
	}
	if got := currentRuntimeRaidMetadata(state); got.State != 2 || got.Phase != 1 {
		t.Fatal("metadata lost stateArgument1", got)
	}
	if err := f.service.sendCurrentRaidBattleState(session, state); err != nil {
		t.Fatal(err)
	}
	stream := f.conn.write.Bytes()
	found := false
	for len(stream) > 0 {
		p, rest := splitGameServerUpperPacket(t, stream)
		if p.Header.MsgID == 587 && reflect.DeepEqual(p.Body, []byte{2, 1}) {
			found = true
		}
		stream = rest
	}
	if !found {
		t.Fatal("second phase start animation missing")
	}
}

func TestRealPVFAntonBreakClockStartsActualSecondPhaseOnce(t *testing.T) {
	f, _, _ := firstAntonFlowFixture(t)
	flow := f.state.Battle.RewardFlow[0]
	_, errs := f.service.advanceCurrentAntonRewardFlows(flow.AutoSelectAt, zeroAntonRoll)
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	_, errs = f.service.advanceCurrentAntonRewardFlows(flow.AutoSelectAt.Add(20*time.Second), zeroAntonRoll)
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	_, errs = f.service.advanceCurrentAntonRewardFlows(flow.AutoSelectAt.Add(40*time.Second), zeroAntonRoll)
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	if states, errs := f.service.advanceCurrentRaidClocks(flow.BreakUntil.Add(-time.Nanosecond)); len(states) != 0 || len(errs) != 0 || f.state.Battle.Phase != 0 {
		t.Fatal("break deadline ignored", states, errs)
	}
	// Completion leaves the members in their dungeon scene. The clock must
	// retain the phase until those same members return to the owned waiting area.
	if states, errs := f.service.advanceCurrentRaidClocks(flow.BreakUntil); len(states) != 0 || len(errs) != 0 || f.state.Battle.Phase != 0 {
		t.Fatal("next phase started outside waiting area", states, errs)
	}
	enterRaidStartWaitingFixture(t, f, f.state.Battle.InstanceID)
	states, errs := f.service.advanceCurrentRaidClocks(flow.BreakUntil)
	if len(states) != 1 || len(errs) != 0 || states[0].Battle.Phase != 1 || !states[0].Battle.EnterAfter.Equal(flow.BreakUntil.Add(3*time.Second)) || !states[0].Battle.Deadline.Equal(flow.BreakUntil.Add(2403*time.Second)) {
		t.Fatal("auto next phase timing incorrect", states, errs)
	}
	if states, errs := f.service.advanceCurrentRaidClocks(flow.BreakUntil); len(states) != 0 || len(errs) != 0 {
		t.Fatal("auto start repeated", states, errs)
	}
}

func TestRealPVFAntonFinalAutomaticRewardsWaitForOwnedWeeklyCommit(t *testing.T) {
	f, session, _, at := onlineAntonTerminalFixture(t)
	archive := f.service.initialEquipmentArchive
	config, err := raid.LoadAntonFlowConfiguration(archive)
	if err != nil {
		t.Fatal(err)
	}
	f.state.Battle.AntonFlowConfig = config
	f.service.raidMu.Lock()
	err = f.service.freezeCurrentAntonRewardFlowLocked(*f.state)
	f.service.raidMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	flow := f.state.Battle.RewardFlow[1]
	if _, errs := f.service.advanceCurrentAntonRewardFlows(flow.AutoSelectAt, zeroAntonRoll); len(errs) != 0 || len(flow.Party) != 0 || len(flow.Squad) != 0 {
		t.Fatal("uncommitted final weekly released assets", errs)
	}
	if done, errs := f.service.commitCurrentRaidWeeklyIntents(at); len(done) != 1 || len(errs) != 0 {
		t.Fatal(done, errs)
	}
	if _, errs := f.service.advanceCurrentAntonRewardFlows(flow.AutoSelectAt, zeroAntonRoll); len(errs) != 0 {
		t.Fatal(errs)
	}
	for _, id := range []string{"19", "20"} {
		if antonGrantedItemCount(t, f, id, 10304529) != 45 || antonGrantedItemCount(t, f, id, 10093974) != 52 {
			t.Fatal("actual final party/squad incorrect", id)
		}
	}
	if antonGrantedItemCount(t, f, "21", 10304529) != 0 || antonGrantedItemCount(t, f, "21", 10093974) != 0 {
		t.Fatal("observer received reward")
	}
	state := cloneRuntimeRaidState(*f.state)
	if err := f.service.sendCurrentAntonTerminalResult(session, state); err != nil {
		t.Fatal(err)
	}
	f.conn.write.Reset()
	if err := f.service.sendCurrentAntonRewardFlowProjection(session, state); err != nil {
		t.Fatal(err)
	}
	body := antonNativeCardBodies(t, f)[3]
	if len(body) != 26 || body[1] != 2 || binary.LittleEndian.Uint32(body[7:11]) != 10093974 {
		t.Fatal("current squad body not actual committed two-recipient reward", body)
	}
	writeAntonNativeCardFixture(t, "go-anton-squad-final.bin", body)
	f.conn.write.Reset()
	if err := f.service.sendCurrentAntonRewardFlowProjection(session, state); err != nil || f.conn.write.Len() != 0 {
		t.Fatal("duplicate reward flow replayed", err)
	}
	if _, errs := f.service.advanceCurrentAntonRewardFlows(flow.AutoSelectAt.Add(20*time.Second), zeroAntonRoll); len(errs) != 0 || flow.FinishedAt.IsZero() || !flow.BreakUntil.IsZero() {
		t.Fatal("final reward flow opened extra phase", errs)
	}
	if got := currentRuntimeRaidMetadata(cloneRuntimeRaidState(*f.state)); got.State != 4 || got.Phase != 0 {
		t.Fatal("final success metadata wrong", got)
	}
}

func TestRealPVFAntonFlowSnapshotsDoNotShareEarnedPlans(t *testing.T) {
	f, _, _ := firstAntonFlowFixture(t)
	flow := f.state.Battle.RewardFlow[0]
	if _, errs := f.service.advanceCurrentAntonRewardFlows(flow.AutoSelectAt, zeroAntonRoll); len(errs) != 0 {
		t.Fatal(errs)
	}
	if _, errs := f.service.advanceCurrentAntonRewardFlows(flow.AutoSelectAt.Add(20*time.Second), zeroAntonRoll); len(errs) != 0 {
		t.Fatal(errs)
	}
	copy := cloneRuntimeRaidState(*f.state)
	copy.Battle.RewardFlow[0].Recipients[0].AccountID = "foreign"
	copy.Battle.RewardFlow[0].Party[19].Committed = false
	copy.Battle.RewardFlow[0].Squad[19].Plan.Rewards[0].Count = 999
	if flow.Recipients[0].AccountID != "account-1" || !flow.Party[19].Committed || flow.Squad[19].Plan.Rewards[0].Count == 999 {
		t.Fatal("snapshot mutated live earned grants")
	}
}

func TestRealPVFAntonFlowRareSquadGrantsContentsAndCurrentNestedDisplay(t *testing.T) {
	f, session, _ := firstAntonFlowFixture(t)
	flow := f.state.Battle.RewardFlow[0]
	roll := func(bound int64) (int64, error) {
		if bound == 100 {
			return 98, nil
		}
		return 0, nil
	}
	if _, errs := f.service.advanceCurrentAntonRewardFlows(flow.AutoSelectAt, roll); len(errs) != 0 {
		t.Fatal(errs)
	}
	if _, errs := f.service.advanceCurrentAntonRewardFlows(flow.AutoSelectAt.Add(20*time.Second), roll); len(errs) != 0 {
		t.Fatal(errs)
	}
	for _, id := range []string{"19", "20"} {
		if antonGrantedItemCount(t, f, id, 10304483) != 0 || antonGrantedItemCount(t, f, id, 10093974) != 20 || antonGrantedItemCount(t, f, id, 100050518) != 1 {
			t.Fatal("rare squad final materials/equipment missing or wrapper awarded", id)
		}
	}
	state := cloneRuntimeRaidState(*f.state)
	if err := f.service.sendCurrentAntonTerminalResult(session, state); err != nil {
		t.Fatal(err)
	}
	f.conn.write.Reset()
	if err := f.service.sendCurrentAntonRewardFlowProjection(session, state); err != nil {
		t.Fatal(err)
	}
	body := antonNativeCardBodies(t, f)[3]
	if len(body) != 42 || binary.LittleEndian.Uint32(body[7:11]) != 10093974 || binary.LittleEndian.Uint32(body[15:19]) != 100050518 || body[13] != 1 || body[21] != 1 {
		t.Fatal("rare current squad descriptor changed", body)
	}
	writeAntonNativeCardFixture(t, "go-anton-squad-rare-first.bin", body)
	copyFlow := cloneCurrentAntonRewardFlow(flow)
	if _, errs := f.service.advanceCurrentAntonRewardFlows(flow.AutoSelectAt.Add(20*time.Second), zeroAntonRoll); len(errs) != 0 {
		t.Fatal(errs)
	}
	if _, errs := f.service.advanceCurrentAntonRewardFlows(flow.AutoSelectAt.Add(40*time.Second), zeroAntonRoll); len(errs) != 0 {
		t.Fatal(errs)
	}
	if copyFlow.FinishedAt == flow.FinishedAt || !copyFlow.FinishedAt.IsZero() {
		t.Fatal("display snapshot shares finish mutation")
	}
	f.conn.write.Reset()
	if err := f.service.sendCurrentAntonRewardFlowProjection(session, cloneRuntimeRaidState(*f.state)); err != nil {
		t.Fatal(err)
	}
	sawBreak, sawTimer, sawReturn := false, false, false
	stream := f.conn.write.Bytes()
	for len(stream) > 0 {
		p, rest := splitGameServerUpperPacket(t, stream)
		if p.Header.MsgID == 600 {
			t.Fatal("finishing the squad display replayed its already delivered cards", p.Body)
		}
		if p.Header.MsgID == 587 && reflect.DeepEqual(p.Body, []byte{5, 0}) {
			sawBreak = true
		}
		if p.Header.MsgID == 596 && binary.LittleEndian.Uint32(p.Body[9:13]) == 600 {
			sawTimer = true
		}
		if p.Header.MsgID == 115 && reflect.DeepEqual(p.Body, []byte{0}) {
			sawReturn = true
		}
		stream = rest
	}
	if !sawBreak || !sawTimer || !sawReturn {
		t.Fatal("finished phase did not restore actual break/owned return", sawBreak, sawTimer, sawReturn)
	}
	// A late movie completion must ACK without covering the standby state.
	f.conn.write.Reset()
	dispatchAntonCardRequest(t, f, session, 659, []byte{0})
	packet, rest := splitGameServerUpperPacket(t, f.conn.write.Bytes())
	if packet.Header.MsgID != 659 || len(rest) != 0 {
		t.Fatal("late movie reopened old cards", packet, rest)
	}
}

func TestRealPVFAntonNextPhaseRejectsForeignAndUnfinishedOwners(t *testing.T) {
	f, session, _ := firstAntonFlowFixture(t)
	flow := f.state.Battle.RewardFlow[0]
	if _, errs := f.service.advanceCurrentAntonRewardFlows(flow.AutoSelectAt, zeroAntonRoll); len(errs) != 0 {
		t.Fatal(errs)
	}
	if _, errs := f.service.advanceCurrentAntonRewardFlows(flow.AutoSelectAt.Add(20*time.Second), zeroAntonRoll); len(errs) != 0 {
		t.Fatal(errs)
	}
	if _, errs := f.service.advanceCurrentAntonRewardFlows(flow.AutoSelectAt.Add(40*time.Second), zeroAntonRoll); len(errs) != 0 {
		t.Fatal(errs)
	}
	base := cloneRuntimeRaidState(*f.state)
	for _, name := range []string{"login", "account", "channel", "leader", "phase", "unfinished", "timeout", "missing_flow", "unfinished_rewards", "uncommitted_squad", "group", "duplicate", "oversized", "limits"} {
		t.Run(name, func(t *testing.T) {
			state := cloneRuntimeRaidState(base)
			f.service.raids[state.RaidKey] = &state
			login, account, channel := session.loginPacketSeen, session.accountID, session.channel.ID
			defer func() { session.loginPacketSeen, session.accountID, session.channel.ID = login, account, channel }()
			switch name {
			case "login":
				session.loginPacketSeen = false
			case "account":
				session.accountID = "foreign"
			case "channel":
				session.channel.ID++
			case "leader":
				state.LeaderID = 20
			case "phase":
				state.Battle.Phase = 1
			case "unfinished":
				state.Battle.PhaseCompleted = false
			case "timeout":
				state.Battle.TimedOut = true
			case "missing_flow":
				state.Battle.RewardFlow[0] = nil
			case "unfinished_rewards":
				state.Battle.RewardFlow[0].FinishedAt = time.Time{}
			case "uncommitted_squad":
				state.Battle.RewardFlow[0].Squad[19].Committed = false
			case "group":
				state.Members[0].GroupIndex = 0
			case "oversized":
				state.Members = append(state.Members, state.Members[0], state.Members[0], state.Members[0])
			case "duplicate":
				state.Members = append(state.Members, state.Members[0])
			case "limits":
				state.Battle.Configuration.PhaseLimitSeconds = nil
			}
			before := cloneRuntimeRaidState(state)
			if _, ok, _ := f.service.startRuntimeRaidWithEntryCost(session); ok || !reflect.DeepEqual(before, state) {
				t.Fatal("foreign next phase admitted/mutated")
			}
		})
	}
}

func TestRealPVFAntonFlowPartialProjectionClosesAndNewOwnedConnectionRestoresWithoutGrant(t *testing.T) {
	f, session, _ := firstAntonFlowFixture(t)
	flow := f.state.Battle.RewardFlow[0]
	if _, errs := f.service.advanceCurrentAntonRewardFlows(flow.AutoSelectAt, zeroAntonRoll); len(errs) != 0 {
		t.Fatal(errs)
	}
	if _, errs := f.service.advanceCurrentAntonRewardFlows(flow.AutoSelectAt.Add(20*time.Second), zeroAntonRoll); len(errs) != 0 {
		t.Fatal(errs)
	}
	state := cloneRuntimeRaidState(*f.state)
	if err := f.service.sendCurrentAntonTerminalResult(session, state); err != nil {
		t.Fatal(err)
	}
	f.conn.write.Reset()
	conn := &antonPartialWriteConn{Conn: session.conn}
	session.conn = conn
	if err := f.service.sendCurrentAntonRewardFlowProjection(session, state); err == nil || !conn.closed || !session.raidProjection.failed || session.raidProjection.rewardFlowSignature != [32]byte{} {
		t.Fatal("partial squad batch accepted", err)
	}
	writes := conn.calls
	if err := f.service.sendCurrentAntonRewardFlowProjection(session, state); err == nil || writes != conn.calls {
		t.Fatal("partial squad replayed", err)
	}
	newConn := &bufferConn{}
	replacement := f.session()
	replacement.conn, replacement.loginPacketSeen, replacement.accountFromToken = newConn, true, true
	f.service.bindGameSessionCharacter(replacement, 19)
	if err := f.service.sendCurrentAntonTerminalResult(replacement, state); err != nil {
		t.Fatal(err)
	}
	newConn.write.Reset()
	if err := f.service.sendCurrentAntonRewardFlowProjection(replacement, state); err != nil {
		t.Fatal(err)
	}
	stream, seen := newConn.write.Bytes(), false
	for len(stream) > 0 {
		packet, rest := splitGameServerUpperPacket(t, stream)
		if packet.Header.MsgID == 600 && packet.Body[0] == 3 {
			seen = true
		}
		stream = rest
	}
	if !seen || antonGrantedItemCount(t, f, "19", 10304529) != 30 || antonGrantedItemCount(t, f, "19", 10093974) != 19 {
		t.Fatal("reconnect lost/duplicated committed squad")
	}
}
