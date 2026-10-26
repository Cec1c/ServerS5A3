package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
)

func assertCurrentCrossSceneProfileFramePair(
	t *testing.T,
	raw []byte,
	headerSize int,
	actorID uint16,
	route byte,
	owner byte,
) (mode8Body, mode3Body []byte) {
	t.Helper()
	mode8Packet, rest := splitGameServerUpperPacketWithHeader(t, raw, headerSize)
	mode3Packet, tail := splitGameServerUpperPacketWithHeader(t, rest, headerSize)
	if len(tail) != 0 {
		t.Fatalf("cross-scene inspection emitted bytes after mode8+mode3: %d", len(tail))
	}
	if mode8Packet.Header.Classification != 0 || mode3Packet.Header.Classification != 0 ||
		mode8Packet.Header.MsgID != uint16(dnfenum.CmdPacketSetUDPIPPort) ||
		mode3Packet.Header.MsgID != uint16(dnfenum.CmdPacketSetUDPIPPort) {
		t.Fatalf("cross-scene inspection classes/message ids=%d:%d/%d:%d",
			mode8Packet.Header.Classification, mode8Packet.Header.MsgID,
			mode3Packet.Header.Classification, mode3Packet.Header.MsgID)
	}
	mode8 := upperFrame{msgID: mode8Packet.Header.MsgID, body: mode8Packet.Body}
	mode3 := upperFrame{msgID: mode3Packet.Header.MsgID, body: mode3Packet.Body}
	if len(mode8.body) < currentCrossSceneMode0KeyOffset+2 ||
		mode8.body[0] != currentCrossSceneMode8Subtype ||
		binary.LittleEndian.Uint16(mode8.body[1:3]) != currentCrossSceneMode8Count {
		t.Fatalf("cross-scene mode8 header=%x", mode8.body[:min(len(mode8.body), 96)])
	}
	if len(mode3.body) < currentCrossSceneMode3KeyOffset+2 ||
		mode3.body[0] != currentCrossSceneMode3Subtype ||
		binary.LittleEndian.Uint16(mode3.body[1:3]) != currentCrossSceneMode8Count {
		t.Fatalf("cross-scene normal mode3 header=%x", mode3.body[:min(len(mode3.body), 96)])
	}
	if key := binary.LittleEndian.Uint16(mode8.body[currentCrossSceneMode0KeyOffset : currentCrossSceneMode0KeyOffset+2]); key != actorID {
		t.Fatalf("mode8 mode0 key=%d want actor=%d", key, actorID)
	}
	if key := binary.LittleEndian.Uint16(mode3.body[currentCrossSceneMode3KeyOffset : currentCrossSceneMode3KeyOffset+2]); key != actorID {
		t.Fatalf("normal mode3 key=%d want actor=%d", key, actorID)
	}
	if mode8.body[3] != route || mode8.body[4] != owner {
		t.Fatalf("mode8 namespace route=%d owner=%d want %d/%d", mode8.body[3], mode8.body[4], route, owner)
	}
	if mode3.body[3] != route || mode3.body[4] != owner {
		t.Fatalf("normal mode3 namespace route=%d owner=%d want %d/%d", mode3.body[3], mode3.body[4], route, owner)
	}
	innerStart := len(mode8.body) - (len(mode3.body) - currentCrossSceneSourcePrefixBytes)
	if innerStart < currentCrossSceneSourcePrefixBytes || innerStart+1 >= len(mode8.body) ||
		mode8.body[innerStart] != route || mode8.body[innerStart+1] != owner {
		t.Fatalf("mode8 embedded mode3 namespace offset=%d mode8_len=%d mode3_len=%d", innerStart, len(mode8.body), len(mode3.body))
	}
	if !bytes.Equal(mode8.body[innerStart:], mode3.body[currentCrossSceneSourcePrefixBytes:]) {
		t.Fatal("normal mode3 identity/tail differs from the mode8 priming copy")
	}
	return mode8.body, mode3.body
}

