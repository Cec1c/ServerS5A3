package dnfbridge

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

func TestCompletedSettlementCharacterStatisticCapturesSixU32WithoutResponse(t *testing.T) {
	service, runtime, session, conn, _ := prepareCompletedSettlementRuntime(t)
	conn.write.Reset()
	body := make([]byte, currentDungeonCharacterStatisticBodySize)
	for index := 0; index < 6; index++ {
		binary.LittleEndian.PutUint32(body[index*4:index*4+4], uint32(index+10))
	}

	packet, err := dnfproto.BuildChannelPacket(
		uint16(dnfenum.CmdPacketCharacterStatistic), body, 0, dnfproto.DefaultChannelClassification,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.handleGameUpper(session, packet); err != nil {
		t.Fatal(err)
	}
	if conn.write.Len() != 0 || !runtime.settlementStatisticReceived ||
		!bytes.Equal(runtime.settlementStatisticBody, body) {
		t.Fatalf("op123 capture state=%+v wrote=%x", runtime, conn.write.Bytes())
	}

	if err := service.handleDungeonCharacterStatistic(session, append([]byte(nil), body...)); err != nil {
		t.Fatal(err)
	}
	conflict := append([]byte(nil), body...)
	conflict[0]++
	if err := service.handleDungeonCharacterStatistic(session, conflict); err != nil {
		t.Fatal(err)
	}
	if conn.write.Len() != 0 || !bytes.Equal(runtime.settlementStatisticBody, body) {
		t.Fatalf("op123 replay changed capture=%x wrote=%x", runtime.settlementStatisticBody, conn.write.Bytes())
	}
}

func TestCompletedTutorialFirstOp42BuildsTownActorBeforeTypedTransition(t *testing.T) {
	service, runtime, session, conn, _ := prepareCompletedSettlementRuntime(t)
	conn.write.Reset()
	completeSettlementExitBarriersForTest(t, service, runtime, session)
	packet, err := dnfproto.BuildChannelPacket(
		uint16(dnfenum.CmdPacketGiveupGame), nil, 0, dnfproto.DefaultChannelClassification,
	)
	if err != nil {
		t.Fatal(err)
	}
	conn.write.Reset()
	if err := service.handleGameUpper(session, packet); err != nil {
		t.Fatal(err)
	}

	assertCompletedDungeonTownRouteWithGiveupAckForTest(t, conn.write.Bytes(), wantTutorialCompletionTownTransitionBody())
	if runtime.settlementCardLayoutSent || runtime.settlementCardSelectionSent {
		t.Fatalf("completed tutorial op42 incorrectly entered card flow state=%+v", runtime)
	}
	if !runtime.townReturnPending || !runtime.townReturnOp24Sent ||
		runtime.townReturnRequestMsgID != uint16(dnfenum.CmdPacketGiveupGame) ||
		runtime.Session.Snapshot().Run.Status != worldmap.DungeonRunCompleted || session.dungeon.runtime != nil ||
		session.initialTownRouteStage != currentInitialTownRoutePlayerStateSent ||
		!session.sceneBootstrapTailDeferred ||
		!session.selectedUserInfoRefreshSent ||
		session.selectedUserInfoMode3Sent {
		t.Fatalf("completed op42 state=%+v owner=%p", runtime, session.dungeon.runtime)
	}

	conn.write.Reset()
	if err := service.handleDungeonGiveupGame(session, nil); err != nil {
		t.Fatal(err)
	}
	if conn.write.Len() != 0 {
		t.Fatalf("op42 replay resent op24=%x", conn.write.Bytes())
	}
}

func TestCompletedOrdinaryDungeonOp42CannotBypassCardRequestChain(t *testing.T) {
	service, runtime, session, conn := prepareOrdinaryCompletedSettlementRuntimeForExitTest(t)
	conn.write.Reset()
	completeSettlementExitBarriersForTest(t, service, runtime, session)
	conn.write.Reset()

	if err := service.handleDungeonGiveupGame(session, nil); err != nil {
		t.Fatal(err)
	}
	if conn.write.Len() != 0 {
		t.Fatalf("ordinary pre-card op42 synthesized card/exit packets=%x", conn.write.Bytes())
	}
	if runtime.settlementCardLayoutSent || runtime.settlementCardSelectionSent ||
		runtime.townReturnPending || session.dungeon.runtime != runtime {
		t.Fatalf("ordinary completed op42 bypassed card gate state=%+v owner=%p", runtime, session.dungeon.runtime)
	}

	conn.write.Reset()
	if err := service.handleDungeonGiveupGame(session, nil); err != nil {
		t.Fatal(err)
	}
	if conn.write.Len() != 0 || runtime.settlementCardSelectionSent ||
		runtime.townReturnPending || session.dungeon.runtime != runtime {
		t.Fatalf("ordinary completed op42 did not wait for card selection state=%+v owner=%p wrote=%x",
			runtime, session.dungeon.runtime, conn.write.Bytes())
	}
}

func TestCompletedTutorialOp42WriteFailureRetainsRuntimeForRetry(t *testing.T) {
	service, runtime, session, _, _ := prepareCompletedSettlementRuntime(t)
	completeSettlementExitBarriersForTest(t, service, runtime, session)
	wantErr := errors.New("completed tutorial op24 write failed")
	connection := &failCompletedTownOp24WriteConn{t: t, err: wantErr}
	session.conn = connection

	err := service.handleDungeonGiveupGame(session, nil)
	if !errors.Is(err, wantErr) {
		t.Fatalf("completed tutorial op42 write failure=%v want=%v", err, wantErr)
	}
	if session.dungeon.runtime != runtime || !runtime.townReturnPending || runtime.townReturnOp24Sent {
		t.Fatalf("failed completed tutorial route changed ownership owner=%p state=%+v", session.dungeon.runtime, runtime)
	}
	if runtime.settlementCardLayoutSent || runtime.settlementCardSelectionSent {
		t.Fatalf("failed completed tutorial route incorrectly entered card flow state=%+v", runtime)
	}

	if connection.failedOp24Attempts != 1 || connection.successfulOp24Writes != 0 ||
		connection.positionWrites != 1 || connection.areaWrites != 1 ||
		!bytes.Equal(connection.failedOp24Body, wantTutorialCompletionTownTransitionBody()) {
		t.Fatalf("did not fail exact op24 after one successful location prefix: %+v", connection)
	}
	if rest := assertCompletedDungeonTownPrefixForTest(t, connection.write.Bytes(), wantTutorialCompletionTownTransitionBody()); len(rest) != 0 {
		t.Fatalf("failed op24 unexpectedly committed a trailing packet=%x", rest)
	}
	prefix := append([]byte(nil), connection.write.Bytes()...)
	if err := service.handleDungeonGiveupGame(session, nil); err != nil {
		t.Fatalf("retry completed tutorial op42: %v", err)
	}
	if connection.failedOp24Attempts != 1 || connection.successfulOp24Writes != 1 ||
		connection.positionWrites != 1 || connection.areaWrites != 1 ||
		!bytes.Equal(connection.write.Bytes()[:len(prefix)], prefix) {
		t.Fatalf("retry duplicated or changed successful prefix: %+v", connection)
	}
	retry24, rest := splitGameServerUpperPacket(t, connection.write.Bytes()[len(prefix):])
	if retry24.Header.Classification != 0 || retry24.Header.MsgID != currentSceneTransitionMsgID ||
		!bytes.Equal(retry24.Body, wantTutorialCompletionTownTransitionBody()) {
		t.Fatalf("retry must write exact op24 followed by op42 ACK: %+v body=%x rest=%x", retry24.Header, retry24.Body, rest)
	}
	assertCompletedDungeonTownRouteWithGiveupAckForTest(t, connection.write.Bytes(), wantTutorialCompletionTownTransitionBody())
	if session.dungeon.runtime != nil || !runtime.townReturnOp24Sent {
		t.Fatalf("successful completed tutorial retry did not detach owner=%p state=%+v", session.dungeon.runtime, runtime)
	}
}

func TestCompletedTutorialOp42WaitsForPostSettlementStatisticBarrier(t *testing.T) {
	service, runtime, session, conn, _ := prepareCompletedSettlementRuntime(t)
	conn.write.Reset()

	if err := service.handleDungeonGiveupGame(session, nil); err != nil {
		t.Fatal(err)
	}
	if conn.write.Len() != 0 || runtime.townReturnPending {
		t.Fatalf("pre-barrier op42 changed return state=%+v wrote=%x", runtime, conn.write.Bytes())
	}

	// A captured play-result request is not the final exit barrier. The current
	// EXE emits the exact six-u32 statistic after its settlement builder and
	// before the timed bodyless op42.
	plan := currentDungeonSettlementPacketPlanForTest(t, runtime, 0)
	runtime.settlementResultPlan = &plan
	if err := service.handleDungeonSetPlayResult(
		session,
		make([]byte, currentDungeonPlayResultBaseSize),
	); err != nil {
		t.Fatal(err)
	}
	conn.write.Reset()
	if err := service.handleDungeonGiveupGame(session, nil); err != nil {
		t.Fatal(err)
	}
	if conn.write.Len() != 0 || runtime.townReturnPending {
		t.Fatalf("op46-only op42 changed return state=%+v wrote=%x", runtime, conn.write.Bytes())
	}

	if err := service.handleDungeonCharacterStatistic(
		session,
		make([]byte, currentDungeonCharacterStatisticBodySize),
	); err != nil {
		t.Fatal(err)
	}
	conn.write.Reset()
	if err := service.handleDungeonGiveupGame(session, nil); err != nil {
		t.Fatal(err)
	}
	assertCompletedDungeonTownRouteWithGiveupAckForTest(t, conn.write.Bytes(), wantTutorialCompletionTownTransitionBody())
	if runtime.settlementCardLayoutSent || runtime.settlementCardSelectionSent ||
		!runtime.townReturnOp24Sent || session.dungeon.runtime != nil {
		t.Fatalf("post-barrier tutorial op42 did not commit direct town route state=%+v owner=%p",
			runtime, session.dungeon.runtime)
	}
}

func TestActiveRunOp42KeepsGiveupPendingUntilTownEvidence(t *testing.T) {
	service, session, runtime := newBackToVillageRuntime(t)
	if err := service.handleDungeonGiveupGame(session, nil); err != nil {
		t.Fatal(err)
	}
	if !runtime.townReturnPending || runtime.Session.Snapshot().Run.Status != worldmap.DungeonRunActive {
		t.Fatalf("active op42 state=%+v", runtime)
	}
	connection := session.conn.(*bufferConn)
	connection.write.Reset()
	if err := service.commitPendingDungeonReturnForSceneRequest(session, "test_op42_town_evidence"); err != nil {
		t.Fatal(err)
	}
	ack, rest := splitGameServerUpperPacket(t, connection.write.Bytes())
	if ack.Header.Classification != dnfproto.DefaultChannelClassification ||
		ack.Header.MsgID != uint16(dnfenum.CmdPacketGiveupGame) || !bytes.Equal(ack.Body, []byte{1}) {
		t.Fatalf("confirmed op42 native wait reset ack=%+v body=%x", ack.Header, ack.Body)
	}
	if trailing := splitTownPostTransitionPlayerState(
		t,
		rest,
		session,
		session.selectedCharacterID,
		backToVillageSkillProjectionBody(t, service),
		false,
	); len(trailing) != 0 {
		t.Fatalf("active op42 confirmed return trailing packets=%x", trailing)
	}
	if session.dungeon.runtime != nil || runtime.Session.Snapshot().Run.Status != worldmap.DungeonRunAbandoned {
		t.Fatalf("committed active op42 state=%+v owner=%p", runtime, session.dungeon.runtime)
	}
	connection.write.Reset()
	if err := service.commitPendingDungeonReturnForSceneRequest(session, "test_op42_duplicate_town_evidence"); err != nil {
		t.Fatal(err)
	}
	if connection.write.Len() != 0 {
		t.Fatalf("duplicate confirmation sent packets=%x", connection.write.Bytes())
	}
}

func TestActiveRunOp42AckWriteFailureRetainsOwnerForTownConfirmationRetry(t *testing.T) {
	service, session, runtime := newBackToVillageRuntime(t)
	if err := service.handleDungeonGiveupGame(session, nil); err != nil {
		t.Fatal(err)
	}
	wantErr := errors.New("giveup ack write failed")
	failing := &failNthDungeonWriteConn{failAt: 1, err: wantErr}
	session.conn = failing
	if err := service.commitPendingDungeonReturnForSceneRequest(session, "test_op42_failed_ack"); !errors.Is(err, wantErr) {
		t.Fatalf("confirmation error=%v want=%v", err, wantErr)
	}
	if session.dungeon.runtime != runtime || !runtime.townReturnPending || !runtime.townReturnOp24Sent ||
		runtime.Session.Snapshot().Run.Status != worldmap.DungeonRunActive || failing.write.Len() != 0 {
		t.Fatalf("failed ack retired pending owner=%p state=%+v wrote=%x", session.dungeon.runtime, runtime, failing.write.Bytes())
	}
	connection := &bufferConn{}
	session.conn = connection
	if err := service.commitPendingDungeonReturnForSceneRequest(session, "test_op42_ack_retry"); err != nil {
		t.Fatal(err)
	}
	ack, rest := splitGameServerUpperPacket(t, connection.write.Bytes())
	if ack.Header.Classification != dnfproto.DefaultChannelClassification ||
		ack.Header.MsgID != uint16(dnfenum.CmdPacketGiveupGame) || !bytes.Equal(ack.Body, []byte{1}) {
		t.Fatalf("retry op42 ack=%+v body=%x", ack.Header, ack.Body)
	}
	if trailing := splitTownPostTransitionPlayerState(t, rest, session, session.selectedCharacterID,
		backToVillageSkillProjectionBody(t, service), false); len(trailing) != 0 {
		t.Fatalf("retry trailing packets=%x", trailing)
	}
	if session.dungeon.runtime != nil || runtime.Session.Snapshot().Run.Status != worldmap.DungeonRunAbandoned {
		t.Fatalf("retry did not commit owner=%p state=%+v", session.dungeon.runtime, runtime)
	}
}

func TestCompletedSettlementOp16StartsNextChallengeWithFreshRuntime(t *testing.T) {
	service, completedRuntime, session, conn, _ := prepareCompletedSettlementRuntime(t)
	conn.write.Reset()
	body := make([]byte, 21)
	binary.LittleEndian.PutUint32(body[0:4], uint32(completedRuntime.Dungeon.ID))
	body[4] = 1
	if err := service.handleDungeonSelectUpper(session, body); err != nil {
		t.Fatal(err)
	}
	if conn.write.Len() == 0 || session.dungeon.runtime == nil ||
		session.dungeon.runtime == completedRuntime ||
		session.dungeon.runtime.Session.Snapshot().Run.Status != worldmap.DungeonRunActive {
		t.Fatalf("next challenge runtime old=%p current=%p wrote=%x",
			completedRuntime,
			session.dungeon.runtime,
			conn.write.Bytes())
	}
}

func TestSettlementExitRequestsRejectWrongShapeClassOrStage(t *testing.T) {
	tests := []struct {
		name           string
		msgID          uint16
		body           []byte
		classification byte
	}{
		{name: "op42 body", msgID: uint16(dnfenum.CmdPacketGiveupGame), body: []byte{0}, classification: dnfproto.DefaultChannelClassification},
		{name: "op42 class", msgID: uint16(dnfenum.CmdPacketGiveupGame), classification: 2},
		{name: "op123 body", msgID: uint16(dnfenum.CmdPacketCharacterStatistic), body: make([]byte, 23), classification: dnfproto.DefaultChannelClassification},
		{name: "op123 class", msgID: uint16(dnfenum.CmdPacketCharacterStatistic), body: make([]byte, 24), classification: 2},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service, session, runtime := newBackToVillageRuntime(t)
			packet, err := dnfproto.BuildChannelPacket(test.msgID, test.body, 0, test.classification)
			if err != nil {
				t.Fatal(err)
			}
			if err := service.handleGameUpper(session, packet); err != nil {
				t.Fatal(err)
			}
			if session.conn.(*bufferConn).write.Len() != 0 || runtime.townReturnPending ||
				runtime.settlementStatisticReceived {
				t.Fatalf("rejected request changed state=%+v wrote=%x", runtime, session.conn.(*bufferConn).write.Bytes())
			}
		})
	}

	service, runtime := prepareTutorialScopeRuntime(t, tutorialScopeFixtureOptions{singleMonster: true})
	if err := runtime.Session.CompleteCurrentRoom(); err != nil {
		t.Fatal(err)
	}
	session := &gameSession{
		conn:                &bufferConn{},
		selectedCharacterID: 99,
		dungeon:             dungeonSessionState{runtime: runtime},
	}
	if err := service.handleDungeonGiveupGame(session, nil); err != nil {
		t.Fatal(err)
	}
	if err := service.handleDungeonCharacterStatistic(session, make([]byte, 24)); err != nil {
		t.Fatal(err)
	}
	if session.conn.(*bufferConn).write.Len() != 0 || runtime.townReturnPending || runtime.settlementStatisticReceived {
		t.Fatalf("completed pre-op31 requests changed state=%+v", runtime)
	}
}

