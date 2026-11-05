package dnfbridge

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/raid"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

// Connect actual invitation acceptance to raid entry ownership for each party
// size. Solo members' former seats must not make the third invitation leave
// the ordinary party and raid projection inconsistent.
func TestStartedRaidFreePartySequentialJoinsFreezeAllMembers(t *testing.T) {
	for _, wanted := range []int{2, 3, 4} {
		t.Run(fmt.Sprint(wanted), func(t *testing.T) {
			f := newInviteAcceptDungeonFixture(t)
			state := installPartyRaidStateForTest(t, f, 0, []runtimeRaidMemberState{
				{CharID: 101, GroupIndex: 3, OrdinaryPartySolo: true},
				{CharID: 102, GroupIndex: 4, OrdinaryPartySolo: true},
				{CharID: 200, GroupIndex: 1, SlotOrder: 1, OrdinaryPartySolo: true},
				{CharID: 201, GroupIndex: 1, SlotOrder: 2, OrdinaryPartySolo: true},
			})
			state.Started = true
			state.Battle = &runtimeRaidBattle{
				InstanceID: "free-party-multiplayer-entry", Mode: 1,
				ChannelID: f.channel.ID, ChannelType: f.channel.Type,
				EnterAfter: time.Now().Add(-time.Second), Deadline: time.Now().Add(time.Hour),
				Dungeons:       []raid.DungeonState{{ID: 210, State: 0}, {ID: 700, State: 0}},
				MemberAccounts: map[uint16]string{99: "account-1", 100: "account-1", 101: "account-1", 102: "account-1"},
				MemberGroups:   map[uint16]byte{99: 1, 100: 2, 101: 3, 102: 4, 200: 1, 201: 1},
			}
			frozen := cloneRuntimeRaidBattle(state.Battle)
			repos, _ := f.service.repositoryProvider()
			owners := []*gameSession{f.leader, f.follower}
			for id := uint16(101); id <= 102; id++ {
				key := fmt.Sprint(id)
				if err := repos.Character.Save(context.Background(), dnfrepo.CharacterRecord{CharacterID: key, AccountID: "account-1", Level: 20, Stats: map[string]int64{"fatigue": 100, "town_id": 38, "area_id": 1, "pos_x": 450, "pos_y": 234, "direction": 5, "area_state": 3}}); err != nil {
					t.Fatal(err)
				}
				if err := repos.Inventory.Save(context.Background(), dnfrepo.InventoryRecord{CharacterID: key, Slots: map[string]dnfrepo.ItemStack{}}); err != nil {
					t.Fatal(err)
				}
				peer := &gameSession{conn: &bufferConn{}, connID: "free-party-" + key, channel: f.channel, residentChannel: f.channel}
				f.service.bindGameSessionCharacter(peer, id)
				f.service.onlinePlayers.EnterArea(&onlinePlayerInfo{CharacterID: id, TownID: 38, AreaID: 1, Session: peer})
				bindDungeonSelectorOriginForTestAt(t, f.service, peer, 38, 1, 450, 234)
				owners = append(owners, peer)
			}
			for _, owner := range owners {
				owner.loginPacketSeen, owner.accountFromToken, owner.accountID = true, true, "account-1"
			}
			for count := 2; count <= wanted; count++ {
				peer := owners[count-1]
				ok, reason, _ := f.service.forceInviteParty(99, peer.selectedCharacterID, 0, [3]uint32{})
				if !ok {
					t.Fatalf("size %d invitation: %s", count, reason)
				}
				if err := f.service.handleOnlineResponsePeer(peer, uint16(dnfenum.CmdPacketResponsePeer), buildResponsePeerBody(99, 0, 0)); err != nil {
					t.Fatal(err)
				}
				party := f.service.runtimePartyStateForSession(f.leader)
				if len(runtimePartyMembers(party)) != count {
					t.Fatalf("size %d ordinary party not accepted", count)
				}
				anchor, _ := runtimeRaidMemberForTest(*state, 99)
				var targets []*runtimePartyPreparedDungeonEntry
				var ids []uint16
				for slot, owner := range owners[:count] {
					id := owner.selectedCharacterID
					row, found := runtimeRaidMemberForTest(*state, id)
					if !found || row.GroupIndex != anchor.GroupIndex || row.SlotOrder != byte(slot) || row.OrdinaryPartySolo {
						t.Fatalf("size %d accepted member %d remained outside live raid party: %+v", count, id, row)
					}
					ids = append(ids, id)
					targets = append(targets, &runtimePartyPreparedDungeonEntry{session: owner, runtime: &runtimeDungeonState{Dungeon: worldmap.Dungeon{ID: 210}, AccountID: "account-1", Character: dnfrepo.CharacterRecord{CharacterID: fmt.Sprint(id)}}})
				}
				if err := f.service.prepareCurrentRaidDungeonEntry(f.leader, targets[0].runtime, nil, targets[1:]); err != nil {
					t.Fatalf("size %d raid entry rejected: %v", count, err)
				}
				for i, target := range targets {
					if entry := target.runtime.raidEntry; entry == nil || !sameRaidParticipants(entry.Participants, ids) || entry.RunID != targets[0].runtime.raidEntry.RunID {
						t.Fatalf("size %d incomplete shared entry", count)
					}
					if i > 0 {
						if err := f.service.prepareCurrentRaidDungeonEntry(target.session, target.runtime, target, nil); err != nil {
							t.Fatalf("size %d follower pass: %v", count, err)
						}
					}
				}
				// A later member failing ownership still rejects everyone before binding.
				bad := make([]*runtimePartyPreparedDungeonEntry, len(targets))
				for i, target := range targets {
					r := *target.runtime
					r.raidEntry = nil
					bad[i] = &runtimePartyPreparedDungeonEntry{session: target.session, runtime: &r}
				}
				bad[len(bad)-1].runtime.AccountID = "different-account"
				if err := f.service.prepareCurrentRaidDungeonEntry(f.leader, bad[0].runtime, nil, bad[1:]); err == nil {
					t.Fatal("foreign late member admitted")
				}
				for _, target := range bad {
					if target.runtime.raidEntry != nil {
						t.Fatal("failed group partly bound")
					}
				}
				if !reflect.DeepEqual(state.Battle.MemberGroups, frozen.MemberGroups) {
					t.Fatal("reward-frozen groups changed")
				}
				for _, id := range []uint16{200, 201} {
					row, _ := runtimeRaidMemberForTest(*state, id)
					if !row.OrdinaryPartySolo || row.GroupIndex != 1 {
						t.Fatal("unrelated solo member moved")
					}
				}
			}

			f.selectDungeon(t, f.leader, 700)
			for _, owner := range owners[:wanted] {
				r := owner.dungeon.runtime
				if r == nil || r.Dungeon.ID != 700 || r.raidEntry == nil || len(r.raidEntry.Participants) != wanted {
					logPath := f.service.packetLog.file.Name()
					_ = f.service.packetLog.Close()
					raw, _ := os.ReadFile(logPath)
					t.Fatalf("size %d actual selection did not commit every party member: %s", wanted, raw)
				}
			}
		})
	}
}

