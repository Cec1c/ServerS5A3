package dnfbridge

import (
	"bytes"
	"encoding/binary"
	"log/slog"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/party"
)

func TestPartyEntryAfterInviteAcceptConfirmsExistingMembership(t *testing.T) {
	f := newInviteAcceptDungeonFixture(t)
	f.formPartyThroughClientAccept(t)
	m := f.service.runtimePartyManagerForService()
	before, _ := m.SnapshotByUser(inviteAcceptLeaderCharacterID, 0)
	for _, sender := range []*gameSession{f.leader, f.follower, f.leader} {
		target := f.follower
		if sender == f.follower {
			target = f.leader
		}
		for _, p := range []*gameSession{f.leader, f.follower} {
			p.conn.(*bufferConn).write.Reset()
		}
		body := make([]byte, 4)
		binary.LittleEndian.PutUint32(body, uint32(target.selectedCharacterID))
		if err := f.service.handleOnlineEntryIntoParty(sender, uint16(dnfenum.CmdPacketEntryIntoParty), body); err != nil {
			t.Fatal(err)
		}
		ack, _ := splitGameServerUpperPacket(t, sender.conn.(*bufferConn).write.Bytes())
		want := party.BuildEntryIntoPartyAck(uint32(target.selectedCharacterID), uint32(sender.selectedCharacterID))
		if !bytes.Equal(ack.Body, want) {
			t.Fatalf("post-accept entry reply=% X, want success % X", ack.Body, want)
		}
		after, _ := m.SnapshotByID(before.ID)
		if !reflect.DeepEqual(before, after) {
			t.Fatalf("confirmation changed party: before=%+v after=%+v", before, after)
		}
		for _, p := range []*gameSession{f.leader, f.follower} {
			for _, frame := range splitUpperFrames(t, p.conn.(*bufferConn).write.Bytes()) {
				if frame.msgID == 11 {
					t.Fatal("confirmation replayed the formation endpoint handshake")
				}
			}
		}
	}
}

func TestPartyEntryFailureOnlyReportsFullForCapacity(t *testing.T) {
	for _, reason := range []string{"stale_member_session", "party_full"} {
		t.Run(reason, func(t *testing.T) {
			f := newInviteAcceptDungeonFixture(t)
			f.formPartyThroughClientAccept(t)
			m := f.service.runtimePartyManagerForService()
			current, _ := m.SnapshotByUser(inviteAcceptLeaderCharacterID, 0)
			if reason == "stale_member_session" {
				if result := m.Leave(inviteAcceptFollowerCharacterID, f.follower.characterGeneration); !result.OK {
					t.Fatal(result.Reason)
				}
				if result := m.Join(current.ID, party.RuntimePartyMember{UserID: inviteAcceptFollowerCharacterID, SessionGeneration: f.follower.characterGeneration + 1}); !result.OK {
					t.Fatal(result.Reason)
				}
			} else {
				// The target belongs to a different lobby; the sender's party is full.
				if result := m.Leave(inviteAcceptFollowerCharacterID, f.follower.characterGeneration); !result.OK {
					t.Fatal(result.Reason)
				}
				for _, id := range []uint16{201, 202, 203} {
					if result := m.Join(current.ID, party.RuntimePartyMember{UserID: id, SessionGeneration: 1}); !result.OK {
						t.Fatal(result.Reason)
					}
				}
				f.follower.party.state = party.RuntimePartySnapshot{}.Settings
			}
			before, _ := m.SnapshotByID(current.ID)
			var logs bytes.Buffer
			f.service.packetLog = nil
			f.service.logger = slog.New(slog.NewJSONHandler(&logs, nil))
			f.leader.conn.(*bufferConn).write.Reset()
			body := make([]byte, 4)
			binary.LittleEndian.PutUint32(body, uint32(inviteAcceptFollowerCharacterID))
			if err := f.service.handleOnlineEntryIntoParty(f.leader, uint16(dnfenum.CmdPacketEntryIntoParty), body); err != nil {
				t.Fatal(err)
			}
			ack, rest := splitGameServerUpperPacket(t, f.leader.conn.(*bufferConn).write.Bytes())
			code := byte(entryIntoPartyFailureGeneric)
			if reason == "party_full" {
				code = entryIntoPartyFailureFull
			}
			if !bytes.Equal(ack.Body, []byte{0, code}) || len(rest) != 0 {
				t.Fatalf("%s reply=% X trailing=%d", reason, ack.Body, len(rest))
			}
			after, _ := m.SnapshotByID(current.ID)
			if !reflect.DeepEqual(before, after) {
				t.Fatal("rejected entry changed party")
			}
			event := r195DiagnosticEvent(t, &logs, "game-party-entry-rejected")
			if event["reason"] != reason || event["error_code"] != float64(code) {
				t.Fatalf("entry diagnostic=%v", event)
			}
		})
	}
}