func completeSettlementExitBarriersForTest(
	t *testing.T,
	service *Service,
	runtime *runtimeDungeonState,
	session *gameSession,
) {
	t.Helper()
	plan := currentDungeonSettlementPacketPlanForTest(t, runtime, 0)
	runtime.settlementResultPlan = &plan
	if err := service.handleDungeonSetPlayResult(
		session,
		make([]byte, currentDungeonPlayResultBaseSize),
	); err != nil {
		t.Fatal(err)
	}
	if err := service.handleDungeonCharacterStatistic(
		session,
		make([]byte, currentDungeonCharacterStatisticBodySize),
	); err != nil {
		t.Fatal(err)
	}
}

func prepareOrdinaryCompletedSettlementRuntimeForExitTest(
	t *testing.T,
) (*Service, *runtimeDungeonState, *gameSession, *bufferConn) {
	t.Helper()
	service, runtime := prepareTutorialScopeRuntime(t, tutorialScopeFixtureOptions{
		disableTutorial: true,
		singleMonster:   true,
	})
	conn := &bufferConn{}
	session := &gameSession{
		conn:                conn,
		connID:              "ordinary-completed-settlement-exit-test",
		selectedCharacterID: 99,
		dungeon:             dungeonSessionState{runtime: runtime},
		townSelectorOriginSnapshot: currentTownPositionSnapshot{
			CharacterID:   99,
			TownID:        7,
			AreaID:        3,
			PositionX:     474,
			PositionY:     234,
			PositionValid: true,
		},
		townSelectorOriginBound: true,
	}
	if err := freezeCurrentDungeonTownReturnOrigin(session, runtime); err != nil {
		t.Fatal(err)
	}
	completeSettlementRuntimeForTest(t, service, runtime, session)
	return service, runtime, session, conn
}

