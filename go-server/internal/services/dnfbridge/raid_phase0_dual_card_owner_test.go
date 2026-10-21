package dnfbridge

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/raid"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func phase0RewardFlowWithGroups(t *testing.T, groups []byte) (*raidStartGuardFixture, []*gameSession, time.Time) {
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
	f := raidSafetyFixture(t, archive, len(groups))
	f.service.options.currentClientVersion = "official-24.9"
	sessions := make([]*gameSession, len(groups))
	accounts := make(map[uint16]string, len(groups))
	for i, group := range groups {
		id := uint16(19 + i)
		s := f.session()
		account := "account-1"
		if id != 19 {
			account = fmt.Sprintf("account-%d", id)
		}
		s.selectedCharacterID, s.accountID = id, account
		s.loginPacketSeen, s.accountFromToken = true, true
		f.service.bindGameSessionCharacter(s, id)
		sessions[i] = s
		f.state.Members[i].GroupIndex = group
		f.state.Members[i].SlotOrder = 0
		for j := 0; j < i; j++ {
			if groups[j] == group {
				f.state.Members[i].SlotOrder++
			}
		}
		accounts[id] = s.accountID
	}
	start := time.Now().Add(-2 * time.Minute)
	progress, err := program.BeginPhase(0, start, 91321)
	if err != nil {
		t.Fatal(err)
	}
	f.state.Started = true
	f.state.TailFlag, f.state.CurrentLayout, f.state.CurrentUIFlags = 0, true, [3]byte{}
	f.state.Battle = &runtimeRaidBattle{InstanceID: "33445566778899aabbccddeeff001122", Mode: 1, ChannelID: sessions[0].channel.ID, ChannelType: sessions[0].channel.Type, StartedAt: start, EnterAfter: start, Deadline: start.Add(time.Duration(config.PhaseLimitSeconds[0]) * time.Second), Configuration: config, Anton: progress, AntonRewards: &rewards, AntonFlowConfig: flow, Dungeons: progress.Dungeons(), MemberAccounts: accounts, MemberGroups: map[uint16]byte{}}
	for i, group := range groups {
		id := uint16(19 + i)
		f.state.Battle.MemberGroups[id] = group
	}
	groupSessions := map[byte][]*gameSession{}
	var groupOrder []byte
	for i, group := range groups {
		if _, seen := groupSessions[group]; !seen {
			groupOrder = append(groupOrder, group)
		}
		groupSessions[group] = append(groupSessions[group], sessions[i])
	}
	for n, dungeon := range []uint32{210, 210, 210, 210, 213, 213, 215, 215} {
		owners := groupSessions[groupOrder[n%len(groupOrder)]]
		leader := owners[0]
		leaderRun := raidCompletedRuntimeFixture(t, leader, dungeon)
		peers := make([]*runtimePartyPreparedDungeonEntry, 0, len(owners)-1)
		for _, peer := range owners[1:] {
			peers = append(peers, &runtimePartyPreparedDungeonEntry{session: peer, runtime: raidCompletedRuntimeFixture(t, peer, dungeon)})
		}
		if err := f.service.freezeCurrentRaidDungeonGroupEntry(leader, leaderRun, peers); err != nil {
			t.Fatal(err)
		}
		if _, changed, err := f.service.reduceCurrentRaidDungeonClearLocked(leader, leaderRun, start.Add(time.Duration(n+1)*time.Second)); err != nil || !changed {
			t.Fatal(n, changed, err)
		}
	}
	// Existing capacity/reward cases model the native 659 show one movie after
	// completion. The dedicated r167 timing regression clears this marker.
	if flow := f.state.Battle.RewardFlow[0]; flow != nil {
		flow.PartyShownAt = flow.CompletedAt.Add(currentAntonMovieWindow(flow))
	}
	return f, sessions, start.Add(8 * time.Second)
}

