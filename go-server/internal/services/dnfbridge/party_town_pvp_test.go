package dnfbridge

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"testing"
)

func TestCurrentTownPVPDoesNotBecomeOrdinaryParty(t *testing.T) {
	for _, mode := range []byte{4, 13} {
		t.Run(fmt.Sprint(mode), func(t *testing.T) {
			s, source, target, src, dst := newCurrentSocialPeerTest(t)
			s.options.currentClientVersion = "official-24.9"
			// Full native 036B78E0 output; only the independently captured mode differs.
			body, _ := hex.DecodeString("0500040000000000000000")
			body[2] = mode
			if handled, err := s.handleOnlinePartyCommand(source, 10, body); !handled || err != nil {
				t.Fatalf("request handled=%t err=%v", handled, err)
			}
			ack, rest := splitGameServerUpperPacket(t, src.write.Bytes())
			if ack.Header.Classification != 1 || ack.Header.MsgID != 10 ||
				!bytes.Equal(ack.Body, []byte{0, 3, mode, 0, 0, 0, 0}) || len(rest) != 0 || dst.write.Len() != 0 {
				t.Fatalf("wrong challenge failure or forwarded party prompt: %+v", ack)
			}
			exportGuildSocialPacket(t, fmt.Sprintf("town-pvp-mode%d-request-failure.bin", mode), ack.Body)
			src.write.Reset()
			response := buildResponsePeerBody(1, mode, 1)
			if handled, err := s.handleOnlinePartyCommand(target, 11, response); !handled || err != nil {
				t.Fatalf("response handled=%t err=%v", handled, err)
			}
			if src.write.Len() != 0 || dst.write.Len() != 0 ||
				runtimePartyStateSnapshot(source).PartyID != 0 || runtimePartyStateSnapshot(target).PartyID != 0 {
				t.Fatal("unrecorded challenge acceptance created state or emitted success")
			}
		})
	}
}

func TestCurrentTownPVPRetiresPendingAcceptRejectCancelTimeout(t *testing.T) {
	for _, mode := range []byte{4, 13} {
		for _, action := range []string{"accept", "reject", "cancel", "timeout"} {
			t.Run(fmt.Sprintf("%d/%s", mode, action), func(t *testing.T) {
				s, source, target, src, dst := newCurrentSocialPeerTest(t)
				s.options.currentClientVersion = "official-24.9"
				inviter, _ := s.boundGameSessionCharacterSnapshot(source)
				acceptor, _ := s.boundGameSessionCharacterSnapshot(target)
				manager := s.runtimePartyManagerForService()
				// Seed a request from the old misrouted handler. New requests no
				// longer enter this owner; replayed responses still cannot join.
				if _, ok := manager.RecordOnlinePeerRequest(acceptor.character, acceptor.generation,
					inviter.character, inviter.generation, mode); !ok {
					t.Fatal("could not seed pending request")
				}
				value := uint32(0)
				if action == "accept" {
					value = 1
				}
				body := buildResponsePeerBody(1, mode, value)
				if err := s.handleOnlineResponsePeer(target, 11, body); err != nil {
					t.Fatal(err)
				}
				for _, check := range []struct {
					conn *bufferConn
					op   uint16
					body []byte
				}{{src, 10, []byte{0, 133, mode, 0, 0, 0, 0}}, {dst, 11, []byte{0, 133, mode}}} {
					packet, rest := splitGameServerUpperPacket(t, check.conn.write.Bytes())
					if packet.Header.Classification != 1 || packet.Header.MsgID != check.op ||
						!bytes.Equal(packet.Body, check.body) || len(rest) != 0 {
						t.Fatalf("wrong terminal response: %+v", packet)
					}
					exportGuildSocialPacket(t, fmt.Sprintf("town-pvp-mode%d-op%d-terminal.bin", mode, check.op), packet.Body)
					check.conn.write.Reset()
				}
				if runtimePartyStateSnapshot(source).PartyID != 0 || runtimePartyStateSnapshot(target).PartyID != 0 {
					t.Fatal("town PvP response created an ordinary dungeon party")
				}
				if err := s.handleOnlineResponsePeer(target, 11, body); err != nil {
					t.Fatal(err)
				}
				if src.write.Len() != 0 || dst.write.Len() != 0 {
					t.Fatal("duplicate challenge response emitted packets")
				}
			})
		}
	}
}
