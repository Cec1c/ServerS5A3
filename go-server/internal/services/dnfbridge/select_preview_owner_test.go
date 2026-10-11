package dnfbridge

import (
	"context"
	"encoding/binary"
	"strconv"
	"testing"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func TestSelectPreviewKeepsSelectedChannelThroughRemoval(t *testing.T) {
	for _, channel := range []int{10, 11, 23, 32, 121} {
		t.Run(strconv.Itoa(channel), func(t *testing.T) {
			conn := &bufferConn{}
			repos := testRepositoryGroup()
			service := &Service{options: options{gameUpperBodyCodec: gameUpperBodyCodecPlain}, repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true }}
			session := &gameSession{conn: conn, selectedCharacterID: 31, channel: channelcatalog.Channel{ID: channel}}
			character := dnfrepo.CharacterRecord{CharacterID: "31", Name: "q7a", Level: 90, Stats: map[string]int64{"grow_type": 0x21}}
			if err := service.sendCurrentSelectPreviewObjectState(context.Background(), session, 31, "q7a", character, true, "owned_preview_regression"); err != nil {
				t.Fatal(err)
			}
			rest := conn.write.Bytes()
			for page := 0; page < currentSceneOverseerPageCount; page++ {
				packet, next := splitGameServerUpperPacket(t, rest)
				if packet.Header.MsgID != uint16(dnfenum.CmdPacketRequestOverseer) {
					t.Fatalf("page message=%d", packet.Header.MsgID)
				}
				rest = next
			}
			_, rest = splitGameServerUpperPacket(t, rest)
			mode0, rest := splitGameServerUpperPacket(t, rest)
			mode1, _ := splitGameServerUpperPacket(t, rest)
			if mode0.Body[4] != byte(channel) || mode1.Body[4] != byte(channel) || binary.LittleEndian.Uint16(mode0.Body[0x4c:0x4e]) != 31 || binary.LittleEndian.Uint16(mode1.Body[0x15:0x17]) != 31 {
				t.Fatalf("preview must use selected UID31 and owner%d", channel)
			}
			if !session.selectPreviewObjectStateSent || session.selectPreviewActorOwnerChannel != byte(channel) || mode1.Body[currentMode1CreateCountOffset] != 0 {
				t.Fatal("preview binding state or equipment changed")
			}
			written := conn.write.Len()
			if err := service.sendCurrentSelectPreviewObjectState(context.Background(), session, 31, "q7a", character, true, "duplicate"); err != nil || conn.write.Len() != written {
				t.Fatal("duplicate preview emitted packets")
			}
			// Removal belongs to the already created actor, even if the current
			// channel context changes before the dungeon transition.
			session.channel.ID = 32
			if err := service.sendCurrentSceneOp9PreviewActorRemovalOnce(session, "transition"); err != nil {
				t.Fatal(err)
			}
			removal, trailing := splitGameServerUpperPacket(t, conn.write.Bytes()[written:])
			if len(trailing) != 0 || removal.Body[7] != byte(channel) || binary.LittleEndian.Uint16(removal.Body[4:6]) != 31 || removal.Body[9] != currentSceneOp9ActorRemoveKind {
				t.Fatalf("removal owner must be original channel%d", channel)
			}
		})
	}
}