func assertPeerUserInfoCrossSceneProfile(t *testing.T, fixture *peerUserInfoActiveDungeonFixture) {
	t.Helper()
	if fixture.service.currentDungeonPeerUserInfoAllowed(context.Background(), fixture.source, fixture.target, 1001, 1002) {
		t.Fatal("fixture must fail the original same-run gate")
	}
	handled, err := fixture.service.handleCurrentPeerUserInfoRequest(fixture.source, peerUserInfoActiveDungeonRequest(1002))
	if err != nil || !handled {
		t.Fatalf("cross-scene request handled=%v err=%v", handled, err)
	}
	assertCurrentCrossSceneProfileFramePair(
		t,
		fixture.source.conn.(*bufferConn).write.Bytes(),
		fixture.service.gameUpperHeaderSize(),
		1002,
		fixture.service.currentSceneActorRoute(),
		currentConnectionTownActorOwnerContext(fixture.source),
	)
	if fixture.target.conn.(*bufferConn).write.Len() != 0 || fixture.source.rosterRequested {
		t.Fatal("cross-scene inspection wrote to its target or bootstrapped a roster")
	}
}

func TestCrossScenePeerRequestPrimesMode8ThenOpensNormalMode3(t *testing.T) {
	fixture := newTownPartyActorHPFixture(t, true)
	s, viewer, actor := fixture.service, fixture.target, fixture.actor
	viewer.accountID = "dnf:1"
	actor.accountID = "dnf:2"
	viewer.connectionTownActorOwnerChannel = byte(viewer.channel.ID)
	actorInfo := fixture.actorInfo
	actorInfo.AreaID++
	s.onlinePlayers.EnterArea(&actorInfo)
	if s.onlinePlayers.PeerInSameArea(29, 17) {
		t.Fatal("fixture still has both characters in the same scene")
	}
	fixture.reset()
	if err := s.handleGameCommand(viewer, byte(dnfenum.GameCmdCommand), uint16(dnfenum.GameTypeGetUserInfo), []byte{17, 0, 3}); err != nil {
		t.Fatal(err)
	}
	assertCurrentCrossSceneProfileFramePair(
		t,
		fixture.targetConn.write.Bytes(),
		s.gameUpperHeaderSize(),
		17,
		s.currentSceneActorRoute(),
		currentConnectionTownActorOwnerContext(viewer),
	)
	if viewer.rosterRequested || fixture.actorConn.write.Len() != 0 {
		t.Fatal("inspection triggered selector bootstrap or wrote to the inspected actor")
	}
}

func TestCrossScenePeerRequestRejectsCachedPartyWithoutMembership(t *testing.T) {
	fixture := newTownPartyActorHPFixture(t, false)
	s, viewer, actor := fixture.service, fixture.target, fixture.actor
	viewer.accountID, actor.accountID = "dnf:1", "dnf:2"
	viewer.connectionTownActorOwnerChannel = byte(viewer.channel.ID)
	actorInfo := fixture.actorInfo
	actorInfo.AreaID++
	s.onlinePlayers.EnterArea(&actorInfo)
	fixture.reset()
	handled, err := s.handleCurrentPeerUserInfoRequest(viewer, []byte{17, 0, 3})
	if err != nil || !handled || fixture.targetConn.write.Len() != 0 || fixture.actorConn.write.Len() != 0 {
		t.Fatalf("cached-only party inspection: handled=%v err=%v viewerBytes=%d actorBytes=%d", handled, err, fixture.targetConn.write.Len(), fixture.actorConn.write.Len())
	}
}

func TestCrossScenePeerRequestRejectsAppearanceChangedDuringPrepare(t *testing.T) {
	f := newCurrentCrossSceneProfileBatchFixture(t)
	actorLoads := 0
	wrapCurrentCrossSceneProfileBatchCharacterLoads(t, f.service, func(id string) {
		if id == "17" {
			actorLoads++
			if actorLoads == 2 {
				invalidateCurrentCrossSceneAppearance(f.actor)
			}
		}
	})
	f.reset()
	if err := f.service.sendCurrentCrossScenePeerProfile(context.Background(), f.target, f.actor, 29, 17); err != nil {
		t.Fatal(err)
	}
	if actorLoads < 2 || f.targetConn.write.Len() != 0 {
		t.Fatalf("inspection did not reject superseded appearance: loads=%d bytes=%d", actorLoads, f.targetConn.write.Len())
	}
}
