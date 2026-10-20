package dnfbridge

import (
	"bytes"
	"encoding/hex"
	"testing"
)

// This fixture is independently consumed by the actual 24.9 EXE loop
// 02043285..02043638 in codex_r32_op24_native.py. Eight-byte rows make the
// native loop read the selected ID 29 as 33792 and run out of packet bytes.
func TestCurrentTownTransitionMatchesVerifiedTwoActorNativeFixture(t *testing.T) {
	service, session, _ := newTownMoveTest(t)
	service.onlinePlayers = newOnlinePlayerManager()
	service.onlinePlayers.EnterArea(&onlinePlayerInfo{
		CharacterID: 17, TownID: 38, AreaID: 0,
		ChannelID: session.channel.ID, PositionX: 320, PositionY: 210,
		Direction: 1, AreaState: 4,
	})
	if err := service.handleTownSetUserArea(session, buildTownMoveRequest(38, 0, 900, 250, 5)); err != nil {
		t.Fatal(err)
	}
	packet, rest := splitTownAreaTransitionSequence(t, session.conn.(*bufferConn).write.Bytes(), 29, 38, 0, 900, 250, 5, 3)
	want, err := hex.DecodeString("2600020011004001d2000104011d008403fa00050301")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(packet.Body, want) || len(rest) != 0 {
		t.Fatalf("native two-actor fixture mismatch: body=%x trailing=%x want=%x", packet.Body, rest, want)
	}
}
