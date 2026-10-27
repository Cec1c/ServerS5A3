package dnfbridge

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/party"
)

func TestR170FiendWarManualCommandsCannotCreateOrChangeParty(t *testing.T) {
	for _, op := range []dnfenum.CmdPacket{dnfenum.CmdPacketSetPartyInfo,
		dnfenum.CmdPacketEntryIntoParty, dnfenum.CmdPacketEnterWarroom,
		dnfenum.CmdPacketWalkoutPartyMember, dnfenum.CmdPacketChangeHost,
		dnfenum.CmdPacketChangePartyMemberPosition, dnfenum.CmdPacketRegisterQuickParty,
		dnfenum.CmdPacketDirectEntranceDungeonQuickParty} {
		t.Run(fmt.Sprint(op), func(t *testing.T) {
			s, self, peer, _ := newFiendWarPartyAdmissionDiagnosticFixture(t)
			self.dungeon.runtime, peer.dungeon.runtime = nil, nil
			before := runtimePartyStateSnapshot(self)
			handled, err := s.handleOnlinePartyCommand(self, uint16(op), []byte{22, 0})
			if !handled || err != nil {
				t.Fatalf("handled=%v err=%v", handled, err)
			}
			if !reflect.DeepEqual(before, runtimePartyStateSnapshot(self)) || len(s.runtimePartyManager.Snapshots()) != 0 {
				t.Fatal("manual command mutated party graph")
			}
			if peer.conn.(*bufferConn).write.Len() != 0 {
				t.Fatal("manual command reached peer")
			}
		})
	}
}

func TestR170FiendWarWaitingInviteAndStaleAcceptCannotFormParty(t *testing.T) {
	for _, mode := range []byte{0, 12} {
		s, self, peer, _ := newFiendWarPartyAdmissionDiagnosticFixture(t)
		self.dungeon.runtime, peer.dungeon.runtime = nil, nil
		before := runtimePartyStateSnapshot(self)
		body := make([]byte, 13)
		binary.LittleEndian.PutUint16(body, peer.selectedCharacterID)
		body[2] = mode
		if err := s.handleOnlineRequestPeer(self, 10, body); err != nil {
			t.Fatal(err)
		}
		packets := splitAllGameServerUpperPackets(t, self.conn.(*bufferConn).write.Bytes())
		if len(packets) != 1 || !bytes.Equal(packets[0].Body, []byte{0, 3, mode}) {
			t.Fatalf("invalid request failure: %+v", packets)
		}
		if peer.conn.(*bufferConn).write.Len() != 0 {
			t.Fatal("manual invite forwarded")
		}
		// An invitation created before switching channels cannot bypass admission.
		s.runtimePartyManager.RecordInvite(peer.selectedCharacterID, peer.characterGeneration, self.selectedCharacterID, self.characterGeneration, 0, mode)
		if err := s.handleOnlineResponsePeer(peer, 11, buildResponsePeerBody(self.selectedCharacterID, mode, 0)); err != nil {
			t.Fatal(err)
		}
		if len(s.runtimePartyManager.Snapshots()) != 0 || !reflect.DeepEqual(before, runtimePartyStateSnapshot(self)) {
			t.Fatal("stale invite created party")
		}
		if _, result, ok := s.createOrJoinManagedRuntimeParty(peer, self); ok || result.Reason != "fiendwar_automatic_encounter_party_only" {
			t.Fatal("direct helper bypassed automatic formation", result)
		}
	}
}

func TestR170FiendWarManualGateKeepsTradeAndOrdinaryChannels(t *testing.T) {
	s, self, _, _ := newFiendWarPartyAdmissionDiagnosticFixture(t)
	if handled, err := s.rejectCurrentFiendWarActivePartyRequest(self, 10, party.RequestPeerRequest{TargetID: 22, Mode: 1}); handled || err != nil {
		t.Fatal("trade consumed")
	}
	self.channel.Type = 0
	if handled, err := s.rejectCurrentFiendWarManualPartyCommand(self, 12); handled || err != nil {
		t.Fatal("ordinary lobby consumed")
	}
}