// The legacy planner remains for non-current layouts. Use that unchanged
// algorithm as a control: two former solo seats allow the second member but
// block the third. Current-layout free parties must ignore those old seats.
func TestStartedRaidFreePartyHistoricalSeatControl(t *testing.T) {
	for _, current := range []bool{false, true} {
		t.Run(map[bool]string{false: "historical_seat_control", true: "current_free_party"}[current], func(t *testing.T) {
			state := runtimeRaidState{Started: true, CurrentLayout: current, Members: []runtimeRaidMemberState{
				{CharID: 1, GroupIndex: 1}, {CharID: 2, GroupIndex: 2}, {CharID: 3, GroupIndex: 3}, {CharID: 4, GroupIndex: 4},
				{CharID: 5, GroupIndex: 1, SlotOrder: 1, OrdinaryPartySolo: true},
				{CharID: 6, GroupIndex: 1, SlotOrder: 2, OrdinaryPartySolo: true},
			}}
			party := alignedcmd.PartyState{PartyID: 7, UserID: 1, Members: []alignedcmd.PartyMemberState{{UserID: 1}}}
			for count := 2; count <= 4; count++ {
				party.Members = append(party.Members, alignedcmd.PartyMemberState{UserID: uint16(count), Slot: byte(count - 1)})
				candidate, changed := planRuntimeRaidGroupSynchronization(state, party)
				if current || count == 2 {
					if !changed {
						t.Fatalf("party size %d could not join raid group", count)
					}
					state.Members = candidate
					for _, member := range party.Members {
						row, _ := runtimeRaidMemberForTest(state, member.UserID)
						if row.GroupIndex != 1 {
							t.Fatal("party not in shared raid group")
						}
					}
				} else {
					if changed || candidate != nil {
						t.Fatal("historical control no longer reproduces stale-seat refusal")
					}
					row, _ := runtimeRaidMemberForTest(state, uint16(count))
					if row.GroupIndex == 1 {
						t.Fatal("control unexpectedly admitted extra member")
					}
				}
			}
		})
	}
}