func TestRealPVFAntonPhase0FourAndMultiGroupStageCapacity(t *testing.T) {
	for _, tc := range []struct {
		name   string
		groups []byte
		party  []uint16
	}{
		{"four_same_group", []byte{1, 1, 1, 1}, []uint16{19, 20, 21, 22}},
		{"four_plus_one", []byte{1, 1, 1, 1, 2}, []uint16{19, 20, 21, 22}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, sessions, _ := phase0RewardFlowWithGroups(t, tc.groups)
			flow := f.state.Battle.RewardFlow[0]
			if flow == nil || len(flow.Recipients) != len(tc.groups) {
				t.Fatalf("phase0 recipients=%d want=%d", len(flow.Recipients), len(tc.groups))
			}
			state := cloneRuntimeRaidState(*f.state)
			if err := f.service.sendCurrentAntonTerminalResult(sessions[0], state); err != nil {
				t.Fatal(err)
			}
			f.conn.write.Reset()
			dispatchAntonCardRequest(t, f, sessions[0], 659, []byte{0})
			var owners []uint16
			for stream := f.conn.write.Bytes(); len(stream) > 0; {
				packet, rest := splitGameServerUpperPacket(t, stream)
				stream = rest
				if packet.Header.MsgID == 600 && packet.Body[0] == 1 {
					owners = append(owners, parsePhase0CardOwners(t, packet.Body)...)
				}
			}
			if len(owners) != len(tc.party) {
				t.Fatalf("party capacity owners=%v want=%v", owners, tc.party)
			}
			for i := range tc.party {
				if owners[i] != tc.party[i] {
					t.Fatalf("party capacity owners=%v want=%v", owners, tc.party)
				}
			}
			if _, errs := f.service.advanceCurrentAntonRewardFlows(flow.AutoSelectAt, zeroAntonRoll); len(errs) != 0 || len(flow.Squad) != 0 {
				t.Fatalf("squad opened before party display: squad=%d errs=%v", len(flow.Squad), errs)
			}
			if _, errs := f.service.advanceCurrentAntonRewardFlows(flow.AutoSelectAt.Add(20*time.Second), zeroAntonRoll); len(errs) != 0 || len(flow.Squad) != len(tc.groups) {
				t.Fatalf("squad capacity=%d want=%d errs=%v", len(flow.Squad), len(tc.groups), errs)
			}
		})
	}
}

// Current v151 NOTI600 reads kind and card count and accepts kind0/kind1
// through the same consumer class. This records the real phase0 response and
// does not turn the presence or absence of the empty gold description into a
// product expectation without native display evidence.
func TestRealPVFAntonPhase0Natural659CardMasksAndOwnerCapacity(t *testing.T) {
	f, session, _ := firstAntonFlowFixture(t)
	state := cloneRuntimeRaidState(*f.state)
	if err := f.service.sendCurrentAntonTerminalResult(session, state); err != nil {
		t.Fatal(err)
	}
	f.conn.write.Reset()
	dispatchAntonCardRequest(t, f, session, 659, []byte{0})

	var starts, acks, itemLists, goldLists int
	var itemOwners, goldOwners []uint16
	for stream := f.conn.write.Bytes(); len(stream) > 0; {
		packet, rest := splitGameServerUpperPacket(t, stream)
		stream = rest
		switch packet.Header.MsgID {
		case 587:
			if bytes.Equal(packet.Body, []byte{4, 0}) {
				starts++
			}
		case 659:
			if bytes.Equal(packet.Body, []byte{1}) {
				acks++
			}
		case 600:
			if len(packet.Body) < 2 {
				t.Fatalf("short phase0 card list: %x", packet.Body)
			}
			switch packet.Body[0] {
			case 1:
				itemLists++
				itemOwners = append(itemOwners, parsePhase0CardOwners(t, packet.Body)...)
			case 0:
				goldLists++
				goldOwners = append(goldOwners, parsePhase0CardOwners(t, packet.Body)...)
			}
		}
	}
	if acks != 1 || starts != 1 || itemLists != 1 {
		t.Fatalf("phase0 natural 659 screen shape: ack=%d starts=%d item_lists=%d", acks, starts, itemLists)
	}
	if len(itemOwners) != 2 || itemOwners[0] != 19 || itemOwners[1] != 20 {
		t.Fatalf("phase0 party capacity/owners=%v want [19 20]", itemOwners)
	}
	if goldLists != 1 || len(goldOwners) != 2 || goldOwners[0] != 19 || goldOwners[1] != 20 {
		t.Fatalf("phase0 gold mask count/owners: lists=%d owners=%v", goldLists, goldOwners)
	}
	if antonGrantedItemCount(t, f, "19", 10304529) != 0 {
		t.Fatal("natural 659 created a reward without a 660 selection")
	}
}

