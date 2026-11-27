package dnfbridge

import (
	"reflect"
	"strconv"
	"testing"
	"time"

	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestRealPVFAntonFirstPhase4441SoloSwapRewardSeats(t *testing.T) {
	catalog, _ := actualAntonRewardFixture(t)
	archive := catalog.source.(*platformpvf.Archive)
	f := raidSafetyFixture(t, archive, 13)
	f.service.pvfItemCatalog = catalog
	warmAntonRewardQuestFixture(t, f)
	f.service.options.currentClientVersion = "official-24.9"
	f.state.Started = false
	f.state.CurrentLayout = true
	f.state.TailFlag = 0
	f.state.CurrentUIFlags = [3]byte{}

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
		if i < 12 {
			m.GroupIndex, m.SlotOrder = byte(i/4+1), byte(i%4)
		} else {
			// Reproduce the 2026-11-27 13-player roster: 3 full 4-member ordinary
			// parties plus 1 unpartied solo retaining stale GroupIndex 2.
			m.GroupIndex, m.SlotOrder = 2, 0
		}
		peers = append(peers, p)
	}
	party1 := peers[:4]
	party2Initial := peers[4:8]
	party3 := peers[8:12]
	for _, party := range [][]*gameSession{party1, party2Initial, party3} {
		a02RewardPartyWithSessionGenerations(t, f.service, party...)
	}
	enterRaidStartWaitingFixture(t, f, "")

	_, ok, err := f.service.startRuntimeRaidWithEntryCost(peers[0])
	if err != nil || !ok {
		t.Fatalf("start raid: ok=%v err=%v", ok, err)
	}
	b := f.state.Battle
	if !f.state.Started || b == nil {
		t.Fatal("expected started raid battle")
	}
	if !f.state.Members[12].OrdinaryPartySolo || f.state.Members[12].GroupIndex != 2 {
		t.Fatalf("live solo projection changed unexpectedly: %+v", f.state.Members[12])
	}
	// Advance past the start-delay gate while preserving the configured phase window.
	clearBase := b.EnterAfter
	b.EnterAfter = time.Now().Add(-time.Minute)
	b.Deadline = b.EnterAfter.Add(time.Duration(b.Configuration.PhaseLimitSeconds[0]) * time.Second)

	clear := func(dungeon uint32, n int, party []*gameSession) {
		t.Helper()
		run := raidCompletedRuntimeFixture(t, party[0], dungeon)
		var entries []*runtimePartyPreparedDungeonEntry
		for _, p := range party[1:] {
			entries = append(entries, &runtimePartyPreparedDungeonEntry{
				session: p,
				runtime: raidCompletedRuntimeFixture(t, p, dungeon),
			})
		}
		if err := f.service.freezeCurrentRaidDungeonGroupEntry(party[0], run, entries); err != nil {
			t.Fatal(n, err)
		}
		if _, changed, err := f.service.reduceCurrentRaidDungeonClearLocked(
			party[0], run, clearBase.Add(time.Duration(n+1)*time.Second),
		); err != nil || !changed {
			t.Fatal(n, changed, err)
		}
	}

	// Complete 6 dungeons so the first 12 members each earn 2 clears.
	clear(210, 0, party1)
	clear(210, 1, party1)
	clear(210, 2, party2Initial)
	clear(210, 3, party2Initial)
	clear(213, 4, party3)
	clear(213, 5, party3)

	// Mid-phase swap in group 2: peers[7] leaves party 2 (becomes solo) and
	// peers[12] joins party 2 to complete the last 2 dungeons, qualifying all 13.
	f.service.runtimePartyManagerForService().LeaveCurrent(peers[7].selectedCharacterID)
	party2Swapped := []*gameSession{peers[4], peers[5], peers[6], peers[12]}
	a02RewardPartyWithSessionGenerations(t, f.service, party2Swapped...)
	f.state.Members[7].OrdinaryPartySolo = true
	f.state.Members[12].OrdinaryPartySolo = false
	f.state.Members[12].SlotOrder = 3

	clear(215, 6, party2Swapped)
	clear(215, 7, party2Swapped)

	if !b.PhaseCompleted {
		t.Fatal("expected phase 0 completed")
	}
	flow := b.RewardFlow[0]
	if flow == nil {
		err := f.service.freezeCurrentAntonRewardFlowLocked(*f.state)
		t.Fatalf("phase 0 reward flow failed to freeze: %v", err)
	}
	if len(flow.Recipients) != 13 {
		t.Fatalf("phase 0 recipients=%d want=13", len(flow.Recipients))
	}

	groupCounts := map[byte]int{}
	for _, member := range f.state.Members {
		g := b.MemberGroups[member.CharID]
		if g < 1 || g > runtimeRaidMaxGroups {
			t.Fatalf("invalid frozen reward group for %d: %d", member.CharID, g)
		}
		groupCounts[g]++
		if groupCounts[g] > runtimeRaidGroupSize {
			t.Fatalf("frozen reward group %d exceeded capacity: %d", g, groupCounts[g])
		}
	}

	// Simulate 9 manual personal-card clicks (including former solo peers[12])
	// plus 4 unattended auto-selects (including departed solo peers[7]).
	manualPeers := []*gameSession{
		peers[0], peers[1], peers[2],
		peers[4], peers[5], peers[12],
		peers[8], peers[9], peers[10],
	}
	for _, p := range manualPeers {
		if err := f.service.sendCurrentAntonTerminalResult(p, cloneRuntimeRaidState(*f.state)); err != nil {
			t.Fatal(err)
		}
		dispatchAntonCardRequest(t, f, p, 659, []byte{1})
		dispatchAntonCardRequest(t, f, p, 660, []byte{1, 0})
		card := flow.Party[p.selectedCharacterID]
		if card == nil || !card.Committed {
			t.Fatalf("missing manual personal grant for %d", p.selectedCharacterID)
		}
	}

	at := time.Now().Add(2 * time.Minute)
	if flow.AutoSelectAt.After(at) {
		at = flow.AutoSelectAt
	}
	for _, offset := range []time.Duration{0, 20 * time.Second, 40 * time.Second, 80 * time.Second} {
		if _, errs := f.service.advanceCurrentAntonRewardFlows(at.Add(offset), zeroAntonRoll); len(errs) > 0 {
			t.Fatal(errs)
		}
	}

	finalParties := [][]*gameSession{party1, party2Swapped, party3, {peers[7]}}
	for _, party := range finalParties {
		positions := map[byte]bool{}
		for _, p := range party {
			id := p.selectedCharacterID
			if !currentRaidOwnerRewardsCommitted(flow, id) {
				t.Fatalf("missing final phase 0 reward for %d", id)
			}
			card := flow.Party[id].Card
			if card > 3 || positions[card] || len(party) == 1 && card != 0 {
				t.Fatalf("incorrect personal card slot for %d: %d (party size %d)", id, card, len(party))
			}
			positions[card] = true
			p.conn.(*bufferConn).write.Reset()
			if err := f.service.sendCurrentAntonTerminalResult(p, cloneRuntimeRaidState(*f.state)); err != nil {
				t.Fatal(err)
			}
			dispatchAntonCardRequest(t, f, p, 659, []byte{1})
			wire := r195ReadFiendRewardWire(t, p.conn.(*bufferConn).write.Bytes())
			if len(wire.owners[3]) != 13 || len(wire.owners[1]) != len(party) {
				t.Fatalf("incomplete reward page for %d: got party=%d squad=%d want party=%d squad=13",
					id, len(wire.owners[1]), len(wire.owners[3]), len(party))
			}
			before := r195PublicRewardAssetSnapshot(t, f.service, p)
			dispatchAntonCardRequest(t, f, p, 660, []byte{1, 3})
			if after := r195PublicRewardAssetSnapshot(t, f.service, p); !reflect.DeepEqual(before, after) {
				t.Fatalf("replayed grant for %d", id)
			}
		}
	}

	enterRaidStartWaitingFixture(t, f, b.InstanceID)
	if ok, err := f.service.beginCurrentAntonNextPhaseLocked(f.state, flow.BreakUntil); !ok || err != nil {
		t.Fatalf("enter phase 1 after phase 0 settlement: ok=%v err=%v", ok, err)
	}
	if b.Phase != 1 {
		t.Fatalf("expected phase 1, got %d", b.Phase)
	}
}

