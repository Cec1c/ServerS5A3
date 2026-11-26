package dnfbridge

import (
	"bytes"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
)

func armSeraCompletedReturnUpper15Fixture(t *testing.T, session *gameSession) {
	t.Helper()
	session.townMu.Lock()
	session.initialTownRouteCharacterID = session.selectedCharacterID
	session.initialTownRouteStage = currentInitialTownRoutePlayerStateSent
	session.sceneBootstrapTailDeferred = true
	session.sceneBootstrapTailSent = false
	session.initialTownLegacySceneReadyAccepted = false
	session.townSceneReadyCharacterID = session.selectedCharacterID
	session.townPositionSnapshot = currentTownPositionSnapshot{
		CharacterID:   session.selectedCharacterID,
		TownID:        38,
		AreaID:        0,
		PositionX:     642,
		PositionY:     193,
		MovementCode:  5,
		PositionValid: true,
	}
	armCurrentCompletedTownSceneTailOwnerLocked(session, session.selectedCharacterID)
	session.townMu.Unlock()

	if !currentCompletedTownReturnPendingSettlement(session) {
		t.Fatal("fixture did not arm the completed-return tail owner")
	}
}

func TestSeraCompletedReturnUpper15SettlesPendingTailAndOpensContext(t *testing.T) {
	service, session, _ := newTownMoveTest(t)
	session.characterGeneration = 1
	armSeraCompletedReturnUpper15Fixture(t, session)

	connection := session.conn.(*bufferConn)
	connection.write.Reset()
	if err := service.sendEnterSelectDungeonState(
		session,
		currentTownEnterSelectUpperRequestSource,
		false,
		true,
	); err != nil {
		t.Fatal(err)
	}

	if currentCompletedTownReturnPendingSettlement(session) {
		t.Fatal("upper op15 left the completed-return tail pending")
	}
	if !session.enterSelectDungeonContextSent {
		t.Fatal("upper op15 skipped the dungeon-selector context")
	}

	ack, rest := splitGameServerUpperPacket(t, connection.write.Bytes())
	if ack.Header.Classification != dnfproto.DefaultChannelClassification ||
		ack.Header.MsgID != uint16(dnfenum.CmdPacketEnterSelectDungeon) ||
		!bytes.Equal(ack.Body, []byte{1, 1, 0}) {
		t.Fatalf("upper op15 ACK header=%+v body=%x", ack.Header, ack.Body)
	}
	fatigue, rest := splitGameServerUpperPacket(t, rest)
	if fatigue.Header.Classification != 0 || fatigue.Header.MsgID != currentFatigueMsgID {
		t.Fatalf("upper op15 fatigue header=%+v", fatigue.Header)
	}
	contextPacket, trailing := splitGameServerUpperPacket(t, rest)
	if contextPacket.Header.Classification != 0 || contextPacket.Header.MsgID != currentDungeonContextMsgID ||
		len(contextPacket.Body) != 37 || len(trailing) != 0 {
		t.Fatalf("upper op15 context header=%+v body=%x trailing=%x", contextPacket.Header, contextPacket.Body, trailing)
	}
}