func TestRealPVFAntonPhase0SquadStageTimingAndCapacity(t *testing.T) {
	f, session, _ := firstAntonFlowFixture(t)
	flow := f.state.Battle.RewardFlow[0]
	state := cloneRuntimeRaidState(*f.state)
	if err := f.service.sendCurrentAntonTerminalResult(session, state); err != nil {
		t.Fatal(err)
	}
	if _, errs := f.service.advanceCurrentAntonRewardFlows(flow.AutoSelectAt.Add(-time.Nanosecond), zeroAntonRoll); len(errs) != 0 {
		t.Fatal(errs)
	}
	if len(flow.Squad) != 0 || !flow.SquadShownAt.IsZero() {
		t.Fatal("phase0 squad stage opened before its actual grace deadline")
	}
	if _, errs := f.service.advanceCurrentAntonRewardFlows(flow.AutoSelectAt, zeroAntonRoll); len(errs) != 0 {
		t.Fatal(errs)
	}
	if len(flow.Party) != 2 || len(flow.Squad) != 0 || !flow.SquadShownAt.IsZero() || !flow.FinishedAt.IsZero() {
		t.Fatalf("phase0 automatic stage/capacity: party=%d squad=%d squad_shown=%t", len(flow.Party), len(flow.Squad), !flow.SquadShownAt.IsZero())
	}

	f.conn.write.Reset()
	if err := f.service.sendCurrentAntonRewardFlowProjection(session, cloneRuntimeRaidState(*f.state)); err != nil {
		t.Fatal(err)
	}
	var partyOwners, squadOwners []uint16
	for stream := f.conn.write.Bytes(); len(stream) > 0; {
		packet, rest := splitGameServerUpperPacket(t, stream)
		stream = rest
		if packet.Header.MsgID == 600 {
			switch packet.Body[0] {
			case 1:
				partyOwners = append(partyOwners, parsePhase0CardOwners(t, packet.Body)...)
			case 3:
				squadOwners = append(squadOwners, parsePhase0CardOwners(t, packet.Body)...)
			default:
				t.Fatalf("phase0 auto party stage emitted unexpected card kind %d: %x", packet.Body[0], packet.Body)
			}
		}
	}
	if len(partyOwners) != 2 || partyOwners[0] != 19 || partyOwners[1] != 20 || len(squadOwners) != 0 {
		t.Fatalf("phase0 auto party reveal owners=%v squad=%v", partyOwners, squadOwners)
	}
	if _, errs := f.service.advanceCurrentAntonRewardFlows(flow.AutoSelectAt.Add(20*time.Second-time.Nanosecond), zeroAntonRoll); len(errs) != 0 || len(flow.Squad) != 0 {
		t.Fatalf("phase0 squad opened before party display window: errs=%v", errs)
	}
	if _, errs := f.service.advanceCurrentAntonRewardFlows(flow.AutoSelectAt.Add(20*time.Second), zeroAntonRoll); len(errs) != 0 || len(flow.Squad) != 2 || flow.SquadShownAt.IsZero() || !flow.FinishedAt.IsZero() {
		t.Fatalf("phase0 squad stage at display boundary: errs=%v", errs)
	}
	f.conn.write.Reset()
	if err := f.service.sendCurrentAntonRewardFlowProjection(session, cloneRuntimeRaidState(*f.state)); err != nil {
		t.Fatal(err)
	}
	for stream := f.conn.write.Bytes(); len(stream) > 0; {
		packet, rest := splitGameServerUpperPacket(t, stream)
		stream = rest
		if packet.Header.MsgID == 600 {
			if packet.Body[0] != 3 {
				t.Fatalf("phase0 squad projection emitted non-squad card kind %d: %x", packet.Body[0], packet.Body)
			}
			squadOwners = append(squadOwners, parsePhase0CardOwners(t, packet.Body)...)
		}
	}
	if len(squadOwners) != 2 || squadOwners[0] != 19 || squadOwners[1] != 20 {
		t.Fatalf("phase0 squad capacity/owners=%v want [19 20]", squadOwners)
	}

	if _, errs := f.service.advanceCurrentAntonRewardFlows(flow.SquadShownAt.Add(20*time.Second), zeroAntonRoll); len(errs) != 0 || flow.FinishedAt.IsZero() || !flow.BreakUntil.Equal(flow.FinishedAt.Add(600*time.Second)) {
		t.Fatalf("phase0 squad display/break timing: finished=%t break=%v errs=%v", !flow.FinishedAt.IsZero(), flow.BreakUntil, errs)
	}
}

