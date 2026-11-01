package dnfbridge

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	repo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// Diagnostic replay of a started Anton squad-2 member leaving, joining squad 1,
// and entering together. Persistence and sessions are engineering fixtures;
// exact PVF data and the production request handlers own the transition.
func TestR195AntonRegroupWireProbe(t *testing.T) {
	t.Setenv("DNFBRIDGE_PARTY_DUNGEON_ALL_MEMBER_USER_STATE", "all")
	catalog, _ := actualAntonRewardFixture(t)
	f, owners, conns, _ := deadlineAuditV10AdmitConfigured(t, catalog.source.(*platformpvf.Archive), false, 4,
		func(i int, _ uint16, c repo.CharacterRecord) repo.CharacterRecord {
			c.Name, c.Job = fmt.Sprintf("regroup-%d", i), "0"
			c.Stats["grow_type"] = 0x11
			return c
		})
	s := f.service
	// This engineering fixture constructs Service directly; it does not call
	// optionsFromConfig. Set the observed live option explicitly as well.
	s.options.partyDungeonUserStateAll = true
	leader, mover := owners[0], owners[2]
	if f.state.Members[2].GroupIndex != 2 || f.state.Battle.MemberGroups[mover.selectedCharacterID] != 2 {
		t.Fatal("probe does not start with a real squad-2 battle member")
	}
	for _, conn := range conns {
		conn.reset()
	}
	deadlineAuditV10Event(t, s, mover, func() error {
		return s.handleOnlineLeaveParty(mover, uint16(dnfenum.CmdPacketLeaveParty))
	})
	if state := s.runtimePartyStateForSession(mover); len(runtimePartyMembers(state)) > 1 {
		t.Fatal("leaver still belongs to a multi-member ordinary party")
	}
	deadlineAuditV10Event(t, s, leader, func() error {
		ok, reason, _ := s.forceInviteParty(leader.selectedCharacterID, mover.selectedCharacterID, 0, [3]uint32{})
		if !ok {
			return fmt.Errorf("invite: %s", reason)
		}
		return nil
	})
	deadlineAuditV10Event(t, s, mover, func() error {
		return s.handleOnlineResponsePeer(mover, uint16(dnfenum.CmdPacketResponsePeer), buildResponsePeerBody(leader.selectedCharacterID, 0, 0))
	})
	state := s.runtimePartyStateForSession(leader)
	if len(runtimePartyMembers(state)) != 3 || f.state.Members[2].GroupIndex != 1 || f.state.Battle.MemberGroups[mover.selectedCharacterID] != 2 {
		t.Fatalf("regroup ownership mismatch: party=%+v raid=%+v frozen=%v", state, f.state.Members, f.state.Battle.MemberGroups)
	}
	ids := r160EnterRaidGroup(t, f, owners, 0, 210)
	if fmt.Sprint(ids) != "[19 20 21]" {
		t.Fatalf("new entry participants: %v", ids)
	}
	r160WaitRaidProjection(t, f, owners)
	// Drain each member's own event loop before collecting the wire.
	for _, p := range owners {
		deadlineAuditV10Event(t, s, p, func() error { return nil })
	}
	type row struct {
		Index  int
		Class  byte
		Opcode uint16
		Body   []byte
	}
	all := make(map[string]map[uint16][]row)
	capture := func(phase string) {
		all[phase] = make(map[uint16][]row)
		for i, receiver := range owners[:3] {
			frames := r160FindFrames(t, conns[i].bytes())
			for n, frame := range frames {
				all[phase][receiver.selectedCharacterID] = append(all[phase][receiver.selectedCharacterID], row{n, frame.Header.Classification, frame.Header.MsgID, frame.Body})
				if mode, key, ok := r160ActorKey(t, frame); ok {
					t.Logf("phase=%s receiver=%d index=%d op2 mode=%d key=%d", phase, receiver.selectedCharacterID, n, mode, key)
				} else if frame.Header.MsgID == 3 && len(frame.Body) > 0 {
					t.Logf("phase=%s receiver=%d index=%d op3 body=%x", phase, receiver.selectedCharacterID, n, frame.Body)
				} else if frame.Header.MsgID == 6 && len(frame.Body) >= 2 {
					t.Logf("receiver=%d index=%d op6 key=%d", receiver.selectedCharacterID, n, binary.LittleEndian.Uint16(frame.Body[len(frame.Body)-2:]))
				} else if frame.Header.MsgID == 27 || frame.Header.MsgID == 29 || frame.Header.MsgID == 37 {
					t.Logf("receiver=%d index=%d opcode=%d", receiver.selectedCharacterID, n, frame.Header.MsgID)
				}
			}
			key, ready := s.currentDungeonPartyRunKey(receiver)
			if !ready || key.RaidRunID == "" {
				t.Fatalf("receiver %d missing committed run", receiver.selectedCharacterID)
			}
		}
	}
	capture("squad2_leave_join_squad1")
	firstRun, _ := s.currentDungeonPartyRunKey(leader)
	returnStarts := make([]int, len(conns))
	for i, conn := range conns {
		returnStarts[i] = len(conn.bytes())
	}
	// Compare the user's recovery route using the same sockets and characters.
	for _, i := range []int{2, 1, 0} {
		p := owners[i]
		deadlineAuditV10Event(t, s, p, func() error { return s.handleDungeonGiveupGame(p, nil) })
		deadlineAuditV10Event(t, s, p, func() error { return s.sendEnterSelectDungeonState(p, "upper_15", false, true) })
		if p.dungeon.runtime != nil {
			t.Fatalf("confirmed return retained runtime: %d", p.selectedCharacterID)
		}
		deadlineAuditV10Event(t, s, p, func() error { return s.handleOnlineLeaveParty(p, uint16(dnfenum.CmdPacketLeaveParty)) })
	}
	// Preserve the actual town recovery traffic, not only the next entry.
	all["town_return_transition"] = make(map[uint16][]row)
	for i, receiver := range owners[:3] {
		deadlineAuditV10Event(t, s, receiver, func() error { return nil })
		for n, frame := range r160FindFrames(t, conns[i].bytes()[returnStarts[i]:]) {
			all["town_return_transition"][receiver.selectedCharacterID] = append(all["town_return_transition"][receiver.selectedCharacterID], row{n, frame.Header.Classification, frame.Header.MsgID, frame.Body})
		}
	}
	for _, conn := range conns {
		conn.reset()
	}
	for _, peer := range owners[1:3] {
		deadlineAuditV10Event(t, s, leader, func() error {
			ok, reason, _ := s.forceInviteParty(leader.selectedCharacterID, peer.selectedCharacterID, 0, [3]uint32{})
			if !ok {
				return fmt.Errorf("return regroup invite: %s", reason)
			}
			return nil
		})
		deadlineAuditV10Event(t, s, peer, func() error {
			return s.handleOnlineResponsePeer(peer, uint16(dnfenum.CmdPacketResponsePeer), buildResponsePeerBody(leader.selectedCharacterID, 0, 0))
		})
	}
	ids = r160EnterRaidGroup(t, f, owners, 0, 210)
	if fmt.Sprint(ids) != "[19 20 21]" {
		t.Fatalf("recovery participants: %v", ids)
	}
	r160WaitRaidProjection(t, f, owners)
	for _, p := range owners {
		deadlineAuditV10Event(t, s, p, func() error { return nil })
	}
	nextRun, ready := s.currentDungeonPartyRunKey(leader)
	if !ready || nextRun.RaidRunID == firstRun.RaidRunID {
		t.Fatal("return/regroup did not create a new run")
	}
	capture("town_return_regroup_reenter")
	if out := os.Getenv("R195_REGROUP_WIRE"); out != "" {
		raw, err := json.MarshalIndent(all, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err = os.MkdirAll(filepath.Dir(out), 0700); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(out, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, phase := range []string{"squad2_leave_join_squad1", "town_return_regroup_reenter"} {
		for receiver, frames := range all[phase] {
			entered := false
			peers := map[uint16]int{}
			for _, frame := range frames {
				if frame.Opcode == 27 && frame.Class == 0 {
					entered = true
				}
				b := frame.Body
				if !entered || frame.Opcode != 2 || frame.Class != 0 || len(b) < 88 || b[0] != 0 {
					continue
				}
				actor := binary.LittleEndian.Uint16(b[76:78])
				if actor == currentSceneActorObjectKey(receiver) {
					continue
				}
				stateAt := uint64(82) + uint64(binary.LittleEndian.Uint32(b[78:82])) + 5
				if stateAt >= uint64(len(b)) {
					t.Fatal("late peer mode0 has a truncated state")
				}
				if b[stateAt] != 1 {
					t.Errorf("phase=%s receiver=%d actor=%d frame=%d: late mode0 overwrites dungeon state with %d", phase, receiver, actor, frame.Index, b[stateAt])
				}
				peers[actor]++
			}
			if len(peers) != 2 {
				t.Errorf("phase=%s receiver=%d: expected both peers after entry, got %v", phase, receiver, peers)
			}
		}
	}
}
