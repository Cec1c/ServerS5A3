package dnfbridge

import (
	"encoding/binary"
	"fmt"
	"reflect"
	"strconv"
	"testing"
	"time"
)

type r195FiendRewardDescriptor struct {
	kind, objectKind byte
	owner            uint16
	id               uint32
	quantity         uint16
	flags            byte
}

type r195FiendRewardWire struct {
	sequence []string
	owners   map[byte][]uint16
	items    map[r195FiendRewardDescriptor]int
}

// Decode actual nested NOTI600 bytes independently of the production builder.
// Kind and owner are part of the key: an identical team item cannot satisfy a
// missing party reward, and another player's descriptor cannot satisfy ours.
func r195ReadFiendRewardWire(t *testing.T, stream []byte) r195FiendRewardWire {
	t.Helper()
	got := r195FiendRewardWire{owners: map[byte][]uint16{}, items: map[r195FiendRewardDescriptor]int{}}
	for len(stream) > 0 {
		packet, rest := splitGameServerUpperPacket(t, stream)
		stream = rest
		if packet.Header.Classification != 0 {
			continue
		}
		if packet.Header.MsgID == 587 {
			if len(packet.Body) != 2 || packet.Body[1] != 0 {
				t.Fatalf("invalid reward state: %x", packet.Body)
			}
			got.sequence = append(got.sequence, fmt.Sprintf("state%d", packet.Body[0]))
			continue
		}
		if packet.Header.MsgID != 600 {
			continue
		}
		body := packet.Body
		if len(body) < 2 || body[0] > 3 {
			t.Fatalf("invalid reward list: %x", body)
		}
		kind, offset := body[0], 2
		got.sequence = append(got.sequence, fmt.Sprintf("kind%d", kind))
		for c := 0; c < int(body[1]); c++ {
			if offset+4 > len(body) {
				t.Fatal("truncated reward card")
			}
			owner := binary.LittleEndian.Uint16(body[offset:])
			if owner == 0 {
				t.Fatal("reward card has no owner")
			}
			got.owners[kind] = append(got.owners[kind], owner)
			count := int(body[offset+3])
			offset += 4
			for n := 0; n < count; n++ {
				if offset+8 > len(body) {
					t.Fatal("truncated reward descriptor")
				}
				item := r195FiendRewardDescriptor{
					kind: kind, owner: owner, objectKind: body[offset],
					id:       binary.LittleEndian.Uint32(body[offset+1:]),
					quantity: binary.LittleEndian.Uint16(body[offset+5:]), flags: body[offset+7],
				}
				got.items[item]++
				offset += 8
			}
		}
		if offset != len(body) {
			t.Fatalf("reward list trailing bytes: %x", body[offset:])
		}
	}
	return got
}

func r195WantFiendRewardWire(t *testing.T, got r195FiendRewardWire, flow *currentAntonRewardFlow, owners map[byte][]uint16, sequence ...string) {
	t.Helper()
	if !reflect.DeepEqual(got.sequence, sequence) || !reflect.DeepEqual(got.owners, owners) {
		t.Fatalf("reward routing: sequence=%v owners=%v; want sequence=%v owners=%v", got.sequence, got.owners, sequence, owners)
	}
	want := map[r195FiendRewardDescriptor]int{}
	for kind, ids := range owners {
		cards := flow.Party
		if kind == 3 {
			cards = flow.Squad
		} else if kind != 1 {
			t.Fatalf("unexpected committed reward kind %d", kind)
		}
		for _, id := range ids {
			card := cards[id]
			if card == nil || !card.Committed || len(card.Plan.Rewards) == 0 {
				t.Fatalf("fixture lacks committed nonempty kind%d owner%d reward", kind, id)
			}
			for _, reward := range card.Plan.Rewards {
				if reward.Count == 0 || reward.Count > 65535 {
					t.Fatalf("unrepresentable committed quantity %d", reward.Count)
				}
				want[r195FiendRewardDescriptor{kind: kind, owner: id, objectKind: 0,
					id: reward.Definition.ItemID, quantity: uint16(reward.Count), flags: card.Plan.Choice.Flags}]++
			}
		}
	}
	if !reflect.DeepEqual(got.items, want) {
		t.Fatalf("committed reward descriptors missing, duplicated or misrouted: got=%+v want=%+v", got.items, want)
	}
}

