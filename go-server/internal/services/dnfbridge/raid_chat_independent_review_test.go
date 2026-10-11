package dnfbridge

import (
	"bytes"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
)

// Independently exercise recipient ownership rather than trusting the source
// roster snapshot. A retired or misbound peer must receive no raid text.
func TestReviewCurrentRaidChatRecipientOwnership(t *testing.T) {
	for _, mode := range []string{"unbound", "wrong_selected", "wrong_account", "unauthenticated", "wrong_type", "left_raid"} {
		t.Run(mode, func(t *testing.T) {
			s, leader, member, lc, mc, _ := prepareApprovedCurrentRaid(t)
			switch mode {
			case "unbound":
				s.unbindGameSession(member)
			case "wrong_selected":
				member.selectedCharacterID = 65000
			case "wrong_account":
				member.accountID = "not-character-owner"
			case "unauthenticated":
				member.accountFromToken = false
			case "wrong_type":
				member.channel.Type = dnfenum.TradeChannelType
			case "left_raid":
				s.removeRuntimeRaidMember(member.selectedCharacterID)
			}
			if err := s.handleCurrentPeerChat(leader, 17, raidChatRequestFixture(56, leader.selectedCharacterID)); err != nil {
				t.Fatal(err)
			}
			ack, rest := splitGameServerUpperPacket(t, lc.write.Bytes())
			echo, trailing := splitGameServerUpperPacket(t, rest)
			if !bytes.Equal(ack.Body, []byte{1}) || echo.Header.MsgID != 118 || len(trailing) != 0 {
				t.Fatal("valid leader lost own chat")
			}
			if mc.write.Len() != 0 {
				t.Fatal("raid chat reached a peer without current authenticated ownership/context")
			}
		})
	}
}

func TestReviewCurrentRaidChatWrongOwnerSenderAndNamedGroupReject(t *testing.T) {
	for _, mode := range []string{"wrong_account", "unauthenticated", "named_group"} {
		t.Run(mode, func(t *testing.T) {
			s, leader, member, lc, mc, state := prepareApprovedCurrentRaid(t)
			body := raidChatRequestFixture(55, member.selectedCharacterID)
			switch mode {
			case "wrong_account":
				member.accountID = "not-character-owner"
			case "unauthenticated":
				member.loginPacketSeen = false
			case "named_group":
				var w packetWriter
				w.writeByte(55)
				w.writeUint16(leader.selectedCharacterID)
				w.writeUint32(0)
				w.writeRawDstr([]byte("hello"))
				w.writeRawDstr(rosterNameBytes(state.Members[runtimeRaidMemberIndex(state, leader.selectedCharacterID)].Name))
				body = w.bytes()
			}
			if err := s.handleCurrentPeerChat(member, 17, body); err != nil {
				t.Fatal(err)
			}
			if lc.write.Len() != 0 {
				t.Fatal("invalid group origin reached recipient")
			}
			if mc.write.Len() != 0 {
				ack, trailing := splitGameServerUpperPacket(t, mc.write.Bytes())
				if len(ack.Body) == 0 || ack.Body[0] != 0 || len(trailing) != 0 {
					t.Fatal("invalid group origin received success/notice")
				}
			}
		})
	}
}