func TestRealPVFAntonPhase0Natural660SelectsOnlyFrozenPartyCard(t *testing.T) {
	f, session, _ := firstAntonFlowFixture(t)
	state := cloneRuntimeRaidState(*f.state)
	if err := f.service.sendCurrentAntonTerminalResult(session, state); err != nil {
		t.Fatal(err)
	}
	dispatchAntonCardRequest(t, f, session, 659, []byte{0})
	f.conn.write.Reset()
	dispatchAntonCardRequest(t, f, session, 660, []byte{1, 0})

	var reveals, acks int
	for stream := f.conn.write.Bytes(); len(stream) > 0; {
		packet, rest := splitGameServerUpperPacket(t, stream)
		stream = rest
		if packet.Header.MsgID == 660 && bytes.Equal(packet.Body, []byte{1}) {
			acks++
		}
		if packet.Header.MsgID == 600 {
			reveals++
			if packet.Body[0] != 1 {
				t.Fatalf("phase0 660 emitted non-party card kind %d: %x", packet.Body[0], packet.Body)
			}
			owners := parsePhase0CardOwners(t, packet.Body)
			if len(owners) != 1 || owners[0] != 19 {
				t.Fatalf("phase0 660 reveal owners=%v want [19]", owners)
			}
		}
	}
	if reveals != 1 || acks != 1 || antonGrantedItemCount(t, f, "19", 10304529) != 30 {
		t.Fatalf("phase0 660 reveal/ack/reward: reveals=%d acks=%d count=%d", reveals, acks, antonGrantedItemCount(t, f, "19", 10304529))
	}
	card := f.state.Battle.RewardCards[0][19]
	if card == nil || !card.Committed || card.Card != 0 || f.state.Battle.RewardFlow[0].Squad[19] != nil {
		t.Fatal("phase0 party selection leaked into the squad stage")
	}
}

func TestRealPVFAntonPhase0ManualPartyDoesNotAddSecondDisplayDelay(t *testing.T) {
	f, session, _ := firstAntonFlowFixture(t)
	peer := f.session()
	peer.selectedCharacterID, peer.accountID = 20, "account-20"
	peer.loginPacketSeen, peer.accountFromToken = true, true
	f.service.bindGameSessionCharacter(peer, 20)
	state := cloneRuntimeRaidState(*f.state)
	for _, owner := range []*gameSession{session, peer} {
		if err := f.service.sendCurrentAntonTerminalResult(owner, state); err != nil {
			t.Fatal(err)
		}
		dispatchAntonCardRequest(t, f, owner, 659, []byte{0})
	}
	flow := f.state.Battle.RewardFlow[0]
	flow.AutoSelectAt = time.Now().Add(time.Minute)
	dispatchAntonCardRequest(t, f, session, 660, []byte{1, 0})
	dispatchAntonCardRequest(t, f, peer, 660, []byte{1, 1})
	if len(flow.Party) != 2 || flow.Party[19] == nil || flow.Party[20] == nil || !flow.Party[19].Committed || !flow.Party[20].Committed || len(flow.Squad) != 0 {
		t.Fatalf("manual party selection ownership=%d committed=%v/%v squad=%d", len(flow.Party), flow.Party[19] != nil && flow.Party[19].Committed, flow.Party[20] != nil && flow.Party[20].Committed, len(flow.Squad))
	}
	if _, errs := f.service.advanceCurrentAntonRewardFlows(flow.AutoSelectAt, zeroAntonRoll); len(errs) != 0 || len(flow.Squad) != 2 || flow.SquadShownAt.IsZero() {
		t.Fatalf("manual party group gained an extra squad display delay: squad=%d errs=%v", len(flow.Squad), errs)
	}
	if antonGrantedItemCount(t, f, "19", 10304529) != 30 || antonGrantedItemCount(t, f, "20", 10304529) != 30 {
		t.Fatal("manual party selection reward changed")
	}
}