// Preserve the original red-test name. This follows both clients through
// movie completion and the same automatic tick that commits party and team.
func TestR195RealFiendWarAutomaticRewardDisplayIncludesPartyGrant(t *testing.T) {
	r195CheckFiendRewardDisplay(t, false)
}

// The real 660 handler must share its successful kind1 signature with the
// automatic projector. No test writes projection signatures or grant flags.
func TestR195RealFiendWarManualPartyDisplayOnlyAppendsTeam(t *testing.T) {
	r195CheckFiendRewardDisplay(t, true)
}

func r195CheckFiendRewardDisplay(t *testing.T, manualParty bool) {
	t.Helper()
	f, peers := actualCurrentFiendWar(t, 2)
	b := f.state.Battle
	f.state.Started = true
	b.EnterAfter = time.Now().Add(-time.Minute)
	b.Deadline = time.Now().Add(time.Hour)
	b.PhaseCompleted, b.FiendWar.Complete = true, true
	b.PhaseCompletedAt = time.Now()
	b.ClearedRuns = map[string]currentRaidClearedRun{}
	for i := uint32(0); i < b.AntonRewards.Phases[0].MinimumClears; i++ {
		b.ClearedRuns[strconv.Itoa(int(i))] = currentRaidClearedRun{DungeonID: 4978, Attempt: 1, Participants: []uint16{19, 20}}
	}
	if err := f.service.freezeCurrentFiendWarRewardFlowLocked(*f.state); err != nil {
		t.Fatal(err)
	}
	flow := b.RewardFlow[0]
	if flow == nil || len(flow.Recipients) != 2 || flow.Recipients[0].Group == flow.Recipients[1].Group {
		t.Fatal("fixture must have two independently grouped reward owners")
	}
	if _, errs := f.service.commitCurrentRaidWeeklyIntents(b.PhaseCompletedAt); len(errs) != 0 {
		t.Fatal(errs)
	}
	for i, peer := range peers {
		conn := peer.conn.(*bufferConn)
		if err := f.service.sendCurrentAntonTerminalResult(peer, cloneRuntimeRaidState(*f.state)); err != nil {
			t.Fatal(err)
		}
		if !currentAntonResultDelivered(peer, *f.state) {
			t.Fatal("result delivery prerequisite not established")
		}
		conn.write.Reset()
		dispatchAntonCardRequest(t, f, peer, 659, []byte{1})
		masks := r195ReadFiendRewardWire(t, conn.write.Bytes())
		wantOwners := map[byte][]uint16{1: {peer.selectedCharacterID}, 0: {peer.selectedCharacterID}}
		if !reflect.DeepEqual(masks.sequence, []string{"state4", "kind1", "kind0"}) || !reflect.DeepEqual(masks.owners, wantOwners) || len(masks.items) != 0 {
			t.Fatalf("uncommitted movie masks leaked items/another group: %+v", masks)
		}
		if manualParty && i == 0 {
			// Owner20 has not received 601 yet, so the real 660 handler cannot
			// enqueue an asynchronous reveal to that client. Each group is solo.
			conn.write.Reset()
			dispatchAntonCardRequest(t, f, peer, 660, []byte{1, 0})
			r195WantFiendRewardWire(t, r195ReadFiendRewardWire(t, conn.write.Bytes()), flow,
				map[byte][]uint16{1: {peer.selectedCharacterID}}, "kind1")
			if len(flow.Squad) != 0 || flow.Party[20] != nil {
				t.Fatal("manual selection unexpectedly committed another reward group")
			}
		}
		conn.write.Reset()
	}
	rolls := 0
	if _, errs := f.service.advanceCurrentAntonRewardFlows(flow.AutoSelectAt, func(n int64) (int64, error) {
		rolls++
		return zeroAntonRoll(n)
	}); len(errs) != 0 {
		t.Fatal(errs)
	}
	if rolls == 0 || flow.SquadShownAt.IsZero() {
		t.Fatal("fixture did not exercise automatic party/team commitment")
	}
	frozen := cloneCurrentAntonRewardFlow(flow)
	assets := map[uint16][]any{}
	for _, peer := range peers {
		// Warm the normal container representation before checking that
		// projection/reconnection leaves semantic assets and receipts intact.
		if err := f.service.sendSelectedCurrentContainerListsRefresh(peer, "r195_fiend_reward_display_fixture"); err != nil {
			t.Fatal(err)
		}
		assets[peer.selectedCharacterID] = r195PublicRewardAssetSnapshot(t, f.service, peer)
	}
	project := func(peer *gameSession) r195FiendRewardWire {
		t.Helper()
		conn := peer.conn.(*bufferConn)
		conn.write.Reset()
		if err := f.service.sendCurrentAntonRewardFlowProjection(peer, cloneRuntimeRaidState(*f.state)); err != nil {
			t.Fatal(err)
		}
		return r195ReadFiendRewardWire(t, conn.write.Bytes())
	}
	assertUnchanged := func() {
		t.Helper()
		for _, peer := range peers {
			if after := r195PublicRewardAssetSnapshot(t, f.service, peer); !reflect.DeepEqual(assets[peer.selectedCharacterID], after) {
				t.Fatalf("display/replay changed owner%d persisted assets or receipts", peer.selectedCharacterID)
			}
		}
		if !reflect.DeepEqual(frozen.Party, flow.Party) || !reflect.DeepEqual(frozen.Squad, flow.Squad) {
			t.Fatal("display/replay changed frozen plans, cards or committed state")
		}
	}
	for i, peer := range peers {
		wantOwners := map[byte][]uint16{1: {peer.selectedCharacterID}, 3: {19, 20}}
		sequence := []string{"kind1", "kind3"}
		if manualParty && i == 0 {
			delete(wantOwners, 1)
			sequence = []string{"kind3"}
		}
		r195WantFiendRewardWire(t, project(peer), flow, wantOwners, sequence...)
		project(peer)
		if peer.conn.(*bufferConn).write.Len() != 0 {
			t.Fatalf("owner%d duplicate projection sent packets, including container refresh", peer.selectedCharacterID)
		}
	}
	assertUnchanged()
	// Re-enter the real automatic reducer with a roll trap. Neither projection
	// nor a repeat tick may draw again or grant another copy of either reward.
	if _, errs := f.service.advanceCurrentAntonRewardFlows(flow.AutoSelectAt, func(int64) (int64, error) {
		t.Fatal("committed rewards were drawn again")
		return 0, nil
	}); len(errs) != 0 {
		t.Fatal(errs)
	}
	assertUnchanged()
	for _, peer := range peers {
		project(peer)
		if peer.conn.(*bufferConn).write.Len() != 0 {
			t.Fatal("repeat automatic tick replayed an unchanged display")
		}
	}
	for _, peer := range peers {
		conn := &bufferConn{}
		replacement := f.session()
		replacement.conn, replacement.channel, replacement.accountID = conn, peer.channel, peer.accountID
		replacement.loginPacketSeen, replacement.accountFromToken = true, true
		f.service.bindGameSessionCharacter(replacement, peer.selectedCharacterID)
		project(replacement)
		if conn.write.Len() != 0 {
			t.Fatal("fresh connection displayed rewards before its owned result")
		}
		if err := f.service.sendCurrentAntonTerminalResult(replacement, cloneRuntimeRaidState(*f.state)); err != nil {
			t.Fatal(err)
		}
		if !currentAntonResultDelivered(replacement, *f.state) {
			t.Fatal("fresh connection did not receive its owned result")
		}
		r195WantFiendRewardWire(t, project(replacement), flow,
			map[byte][]uint16{1: {peer.selectedCharacterID}, 3: {19, 20}}, "state4", "kind1", "kind3")
		project(replacement)
		if conn.write.Len() != 0 {
			t.Fatal("fresh connection repeated its restored reward projection")
		}
		assertUnchanged()
	}
}
