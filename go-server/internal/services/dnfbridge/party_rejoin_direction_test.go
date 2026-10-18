package dnfbridge

import (
	"encoding/binary"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
)

// Exercise the actual request/response pair: clicking Join emits the same
// mode-0 request as Invite, but the recipient already owns the target lobby.
func TestPartyJoinRequestKeepsExistingLeaderAndSlots(t *testing.T) {
	for _, retained := range []bool{false, true} {
		t.Run(map[bool]string{false: "explicit_solo_lobby", true: "member_left_first"}[retained], func(t *testing.T) {
			f := newInviteAcceptDungeonFixture(t)
			f.service.options.currentClientVersion = "official-24.9"
			manager := f.service.runtimePartyManagerForService()
			identity, _ := f.service.boundGameSessionCharacterSnapshot(f.leader)
			member, _ := f.service.runtimePartyMemberForIdentity(identity)
			created := manager.Create(member, alignedcmd.PartyState{MaxMembers: 4})
			if !created.OK {
				t.Fatal(created.Reason)
			}
			f.service.publishRuntimePartySnapshot(created.Party)
			if retained {
				if _, _, ok := f.service.createOrJoinManagedRuntimeParty(f.follower, f.leader); !ok {
					t.Fatal("initial join")
				}
				id, _ := f.service.boundGameSessionCharacterSnapshot(f.follower)
				if _, _, ok := f.service.leaveManagedRuntimeParty(id, "test_member_left"); !ok {
					t.Fatal("leave")
				}
			}
			request := make([]byte, 13)
			binary.LittleEndian.PutUint16(request, inviteAcceptLeaderCharacterID)
			if err := f.service.handleOnlineRequestPeer(f.follower, uint16(dnfenum.CmdPacketRequestPeer), request); err != nil {
				t.Fatal(err)
			}
			if err := f.service.handleOnlineResponsePeer(f.leader, uint16(dnfenum.CmdPacketResponsePeer), buildResponsePeerBody(inviteAcceptFollowerCharacterID, 0, 0)); err != nil {
				t.Fatal(err)
			}
			got, found := manager.SnapshotByUser(inviteAcceptLeaderCharacterID, 0)
			if !found || got.ID != created.Party.ID || got.Leader != inviteAcceptLeaderCharacterID || len(got.Members) != 2 {
				t.Fatalf("join reversed or replaced existing lobby: found=%t snapshot=%+v", found, got)
			}
			for _, m := range got.Members {
				want := byte(0)
				if m.UserID == inviteAcceptFollowerCharacterID {
					want = 1
				}
				if m.Slot != want {
					t.Fatalf("member %d slot=%d want=%d", m.UserID, m.Slot, want)
				}
			}
		})
	}
}

func TestPartyMemberGiveupKeepsLeaderProjectionAndClearsLeaverRecords(t *testing.T) {
	for _, drifted := range []bool{false, true} {
		t.Run(map[bool]string{false: "current_generation", true: "generation_drift"}[drifted], func(t *testing.T) {
			f := newLeaderExitFixture(t)
			f.service.options.currentClientVersion = "official-24.9"
			f.leader.townActorOwnerChannel = 10
			f.member.townActorOwnerChannel = 10
			before, _ := f.service.runtimePartyManagerForService().SnapshotByUser(99, 0)
			round69AlignInstancePartyID(t, f, before.ID)
			if drifted {
				f.service.mu.Lock()
				f.member.characterGeneration += 100
				f.service.mu.Unlock()
			}
			// Town commit has removed this receiver's old runtime already.
			f.member.dungeon.runtime = nil
			if err := f.service.leaveRuntimePartyOnMemberGiveup(f.member, f.instance); err != nil {
				t.Fatal(err)
			}
			state := runtimePartyStateSnapshot(f.leader)
			if state.PartyID != int(before.ID) || state.UserID != 99 || !state.IsLeader || len(runtimePartyMembers(state)) != 1 {
				t.Fatalf("remaining leader projection was erased: %+v", state)
			}
			if f.leader.dungeon.runtime != f.leaderRuntime || f.leaderRuntime.partyInstance != f.instance {
				t.Fatal("survivor instance changed")
			}
			if _, ok := f.service.runtimePartyManagerForService().SnapshotByUser(100, 0); ok {
				t.Fatal("leaver still managed")
			}
			seen := map[uint16]int{}
			for _, frame := range splitUpperFrames(t, f.memberConn.write.Bytes()) {
				if frame.msgID == 9 && len(frame.body) == 10 && frame.body[7] == 0 && frame.body[9] == 3 {
					seen[binary.LittleEndian.Uint16(frame.body[4:6])]++
				}
			}
			for _, key := range []uint16{99, 100} {
				if seen[key] != 1 {
					t.Errorf("leaver old context record %d cleared %d times", key, seen[key])
				}
			}
			for _, frame := range splitUpperFrames(t, f.leaderConn.write.Bytes()) {
				if frame.msgID == 2 || frame.msgID == 11 {
					t.Fatal("leave unexpectedly redrew appearance or restarted peer handshake")
				}
				if frame.msgID == 9 && len(frame.body) == 10 && binary.LittleEndian.Uint16(frame.body[4:6]) == 99 {
					t.Fatal("remaining leader record removed")
				}
			}
		})
	}
}