func TestRealPVFAntonPhase0ReconnectKeepsSelectedPartyWithoutRegrant(t *testing.T) {
	f, session, _ := firstAntonFlowFixture(t)
	state := cloneRuntimeRaidState(*f.state)
	if err := f.service.sendCurrentAntonTerminalResult(session, state); err != nil {
		t.Fatal(err)
	}
	dispatchAntonCardRequest(t, f, session, 659, []byte{0})
	dispatchAntonCardRequest(t, f, session, 660, []byte{1, 0})
	if got := antonGrantedItemCount(t, f, "19", 10304529); got != 30 {
		t.Fatalf("initial phase0 party reward=%d want=30", got)
	}

	f.conn = &bufferConn{}
	replacement := f.session()
	replacement.loginPacketSeen, replacement.accountFromToken = true, true
	f.service.bindGameSessionCharacter(replacement, 19)
	state = cloneRuntimeRaidState(*f.state)
	if err := f.service.sendCurrentAntonTerminalResult(replacement, state); err != nil {
		t.Fatal(err)
	}
	f.conn.write.Reset()
	if err := f.service.sendCurrentAntonRewardFlowProjection(replacement, state); err != nil {
		t.Fatal(err)
	}
	var restored bool
	var ids []uint16
	var cardKind byte
	for stream := f.conn.write.Bytes(); len(stream) > 0; {
		packet, rest := splitGameServerUpperPacket(t, stream)
		stream = rest
		ids = append(ids, packet.Header.MsgID)
		if packet.Header.MsgID == 600 {
			cardKind = packet.Body[0]
		}
		if packet.Header.MsgID == 600 && packet.Body[0] == 1 {
			for _, owner := range parsePhase0CardOwners(t, packet.Body) {
				restored = restored || owner == 19
			}
		}
	}
	if !restored || antonGrantedItemCount(t, f, "19", 10304529) != 30 {
		t.Fatalf("phase0 reconnect lost/regranted party card: restored=%t count=%d ids=%v kind=%d", restored, antonGrantedItemCount(t, f, "19", 10304529), ids, cardKind)
	}
	f.conn.write.Reset()
	dispatchAntonCardRequest(t, f, replacement, 659, []byte{0})
	packet, rest := splitGameServerUpperPacket(t, f.conn.write.Bytes())
	if packet.Header.MsgID != 659 || !bytes.Equal(packet.Body, []byte{1}) || len(rest) != 0 {
		t.Fatal("phase0 reconnect 659 replay reopened/changed reward screen")
	}
	if antonGrantedItemCount(t, f, "19", 10304529) != 30 || !f.state.Battle.RewardCards[0][19].Committed {
		t.Fatal("phase0 reconnect rerolled or duplicated selected party reward")
	}
}

func parsePhase0CardOwners(t *testing.T, body []byte) []uint16 {
	t.Helper()
	if len(body) < 2 {
		t.Fatalf("card list header=%x", body)
	}
	pos, count := 2, int(body[1])
	owners := make([]uint16, 0, count)
	for i := 0; i < count; i++ {
		if pos+4 > len(body) {
			t.Fatalf("truncated card row %d: %x", i, body)
		}
		owners = append(owners, binary.LittleEndian.Uint16(body[pos:pos+2]))
		items := int(body[pos+3])
		pos += 4 + items*8
		if pos > len(body) {
			t.Fatalf("truncated card items row %d: %x", i, body)
		}
	}
	if pos != len(body) {
		t.Fatalf("card list trailing bytes: parsed=%d body=%x", pos, body)
	}
	return owners
}
