package dnfbridge

import (
	"bytes"
	"testing"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
)

// The reported refusal ("cannot perform this operation during the tournament")
// is produced by the client before it ever sends the legacy op3 EXIT: on
// 2026-09-24 the session on channel 121 (dnf:10005) sent zero exit requests all
// day while the sessions on channels 10/253 (dnf:10001) were acknowledged
// normally. The server side must therefore never grow a channel-kind gate on
// this route - the acknowledgement stays byte-identical no matter which channel
// family the session is standing on.
func TestCurrentChannelExitAckIsIndependentOfSessionChannelFamily(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		channel channelcatalog.Channel
	}{
		{name: "ordinary_area_channel_10", channel: channelcatalog.Channel{ID: 10, Type: 11}},
		{name: "ordinary_area_channel_11", channel: channelcatalog.Channel{ID: 11, Type: 11}},
		{name: "ancient_dungeon_channel_121", channel: channelcatalog.Channel{ID: 121, Type: 22}},
		{name: "death_tower_channel_508", channel: channelcatalog.Channel{ID: 508, Type: 22}},
		{name: "crack_channel_253", channel: channelcatalog.Channel{ID: 253, Type: 40}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			connection := &closeTrackingBufferConn{}
			service := &Service{options: options{
				gameUpperHeader:    gameUpperHeaderServer16,
				gameUpperBodyCodec: gameUpperBodyCodecPlain,
			}}
			session := &gameSession{
				conn:    connection,
				connID:  "channel-exit-family-" + testCase.name,
				channel: testCase.channel,
			}
			request := buildLegacyGamePacketForBridgeTest(
				byte(dnfenum.GameCmdCommand),
				uint16(dnfenum.CmdPacketExit),
				9,
				[]byte{1},
			)
			packets, remaining, skipped, err := dnfproto.SplitLatestGameStream(request, 1024)
			if err != nil {
				t.Fatalf("split: %v", err)
			}
			if len(packets) != 1 || len(remaining) != 0 || skipped != 0 {
				t.Fatalf("split packets=%d remaining=%x skipped=%d", len(packets), remaining, skipped)
			}
			if err := service.handleGameStreamPacket(session, packets[0]); err != nil {
				t.Fatalf("dispatch: %v", err)
			}

			packet, rest := splitLongHengGameServerUpperPacket(t, connection.write.Bytes())
			if packet.Header.Classification != dnfproto.DefaultChannelClassification ||
				packet.Header.MsgID != uint16(dnfenum.CmdPacketExit) ||
				!bytes.Equal(packet.Body, []byte{1}) ||
				len(rest) != 0 {
				t.Fatalf("exit ACK header=%+v body=%x rest=%x", packet.Header, packet.Body, rest)
			}
			if connection.closed {
				t.Fatal("server closed the old channel socket before the client owned the teardown")
			}
		})
	}
}