func TestPartyInviteDirectionSurvivesRecipientLocalLobby(t *testing.T) {
	f := newInviteAcceptDungeonFixture(t)
	f.service.options.currentClientVersion = "official-24.9"
	manager := f.service.runtimePartyManagerForService()
	request := make([]byte, 13)
	binary.LittleEndian.PutUint16(request, inviteAcceptFollowerCharacterID)
	if err := f.service.handleOnlineRequestPeer(f.leader, uint16(dnfenum.CmdPacketRequestPeer), request); err != nil {
		t.Fatal(err)
	}
	// This local op12 lobby appears after the invitation was delivered. It
	// must not change which player originally owned the invitation.
	id, _ := f.service.boundGameSessionCharacterSnapshot(f.follower)
	member, _ := f.service.runtimePartyMemberForIdentity(id)
	created := manager.Create(member, alignedcmd.PartyState{MaxMembers: 4})
	if !created.OK {
		t.Fatal(created.Reason)
	}
	f.service.publishRuntimePartySnapshot(created.Party)
	if err := f.service.handleOnlineResponsePeer(f.follower, uint16(dnfenum.CmdPacketResponsePeer), buildResponsePeerBody(inviteAcceptLeaderCharacterID, 0, 0)); err != nil {
		t.Fatal(err)
	}
	got, ok := manager.SnapshotByUser(inviteAcceptLeaderCharacterID, 0)
	if !ok || got.Leader != inviteAcceptLeaderCharacterID || len(got.Members) != 2 {
		t.Fatalf("local lobby reversed invite: %+v", got)
	}
}

func TestPartyJoinRequestCannotRecreateRetiredLobby(t *testing.T) {
	f := newInviteAcceptDungeonFixture(t)
	f.service.options.currentClientVersion = "official-24.9"
	manager := f.service.runtimePartyManagerForService()
	id, _ := f.service.boundGameSessionCharacterSnapshot(f.leader)
	member, _ := f.service.runtimePartyMemberForIdentity(id)
	created := manager.Create(member, alignedcmd.PartyState{MaxMembers: 4})
	if !created.OK {
		t.Fatal(created.Reason)
	}
	f.service.publishRuntimePartySnapshot(created.Party)
	request := make([]byte, 13)
	binary.LittleEndian.PutUint16(request, inviteAcceptLeaderCharacterID)
	if err := f.service.handleOnlineRequestPeer(f.follower, uint16(dnfenum.CmdPacketRequestPeer), request); err != nil {
		t.Fatal(err)
	}
	// Retire and replace the target while its prompt is open. Record retirement
	// also invalidates pending requests; accepting must never create a team.
	if result := manager.Leave(id.character, id.generation); !result.OK {
		t.Fatal(result.Reason)
	}
	f.service.clearRuntimePartyProjection(id.character, id.generation)
	f.leader.conn.(*bufferConn).write.Reset()
	f.follower.conn.(*bufferConn).write.Reset()
	if err := f.service.handleOnlineResponsePeer(f.leader, uint16(dnfenum.CmdPacketResponsePeer), buildResponsePeerBody(inviteAcceptFollowerCharacterID, 0, 0)); err != nil {
		t.Fatal(err)
	}
	if len(manager.Snapshots()) != 0 {
		t.Fatal("retired prompt recreated party")
	}
	if f.leader.conn.(*bufferConn).write.Len() != 0 || f.follower.conn.(*bufferConn).write.Len() != 0 {
		t.Fatal("retired prompt wrote success frames")
	}
}