func assertCompletedDungeonTownRouteForTest(t *testing.T, data, wantTransition []byte) {
	t.Helper()
	if rest := completedDungeonTownRouteTailForTest(t, data, wantTransition); len(rest) != 0 {
		t.Fatalf("non-op42 completed route emitted trailing packet=%x", rest)
	}
}

func assertCompletedDungeonTownRouteWithGiveupAckForTest(t *testing.T, data, wantTransition []byte) {
	t.Helper()
	rest := completedDungeonTownRouteTailForTest(t, data, wantTransition)
	ack, rest := splitGameServerUpperPacket(t, rest)
	if ack.Header.Classification != dnfproto.DefaultChannelClassification || ack.Header.MsgID != uint16(dnfenum.CmdPacketGiveupGame) ||
		!bytes.Equal(ack.Body, []byte{1}) || len(rest) != 0 {
		t.Fatalf("completed op42 route ACK=%+v body=%x trailing=%x", ack.Header, ack.Body, rest)
	}
}

func completedDungeonTownRouteTailForTest(t *testing.T, data, wantTransition []byte) []byte {
	t.Helper()
	rest := assertCompletedDungeonTownPrefixForTest(t, data, wantTransition)
	op24, rest := splitGameServerUpperPacket(t, rest)
	if op24.Header.MsgID != currentSceneTransitionMsgID || op24.Header.Classification != 0 || !bytes.Equal(op24.Body, wantTransition) {
		t.Fatalf("completed town route op24=%+v body=%x rest=%x", op24.Header, op24.Body, rest)
	}
	return rest
}