func TestLukeFirstAndSecondPhaseSoloCollisionRewardGroups(t *testing.T) {
	state := runtimeRaidState{
		CurrentLayout:   true,
		RouteOrRaidType: 1,
		CurrentUIFlags:  [3]byte{0, 0, 0},
		Members: []runtimeRaidMemberState{
			{CharID: 101, GroupIndex: 1, SlotOrder: 0},
			{CharID: 102, GroupIndex: 1, SlotOrder: 1},
			{CharID: 103, GroupIndex: 1, SlotOrder: 2},
			{CharID: 104, GroupIndex: 1, SlotOrder: 3},
			{CharID: 105, GroupIndex: 2, SlotOrder: 0},
			{CharID: 106, GroupIndex: 2, SlotOrder: 1},
			{CharID: 107, GroupIndex: 1, SlotOrder: 0, OrdinaryPartySolo: true},
			{CharID: 108, GroupIndex: 1, SlotOrder: 1, OrdinaryPartySolo: true},
		},
	}
	before := append([]runtimeRaidMemberState(nil), state.Members...)

	phase0, err := boundedCurrentRaidRewardGroups(state, nil, 2, false)
	if err != nil {
		t.Fatalf("luke phase 0 reward groups: %v", err)
	}
	if !reflect.DeepEqual(before, state.Members) {
		t.Fatal("phase 0 reward grouping mutated live formation")
	}
	assertBounded := func(label string, groups map[uint16]byte) {
		t.Helper()
		counts := map[byte]int{}
		for _, member := range state.Members {
			g := groups[member.CharID]
			if g < 1 || g > 5 {
				t.Fatalf("%s: char %d assigned out-of-range Luke group %d", label, member.CharID, g)
			}
			counts[g]++
			if counts[g] > runtimeRaidGroupSize {
				t.Fatalf("%s: group %d exceeded capacity: %d", label, g, counts[g])
			}
		}
		for _, id := range []uint16{101, 102, 103, 104} {
			if groups[id] != 1 {
				t.Fatalf("%s: ordinary party 1 member %d moved to %d", label, id, groups[id])
			}
		}
		for _, id := range []uint16{105, 106} {
			if groups[id] != 2 {
				t.Fatalf("%s: ordinary party 2 member %d moved to %d", label, id, groups[id])
			}
		}
	}
	assertBounded("phase0", phase0)

	planned := state
	planned.Battle = &runtimeRaidBattle{
		Mode:         2,
		Single:       false,
		MemberGroups: phase0,
	}
	phase1, err := nextPhaseRewardGroups(planned)
	if err != nil {
		t.Fatalf("luke phase 1 reward groups: %v", err)
	}
	if !reflect.DeepEqual(before, planned.Members) {
		t.Fatal("phase 1 reward grouping mutated live formation")
	}
	assertBounded("phase1", phase1)

	singleState := runtimeRaidState{
		CurrentLayout:   true,
		RouteOrRaidType: 1,
		CurrentUIFlags:  [3]byte{1, 0, 0},
		Members:         []runtimeRaidMemberState{{CharID: 201, GroupIndex: 1, OrdinaryPartySolo: true}},
	}
	singleGroups, err := boundedCurrentRaidRewardGroups(singleState, nil, 2, true)
	if err != nil || singleGroups[201] != 1 {
		t.Fatalf("single luke groups=%v err=%v", singleGroups, err)
	}
}