// A target that already created a real op12 lobby is the owner of a Join
// request. The official-24.9 live mode-0 trace fixes this direction. Legacy-profile
// invitation expectations remain separately covered without modification.
// Exercise the actual command and both native roster assertions.
func TestExistingLocalPartyReceivesJoinWithoutLeadershipTransfer(t *testing.T) {
	service, requester, owner := newInviteAcceptSessions(t)
	service.options.currentClientVersion = "official-24.9"
	handled, err := service.handleAlignedGameCommand(owner, byte(dnfenum.GameCmdCommand),
		uint16(dnfenum.CmdPacketSetPartyInfo), op12CreatePartyBody())
	if err != nil || !handled {
		t.Fatalf("op12 handled=%t err=%v", handled, err)
	}
	manager := service.runtimePartyManagerForService()
	ownerIdentity, _ := service.boundGameSessionCharacterSnapshot(owner)
	local, found := manager.SnapshotByUser(1002, ownerIdentity.generation)
	if !found || local.Leader != 1002 {
		t.Fatalf("existing target lobby=%+v found=%t", local, found)
	}
	// This helper sends mode-0 request from first arg and acceptance from second.
	inviteThenAccept(t, service, requester, owner)
	for _, player := range []*gameSession{owner, requester} {
		identity, _ := service.boundGameSessionCharacterSnapshot(player)
		joined, ok := manager.SnapshotByUser(identity.character, identity.generation)
		if !ok || joined.ID != local.ID || joined.Leader != 1002 || len(joined.Members) != 2 {
			t.Fatalf("existing owner replaced: found=%t snapshot=%+v", ok, joined)
		}
		state := runtimePartyStateSnapshot(player)
		if state.UserID != 1002 || state.IsLeader != (identity.character == 1002) {
			t.Fatalf("projection=%+v", state)
		}
		op9, realtime := 0, 0
		for _, frame := range splitUpperFrames(t, player.conn.(*bufferConn).write.Bytes()) {
			if frame.msgID == 0x0099 && len(frame.body) > 0 && frame.body[0] > 0 {
				rows := parseRealtimeInfoRows(t, frame.body)
				if len(rows) != 2 || rows[0].uid != 1002 || rows[0].slot != 0 || rows[0].hp != 100 || rows[1].uid != 1001 || rows[1].slot != 1 || rows[1].hp != 100 {
					t.Fatalf("join HP roster=%+v", rows)
				}
				realtime++
			}
			if frame.msgID == 9 && len(frame.body) >= 10 && frame.body[9] == currentSceneOp9ActorDisplayKind {
				// official-24.9 adds one pre-roster flag to the legacy test parser's header.
				offset := 16 + int(binary.LittleEndian.Uint32(frame.body[12:16])) + 13
				if offset >= len(frame.body) || frame.body[offset] != 0 {
					t.Fatal("unexpected official pre-roster flag")
				}
				legacyShape := append(append([]byte(nil), frame.body[:offset]...), frame.body[offset+1:]...)
				rows, leaderSlot := parseOp9PartyFrame(t, legacyShape, false)
				if len(rows) != 2 || rows[0].uid != 1002 || rows[0].slot != 0 || rows[1].uid != 1001 || rows[1].slot != 1 || leaderSlot != 0 {
					t.Fatalf("join slot roster=%+v leaderSlot=%d", rows, leaderSlot)
				}
				op9++
			}
		}
		if op9 == 0 || realtime == 0 {
			t.Fatalf("missing authoritative roster: op9=%d realtime=%d", op9, realtime)
		}
	}
}

// boundGeneration 取一个会话当前权威角色的会话代际（测试里用来查 manager 快照）。