func assertCompletedDungeonTownPrefixForTest(t *testing.T, data, wantTransition []byte) []byte {
	t.Helper()
	// Typed op24: town, area, u16 count=1, then key/x/y/direction/state/presence.
	if len(wantTransition) != 13 || binary.LittleEndian.Uint16(wantTransition[2:4]) != 1 || wantTransition[12] != 1 {
		t.Fatalf("invalid exact one-owner transition fixture=%x", wantTransition)
	}
	actionTable, rest := splitGameServerUpperPacket(t, data)
	if actionTable.Header.MsgID != uint16(dnfenum.CmdPacketPVPMissionHpPercent) ||
		actionTable.Header.Classification != 0 {
		t.Fatalf("completed town route action table=%+v body=%x", actionTable.Header, actionTable.Body)
	}
	mode0, rest := splitGameServerUpperPacket(t, rest)
	if mode0.Header.MsgID != uint16(dnfenum.CmdPacketSetUDPIPPort) ||
		mode0.Header.Classification != 0 || len(mode0.Body) == 0 || mode0.Body[0] != 0 {
		t.Fatalf("completed town route mode0=%+v body=%x", mode0.Header, mode0.Body)
	}
	mode1, rest := splitGameServerUpperPacket(t, rest)
	if mode1.Header.MsgID != uint16(dnfenum.CmdPacketSetUDPIPPort) ||
		mode1.Header.Classification != 0 || len(mode1.Body) == 0 || mode1.Body[0] != 1 {
		t.Fatalf("completed town route mode1=%+v body=%x", mode1.Header, mode1.Body)
	}
	positionWire := rest
	op22, rest := splitLongHengGameServerUpperPacket(t, rest)
	wantPosition := append([]byte(nil), wantTransition[4:11]...)
	wantPosition = append(wantPosition, byte(currentTownInitialMovementRate), byte(currentTownInitialMovementRate>>8))
	if op22.Header.Classification != 0 || op22.Header.MsgID != currentTownUserPositionNotificationMsgID ||
		op22.Header.Seq != 0 || !bytes.Equal(positionWire[11:16], make([]byte, 5)) ||
		!bytes.Equal(op22.Body, wantPosition) {
		t.Fatalf("completed town route exact op22=%+v body=%x want=%x", op22.Header, op22.Body, wantPosition)
	}
	areaWire := rest
	op23, rest := splitLongHengGameServerUpperPacket(t, rest)
	wantArea := []byte{wantTransition[4], wantTransition[5], wantTransition[0], wantTransition[1]}
	wantArea = append(wantArea, wantTransition[6:12]...)
	if op23.Header.Classification != 0 || op23.Header.MsgID != currentTownUserAreaNotificationMsgID ||
		op23.Header.Seq != 0 || !bytes.Equal(areaWire[11:16], make([]byte, 5)) ||
		!bytes.Equal(op23.Body, wantArea) {
		t.Fatalf("completed town route exact op23=%+v body=%x want=%x", op23.Header, op23.Body, wantArea)
	}
	return rest
}

// Fault selection follows the actual message identity, independent of earlier writes.
type failCompletedTownOp24WriteConn struct {
	bufferConn
	t                    *testing.T
	err                  error
	failedOp24Attempts   int
	successfulOp24Writes int
	positionWrites       int
	areaWrites           int
	failedOp24Body       []byte
}

func (connection *failCompletedTownOp24WriteConn) Write(data []byte) (int, error) {
	packet, rest := splitGameServerUpperPacket(connection.t, data)
	if len(rest) != 0 {
		connection.t.Fatalf("fault fixture requires one complete record per write, rest=%x", rest)
	}
	if packet.Header.Classification == 0 {
		switch packet.Header.MsgID {
		case currentTownUserPositionNotificationMsgID:
			connection.positionWrites++
		case currentTownUserAreaNotificationMsgID:
			connection.areaWrites++
		case currentSceneTransitionMsgID:
			if connection.failedOp24Attempts == 0 {
				connection.failedOp24Attempts++
				connection.failedOp24Body = append([]byte(nil), packet.Body...)
				return 0, connection.err
			}
			connection.successfulOp24Writes++
		}
	}
	return connection.bufferConn.Write(data)
}
