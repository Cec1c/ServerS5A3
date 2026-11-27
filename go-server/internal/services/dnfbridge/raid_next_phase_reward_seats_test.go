package dnfbridge

import (
	"reflect"
	"strconv"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/raid"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestRealPVFAntonSecondPhase644WithNinePersonalReceipts(t *testing.T) {
	catalog, rewards := actualAntonRewardFixture(t)
	archive := catalog.source.(*platformpvf.Archive)
	f := raidSafetyFixture(t, archive, 14)
	warmAntonRewardQuestFixture(t, f)
	f.service.options.currentClientVersion = "official-24.9"
	f.state.Started, f.state.CurrentLayout, f.state.TailFlag = true, true, 0
	program, err := raid.LoadAntonProgram(archive)
	if err != nil {
		t.Fatal(err)
	}
	config, err := raid.LoadBattleConfiguration(archive, "etc/raid/anton.etc")
	if err != nil {
		t.Fatal(err)
	}
	flowConfig, err := raid.LoadAntonFlowConfiguration(archive)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now().Add(-20 * time.Minute)
	progress, err := program.BeginPhase(0, start, 91321)
	if err != nil {
		t.Fatal(err)
	}
	b := &runtimeRaidBattle{InstanceID: "31415926535897932384626433832795", Mode: 1,
		StartedAt: start, EnterAfter: start, Deadline: start.Add(time.Duration(config.PhaseLimitSeconds[0]) * time.Second),
		Configuration: config, Anton: progress, Dungeons: progress.Dungeons(), AntonRewards: &rewards, AntonFlowConfig: flowConfig,
		MemberGroups: map[uint16]byte{}, MemberAccounts: map[uint16]string{}}
	f.state.Battle = b
	var peers []*gameSession
	for i := range f.state.Members {
		m := &f.state.Members[i]
		p := f.session()
		p.conn = &bufferConn{}
		p.selectedCharacterID = m.CharID
		if i > 0 {
			p.accountID = "account-" + strconv.Itoa(int(m.CharID))
		}
		p.loginPacketSeen, p.accountFromToken = true, true
		f.service.bindGameSessionCharacter(p, m.CharID)
		b.ChannelID, b.ChannelType = p.channel.ID, p.channel.Type
		m.GroupIndex, m.SlotOrder, m.OrdinaryPartySolo = byte(i/4+1), byte(i%4), i >= 12
		b.MemberGroups[m.CharID], b.MemberAccounts[m.CharID] = m.GroupIndex, p.accountID
		peers = append(peers, p)
	}
	parties := [][]*gameSession{peers[:4], peers[4:8], peers[8:12], peers[12:13], peers[13:]}
	for _, party := range parties[:3] {
		a02RewardPartyWithSessionGenerations(t, f.service, party...)
	}
	clear := func(dungeon uint32, n int, party []*gameSession) {
		t.Helper()
		run := raidCompletedRuntimeFixture(t, party[0], dungeon)
		var entries []*runtimePartyPreparedDungeonEntry
		for _, p := range party[1:] {
			entries = append(entries, &runtimePartyPreparedDungeonEntry{session: p, runtime: raidCompletedRuntimeFixture(t, p, dungeon)})
		}
		if err := f.service.freezeCurrentRaidDungeonGroupEntry(party[0], run, entries); err != nil {
			t.Fatal(n, err)
		}
		if _, ok, err := f.service.reduceCurrentRaidDungeonClearLocked(party[0], run, b.EnterAfter.Add(time.Duration(n+1)*time.Second)); err != nil || !ok {
			t.Fatal(n, ok, err)
		}
	}
	for i, id := range []uint32{210, 210, 210, 210, 213, 213, 215, 215} {
		clear(id, i, parties[i%5])
	}
	first := b.RewardFlow[0]
	if first == nil || len(first.Recipients) != 14 {
		t.Fatal("first phase recipients")
	}
	for _, offset := range []time.Duration{0, 20 * time.Second, 40 * time.Second, 80 * time.Second} {
		if _, errs := f.service.advanceCurrentAntonRewardFlows(first.AutoSelectAt.Add(offset), zeroAntonRoll); len(errs) > 0 {
			t.Fatal(errs)
		}
	}
	for i := 12; i < 14; i++ {
		f.state.Members[i].GroupIndex = 1
	}
	live := append([]runtimeRaidMemberState(nil), f.state.Members...)
	enterRaidStartWaitingFixture(t, f, b.InstanceID)
	if ok, err := f.service.beginCurrentAntonNextPhaseLocked(f.state, first.BreakUntil); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if !reflect.DeepEqual(live, f.state.Members) {
		t.Fatal("changed live formation")
	}
	// Hold the automatic freeze until nine real manual grants have committed.
	b.AntonFlowConfig.PhaseBreakSeconds = 0
	for i, id := range []uint32{219, 220, 220, 220, 220, 220} {
		clear(id, i, parties[i%5])
	}
	b.AntonFlowConfig = flowConfig
	if _, errs := f.service.commitCurrentRaidWeeklyIntents(time.Now()); len(errs) > 0 {
		t.Fatal(errs)
	}
	for _, p := range peers[:9] {
		if err := f.service.sendCurrentAntonTerminalResult(p, cloneRuntimeRaidState(*f.state)); err != nil {
			t.Fatal(err)
		}
		dispatchAntonCardRequest(t, f, p, 659, []byte{1})
		dispatchAntonCardRequest(t, f, p, 660, []byte{1, 0})
		card := b.RewardCards[1][p.selectedCharacterID]
		if card == nil || !card.Committed {
			t.Fatal("missing manual grant", p.selectedCharacterID)
		}
	}
	if len(b.RewardCards[1]) != 9 {
		t.Fatal("expected nine manual cards")
	}
	manual := b.RewardCards[1]
	if err := f.service.freezeCurrentAntonRewardFlowLocked(*f.state); err != nil {
		t.Fatal("second phase freeze:", err)
	}
	flow := b.RewardFlow[1]
	if len(flow.Recipients) != 14 {
		t.Fatal("lost eligible owner")
	}
	for _, p := range peers[:9] {
		prior := manual[p.selectedCharacterID]
		card := flow.Party[p.selectedCharacterID]
		if card == nil || !card.Committed || card.Card != prior.Card || !reflect.DeepEqual(card.Plan, prior.Plan) {
			t.Fatal("lost committed personal card")
		}
	}
	at := time.Now().Add(2 * time.Minute)
	if _, errs := f.service.advanceCurrentAntonRewardFlows(at, zeroAntonRoll); len(errs) > 0 {
		t.Fatal(errs)
	}
	for _, party := range parties {
		positions := map[byte]bool{}
		for _, p := range party {
			id := p.selectedCharacterID
			if !currentRaidOwnerRewardsCommitted(flow, id) {
				t.Fatal("missing final reward", id)
			}
			card := flow.Party[id].Card
			if card > 3 || positions[card] || len(party) == 1 && card != 0 {
				t.Fatal("incorrect personal slot", id, card)
			}
			positions[card] = true
			p.conn.(*bufferConn).write.Reset()
			if err := f.service.sendCurrentAntonTerminalResult(p, cloneRuntimeRaidState(*f.state)); err != nil {
				t.Fatal(err)
			}
			dispatchAntonCardRequest(t, f, p, 659, []byte{1})
			wire := r195ReadFiendRewardWire(t, p.conn.(*bufferConn).write.Bytes())
			if len(wire.owners[3]) != 14 || len(wire.owners[1]) != len(party) {
				t.Fatal("incomplete reward page", id, wire)
			}
			before := r195PublicRewardAssetSnapshot(t, f.service, p)
			dispatchAntonCardRequest(t, f, p, 660, []byte{1, 3})
			if after := r195PublicRewardAssetSnapshot(t, f.service, p); !reflect.DeepEqual(before, after) {
				t.Fatal("replayed grant", id)
			}
		}
	}
}
