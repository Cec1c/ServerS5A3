package dnfbridge

import (
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
)

func TestR195FiendWarManualGatePreservesOtherProfilesAndPeerModes(t *testing.T) {
	for _, tc := range []struct {
		name, profile string
		channel       byte
		command       dnfenum.CmdPacket
		blocked       bool
	}{
		{"current_fiend_formation", "official-24.9", 42, dnfenum.CmdPacketSetPartyInfo, true},
		{"ordinary_formation", "official-24.9", 1, dnfenum.CmdPacketSetPartyInfo, false},
		{"anton_formation", "official-24.9", byte(dnfenum.RaidChannelType), dnfenum.CmdPacketSetPartyInfo, false},
		{"legacy_formation", "legacy", 42, dnfenum.CmdPacketSetPartyInfo, false},
		// op10/op11 carry party and trade modes; their existing decoders and
		// mode-specific handlers must run, rather than blanket command rejection.
		{"peer_request_decodes_mode", "official-24.9", 42, dnfenum.CmdPacketRequestPeer, false},
		{"peer_response_decodes_mode", "official-24.9", 42, dnfenum.CmdPacketResponsePeer, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conn := &bufferConn{}
			s := &Service{options: options{currentClientVersion: tc.profile, gameUpperHeader: gameUpperHeaderChannel13, gameUpperBodyCodec: gameUpperBodyCodecPlain}}
			p := &gameSession{conn: conn, selectedCharacterID: 19}
			p.channel.Type = tc.channel
			handled, err := s.rejectCurrentFiendWarManualPartyCommand(p, uint16(tc.command))
			if err != nil || handled != tc.blocked {
				t.Fatalf("handled=%t err=%v", handled, err)
			}
			if !tc.blocked && conn.write.Len() != 0 {
				t.Fatal("out-of-scope command wrote a rejection")
			}
		})
	}
}
