package dnfbridge

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

type completedReturnAckPacketForTest struct {
	classification byte
	msgID          uint16
	body           []byte
}

func completedReturnAckPacketsForTest(t *testing.T, data []byte) []completedReturnAckPacketForTest {
	t.Helper()
	packets := make([]completedReturnAckPacketForTest, 0)
	for len(data) > 0 {
		packet, rest := splitCurrentGameServerUpperPacketAuto(t, data)
		packets = append(packets, completedReturnAckPacketForTest{
			classification: packet.Header.Classification,
			msgID:          packet.Header.MsgID,
			body:           append([]byte(nil), packet.Body...),
		})
		data = rest
	}
	return packets
}

func completedReturnAckCountForTest(t *testing.T, data []byte, classification byte, msgID uint16) int {
	t.Helper()
	count := 0
	for _, packet := range completedReturnAckPacketsForTest(t, data) {
		if packet.classification == classification && packet.msgID == msgID {
			count++
		}
	}
	return count
}

type completedReturnAckAssetCountForTest struct {
	inventoryRows  int
	inventoryUnits int64
	warehouseRows  int
	warehouseUnits int64
}

func completedReturnAckAssetCount(ctx context.Context, service *Service) (completedReturnAckAssetCountForTest, error) {
	repositories, ok := service.repositoryGroup()
	if !ok || repositories.Inventory == nil {
		return completedReturnAckAssetCountForTest{}, errors.New("inventory repository unavailable")
	}
	record, found, err := repositories.Inventory.Load(ctx, "99")
	if err != nil {
		return completedReturnAckAssetCountForTest{}, err
	}
	if !found {
		return completedReturnAckAssetCountForTest{}, errors.New("inventory record missing")
	}
	count := completedReturnAckAssetCountForTest{
		inventoryRows: len(record.Slots),
		warehouseRows: len(record.Warehouse),
	}
	for _, stack := range record.Slots {
		count.inventoryUnits += stack.Count
	}
	for _, stack := range record.Warehouse {
		count.warehouseUnits += stack.Count
	}
	return count, nil
}

func seedCompletedReturnAckAssetForTest(t *testing.T, service *Service) completedReturnAckAssetCountForTest {
	t.Helper()
	repositories, ok := service.repositoryGroup()
	if !ok || repositories.Inventory == nil {
		t.Fatal("inventory repository unavailable")
	}
	if err := repositories.Inventory.Save(context.Background(), dnfrepo.InventoryRecord{
		CharacterID: "99",
		Slots: map[string]dnfrepo.ItemStack{
			"0:250": {ItemID: 9001, Count: 7},
		},
	}); err != nil {
		t.Fatalf("seed completed return inventory: %v", err)
	}
	count, err := completedReturnAckAssetCount(context.Background(), service)
	if err != nil {
		t.Fatalf("read completed return asset count: %v", err)
	}
	return count
}

type completedReturnAckObserveConn struct {
	*bufferConn
	t         *testing.T
	service   *Service
	session   *gameSession
	runtime   *runtimeDungeonState
	assets    completedReturnAckAssetCountForTest
	ackWrites int
}

func (connection *completedReturnAckObserveConn) Write(data []byte) (int, error) {
	packet, rest := splitCurrentGameServerUpperPacketAuto(connection.t, data)
	if len(rest) != 0 {
		connection.t.Errorf("completed return observer requires one packet per write: rest=%x", rest)
	}
	if packet.Header.Classification == 1 && packet.Header.MsgID == uint16(dnfenum.CmdPacketGiveupGame) {
		connection.ackWrites++
		if !bytes.Equal(packet.Body, []byte{1}) {
			connection.t.Errorf("completed op42 acknowledgement body=%x want=01", packet.Body)
		}
		if connection.session.dungeon.runtime != connection.runtime {
			connection.t.Errorf("op42 was written after detach: owner=%p want=%p",
				connection.session.dungeon.runtime, connection.runtime)
		}
		if !connection.runtime.townReturnPending || !connection.runtime.townReturnOp24Sent ||
			connection.runtime.townReturnAckSent {
			connection.t.Errorf("op42 write ordering state pending=%t op24=%t ack=%t",
				connection.runtime.townReturnPending,
				connection.runtime.townReturnOp24Sent,
				connection.runtime.townReturnAckSent)
		}
		if status := connection.runtime.Session.Snapshot().Run.Status; status != worldmap.DungeonRunCompleted {
			connection.t.Errorf("op42 write state=%s want=%s", status, worldmap.DungeonRunCompleted)
		}
		if count, err := completedReturnAckAssetCount(context.Background(), connection.service); err != nil {
			connection.t.Errorf("read inventory at op42 write: %v", err)
		} else if count != connection.assets {
			connection.t.Errorf("op42 write changed assets: before=%+v at_write=%+v", connection.assets, count)
		}
	}
	return connection.bufferConn.Write(data)
}

type completedReturnAckFailureConn struct {
	*bufferConn
	t       *testing.T
	service *Service
	session *gameSession
	runtime *runtimeDungeonState
	assets  completedReturnAckAssetCountForTest
	err     error
	failed  bool

	writeCalls      int
	acceptedWrites  int
	failedWrites    int
	acceptedBytes   int
	ackAttempts     int
	failedAckWrites int
}

func (connection *completedReturnAckFailureConn) Write(data []byte) (int, error) {
	connection.writeCalls++
	packet, rest := splitCurrentGameServerUpperPacketAuto(connection.t, data)
	if len(rest) != 0 {
		connection.t.Errorf("completed return ACK failure requires one packet per write: rest=%x", rest)
	}
	if packet.Header.Classification == 1 && packet.Header.MsgID == uint16(dnfenum.CmdPacketGiveupGame) {
		connection.ackAttempts++
		if !bytes.Equal(packet.Body, []byte{1}) {
			connection.t.Errorf("failed op42 acknowledgement body=%x want=01", packet.Body)
		}
		if connection.session.dungeon.runtime != connection.runtime {
			connection.t.Errorf("op42 failed after detach: owner=%p want=%p",
				connection.session.dungeon.runtime, connection.runtime)
		}
		if !connection.runtime.townReturnPending || !connection.runtime.townReturnOp24Sent ||
			connection.runtime.townReturnAckSent {
			connection.t.Errorf("failed op42 write ordering state pending=%t op24=%t ack=%t",
				connection.runtime.townReturnPending,
				connection.runtime.townReturnOp24Sent,
				connection.runtime.townReturnAckSent)
		}
		if status := connection.runtime.Session.Snapshot().Run.Status; status != worldmap.DungeonRunCompleted {
			connection.t.Errorf("failed op42 write state=%s want=%s", status, worldmap.DungeonRunCompleted)
		}
		if count, err := completedReturnAckAssetCount(context.Background(), connection.service); err != nil {
			connection.t.Errorf("read inventory at failed op42 write: %v", err)
		} else if count != connection.assets {
			connection.t.Errorf("failed op42 write changed assets: before=%+v at_write=%+v", connection.assets, count)
		}
		if !connection.failed {
			connection.failed = true
			connection.failedWrites++
			connection.failedAckWrites++
			return 0, connection.err
		}
	}
	n, err := connection.bufferConn.Write(data)
	if n > 0 {
		connection.acceptedBytes += n
	}
	if err == nil && n == len(data) {
		connection.acceptedWrites++
	} else {
		connection.failedWrites++
	}
	return n, err
}

type completedReturnAckOp24FailureConn struct {
	*bufferConn
	t        *testing.T
	err      error
	failOnce bool

	writeCalls         int
	acceptedWrites     int
	failedWrites       int
	acceptedBytes      int
	failedOp24Attempts int
	op24Writes         int
	failedOp24Body     []byte
}

func (connection *completedReturnAckOp24FailureConn) Write(data []byte) (int, error) {
	connection.writeCalls++
	packet, rest := splitCurrentGameServerUpperPacketAuto(connection.t, data)
	if len(rest) != 0 {
		connection.t.Errorf("completed return op24 failure requires one packet per write: rest=%x", rest)
	}
	if connection.failOnce && packet.Header.Classification == 0 && packet.Header.MsgID == currentSceneTransitionMsgID {
		connection.failOnce = false
		connection.failedOp24Attempts++
		connection.failedWrites++
		connection.failedOp24Body = append([]byte(nil), packet.Body...)
		return 0, connection.err
	}
	n, err := connection.bufferConn.Write(data)
	if n > 0 {
		connection.acceptedBytes += n
	}
	if err == nil && n == len(data) {
		connection.acceptedWrites++
		if packet.Header.Classification == 0 && packet.Header.MsgID == currentSceneTransitionMsgID {
			connection.op24Writes++
		}
	} else {
		connection.failedWrites++
	}
	return n, err
}

func TestCompletedDirectOp42AckFollowsTypedOp24BeforeImmediateRetirement(t *testing.T) {
	service, runtime, session, connection, _ := prepareCompletedSettlementRuntime(t)
	completeSettlementExitBarriersForTest(t, service, runtime, session)
	connection.write.Reset()
	assets := seedCompletedReturnAckAssetForTest(t, service)
	observer := &completedReturnAckObserveConn{
		bufferConn: connection,
		t:          t,
		service:    service,
		session:    session,
		runtime:    runtime,
		assets:     assets,
	}
	session.conn = observer

	request, err := dnfproto.BuildChannelPacket(
		uint16(dnfenum.CmdPacketGiveupGame), nil, 0, dnfproto.DefaultChannelClassification,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.handleGameUpper(session, request); err != nil {
		t.Fatalf("completed direct op42: %v", err)
	}
	if observer.ackWrites != 1 {
		t.Fatalf("completed direct op42 wrote %d acknowledgements; want one", observer.ackWrites)
	}
	if session.dungeon.runtime != nil {
		t.Fatalf("completed direct op42 did not retire immediately: owner=%p", session.dungeon.runtime)
	}
	if !runtime.townReturnPending || !runtime.townReturnOp24Sent || !runtime.townReturnAckSent ||
		runtime.townReturnRequestMsgID != uint16(dnfenum.CmdPacketGiveupGame) {
		t.Fatalf("completed direct op42 lifecycle state=%+v", runtime)
	}
	if status := runtime.Session.Snapshot().Run.Status; status != worldmap.DungeonRunCompleted {
		t.Fatalf("completed direct op42 status=%s want=%s", status, worldmap.DungeonRunCompleted)
	}

	packets := completedReturnAckPacketsForTest(t, connection.write.Bytes())
	op24Index := -1
	ackIndex := -1
	op24Count := 0
	ackCount := 0
	for index, packet := range packets {
		if packet.classification == 0 && packet.msgID == currentSceneTransitionMsgID {
			op24Count++
			op24Index = index
			if !bytes.Equal(packet.body, wantTutorialCompletionTownTransitionBody()) {
				t.Fatalf("completed direct op42 typed op24 body=%x", packet.body)
			}
		}
		if packet.classification == 1 && packet.msgID == uint16(dnfenum.CmdPacketGiveupGame) {
			ackCount++
			ackIndex = index
			if !bytes.Equal(packet.body, []byte{1}) {
				t.Fatalf("completed direct op42 acknowledgement body=%x want=01", packet.body)
			}
		}
	}
	if op24Count != 1 || ackCount != 1 || op24Index < 0 || ackIndex <= op24Index {
		t.Fatalf("completed direct op42 packet order op24=%d ack=%d op24_index=%d ack_index=%d",
			op24Count, ackCount, op24Index, ackIndex)
	}

	replay := &bufferConn{}
	session.conn = replay
	if err := service.handleGameUpper(session, request); err != nil {
		t.Fatalf("repeated completed op42 after retirement: %v", err)
	}
	if replay.write.Len() != 0 {
		t.Fatalf("repeated completed op42 after retirement wrote=%x", replay.write.Bytes())
	}
	if count := completedReturnAckCountForTest(t, replay.write.Bytes(), 1, uint16(dnfenum.CmdPacketGiveupGame)); count != 0 {
		t.Fatalf("repeated completed op42 after retirement emitted %d acknowledgements", count)
	}
}

func TestCompletedDirectOp42PreOp24OutputFailureRetainsOwnerAndRetryAcks(t *testing.T) {
	service, runtime, session, connection, _ := prepareCompletedSettlementRuntime(t)
	completeSettlementExitBarriersForTest(t, service, runtime, session)
	connection.write.Reset()
	failing := &completedReturnAckOp24FailureConn{
		bufferConn: connection,
		t:          t,
		err:        fmt.Errorf("completed op24 write failed: %w", io.ErrUnexpectedEOF),
		failOnce:   true,
	}
	session.conn = failing
	request, err := dnfproto.BuildChannelPacket(
		uint16(dnfenum.CmdPacketGiveupGame), nil, 0, dnfproto.DefaultChannelClassification,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.handleGameUpper(session, request); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("completed direct op42 pre-op24 failure=%v want io.ErrUnexpectedEOF", err)
	}
	if session.dungeon.runtime != runtime || !runtime.townReturnPending || runtime.townReturnOp24Sent ||
		runtime.townReturnAckSent || runtime.townReturnRequestMsgID != uint16(dnfenum.CmdPacketGiveupGame) {
		t.Fatalf("pre-op24 failure owner=%p state=%+v", session.dungeon.runtime, runtime)
	}
	if failing.failedOp24Attempts != 1 || failing.op24Writes != 0 {
		t.Fatalf("pre-op24 failure attempts=%d successful=%d", failing.failedOp24Attempts, failing.op24Writes)
	}
	if count := completedReturnAckCountForTest(t, connection.write.Bytes(), 1, uint16(dnfenum.CmdPacketGiveupGame)); count != 0 {
		t.Fatalf("pre-op24 failure emitted %d acknowledgements", count)
	}

	if err := service.handleGameUpper(session, request); err != nil {
		t.Fatalf("retry completed direct op42 after pre-op24 failure: %v", err)
	}
	if session.dungeon.runtime != nil || !runtime.townReturnOp24Sent || !runtime.townReturnAckSent {
		t.Fatalf("pre-op24 retry owner=%p state=%+v", session.dungeon.runtime, runtime)
	}
	packets := completedReturnAckPacketsForTest(t, connection.write.Bytes())
	op24Index := -1
	ackIndex := -1
	op24Count := 0
	ackCount := 0
	for index, packet := range packets {
		if packet.classification == 0 && packet.msgID == currentSceneTransitionMsgID {
			op24Count++
			op24Index = index
		}
		if packet.classification == 1 && packet.msgID == uint16(dnfenum.CmdPacketGiveupGame) {
			ackCount++
			ackIndex = index
			if !bytes.Equal(packet.body, []byte{1}) {
				t.Fatalf("pre-op24 retry acknowledgement body=%x want=01", packet.body)
			}
		}
	}
	if op24Count != 1 || ackCount != 1 || ackIndex <= op24Index {
		t.Fatalf("pre-op24 retry order op24=%d ack=%d op24_index=%d ack_index=%d",
			op24Count, ackCount, op24Index, ackIndex)
	}
	if failing.writeCalls != failing.acceptedWrites+failing.failedWrites ||
		failing.acceptedBytes != connection.write.Len() {
		t.Fatalf("pre-op24 write accounting calls=%d accepted=%d failed=%d bytes=%d buffer=%d",
			failing.writeCalls, failing.acceptedWrites, failing.failedWrites,
			failing.acceptedBytes, connection.write.Len())
	}
}

func TestCompletedOp42AckFailureRetainsRouteForAckOnlyRetry(t *testing.T) {
	for _, retryRequestMsgID := range []uint16{
		uint16(dnfenum.CmdPacketGiveupGame),
		uint16(dnfenum.CmdPacketEplpCommand),
		0,
	} {
		t.Run(fmt.Sprintf("retry_current_arg_%d", retryRequestMsgID), func(t *testing.T) {
			service, runtime, session, connection, transition := prepareCompletedReturnAckOrdinaryRouteForTest(t)
			assets := seedCompletedReturnAckAssetForTest(t, service)
			failing := &completedReturnAckFailureConn{
				bufferConn: connection,
				t:          t,
				service:    service,
				session:    session,
				runtime:    runtime,
				assets:     assets,
				err:        fmt.Errorf("completed op42 acknowledgement write failed: %w", io.ErrUnexpectedEOF),
			}
			session.conn = failing
			err := service.sendCurrentCompletedDungeonReturnToTown(
				session,
				runtime,
				transition,
				uint16(dnfenum.CmdPacketGiveupGame),
				"completed_return_ack_failure",
			)
			if !errors.Is(err, io.ErrUnexpectedEOF) {
				t.Fatalf("completed op42 acknowledgement failure=%v want io.ErrUnexpectedEOF", err)
			}
			if session.dungeon.runtime != runtime || !runtime.townReturnPending ||
				!runtime.townReturnOp24Sent || runtime.townReturnAckSent ||
				runtime.townReturnRequestMsgID != uint16(dnfenum.CmdPacketGiveupGame) {
				t.Fatalf("ack failure owner=%p state=%+v", session.dungeon.runtime, runtime)
			}
			if status := runtime.Session.Snapshot().Run.Status; status != worldmap.DungeonRunCompleted {
				t.Fatalf("ack failure status=%s want=%s", status, worldmap.DungeonRunCompleted)
			}
			if failing.ackAttempts != 1 || failing.failedAckWrites != 1 {
				t.Fatalf("ack failure attempts=%d failed=%d", failing.ackAttempts, failing.failedAckWrites)
			}
			if count := completedReturnAckCountForTest(t, connection.write.Bytes(), 1, uint16(dnfenum.CmdPacketGiveupGame)); count != 0 {
				t.Fatalf("failed acknowledgement was accepted %d times", count)
			}
			if count := completedReturnAckCountForTest(t, connection.write.Bytes(), 0, currentSceneTransitionMsgID); count != 1 {
				t.Fatalf("ack failure route retained %d typed op24 packets; want one", count)
			}
			if failing.writeCalls != failing.acceptedWrites+failing.failedWrites ||
				failing.acceptedBytes != connection.write.Len() {
				t.Fatalf("ack failure write accounting calls=%d accepted=%d failed=%d bytes=%d buffer=%d",
					failing.writeCalls, failing.acceptedWrites, failing.failedWrites,
					failing.acceptedBytes, connection.write.Len())
			}

			retry := &bufferConn{}
			session.conn = retry
			if err := service.sendCurrentCompletedDungeonReturnToTown(
				session,
				runtime,
				transition,
				retryRequestMsgID,
				"completed_return_ack_retry",
			); err != nil {
				t.Fatalf("retry completed op42 acknowledgement: %v", err)
			}
			packets := completedReturnAckPacketsForTest(t, retry.write.Bytes())
			if len(packets) != 1 {
				t.Fatalf("ack-only retry packets=%+v; want one", packets)
			}
			if packets[0].classification != 1 ||
				packets[0].msgID != uint16(dnfenum.CmdPacketGiveupGame) ||
				!bytes.Equal(packets[0].body, []byte{1}) {
				t.Fatalf("ack-only retry packet=%+v body=%x", packets[0], packets[0].body)
			}
			if runtime.townReturnRequestMsgID != uint16(dnfenum.CmdPacketGiveupGame) ||
				!runtime.townReturnAckSent || session.dungeon.runtime != nil {
				t.Fatalf("ack retry owner=%p stored_request=%d ack=%t",
					session.dungeon.runtime, runtime.townReturnRequestMsgID, runtime.townReturnAckSent)
			}
		})
	}
}

func TestCompletedStoredNonOp42OwnerNeverAcknowledgesLaterOp42Caller(t *testing.T) {
	for _, initialRequestMsgID := range []uint16{
		uint16(dnfenum.CmdPacketEplpCommand),
		0,
	} {
		t.Run(fmt.Sprintf("stored_arg_%d", initialRequestMsgID), func(t *testing.T) {
			service, runtime, session, connection, transition := prepareCompletedReturnAckOrdinaryRouteForTest(t)
			failing := &completedReturnAckOp24FailureConn{
				bufferConn: connection,
				t:          t,
				err:        fmt.Errorf("completed route prefix write failed: %w", io.ErrUnexpectedEOF),
				failOnce:   true,
			}
			session.conn = failing
			if err := service.sendCurrentCompletedDungeonReturnToTown(
				session,
				runtime,
				transition,
				initialRequestMsgID,
				"completed_return_stored_owner_failure",
			); !errors.Is(err, io.ErrUnexpectedEOF) {
				t.Fatalf("stored owner prefix failure=%v want io.ErrUnexpectedEOF", err)
			}
			if session.dungeon.runtime != runtime || !runtime.townReturnPending ||
				runtime.townReturnOp24Sent || runtime.townReturnAckSent ||
				runtime.townReturnRequestMsgID != initialRequestMsgID {
				t.Fatalf("stored owner failure owner=%p state=%+v", session.dungeon.runtime, runtime)
			}
			if count := completedReturnAckCountForTest(t, connection.write.Bytes(), 1, uint16(dnfenum.CmdPacketGiveupGame)); count != 0 {
				t.Fatalf("stored non-op42 failure emitted %d class1/op42 acknowledgements", count)
			}

			retry := &bufferConn{}
			session.conn = retry
			if err := service.sendCurrentCompletedDungeonReturnToTown(
				session,
				runtime,
				transition,
				uint16(dnfenum.CmdPacketGiveupGame),
				"completed_return_stored_owner_retry_as_op42",
			); err != nil {
				t.Fatalf("retry stored non-op42 owner: %v", err)
			}
			if count := completedReturnAckCountForTest(t, retry.write.Bytes(), 1, uint16(dnfenum.CmdPacketGiveupGame)); count != 0 {
				t.Fatalf("stored request %d incorrectly acknowledged later op42 caller %d times", initialRequestMsgID, count)
			}
			if count := completedReturnAckCountForTest(t, retry.write.Bytes(), 0, currentSceneTransitionMsgID); count != 1 {
				t.Fatalf("stored owner retry typed op24 count=%d want=1", count)
			}
			if session.dungeon.runtime != nil || runtime.townReturnRequestMsgID != initialRequestMsgID ||
				runtime.townReturnAckSent || !runtime.townReturnOp24Sent {
				t.Fatalf("stored owner retry owner=%p state=%+v", session.dungeon.runtime, runtime)
			}
		})
	}
}

func TestCompletedReturnAckResetRearmsOneShotGuard(t *testing.T) {
	runtime := &runtimeDungeonState{
		townReturnPending:      true,
		townReturnOp24Sent:     true,
		townReturnRequestMsgID: uint16(dnfenum.CmdPacketGiveupGame),
		townReturnAckSent:      true,
		townReturnSource:       "completed_return_ack_reset_test",
		townReturnTransition: currentDungeonTownTransition{
			TownID: 7,
			AreaID: 3,
			Body:   []byte{1, 2, 3},
		},
	}
	resetCurrentDungeonReturnAttempt(runtime)
	if runtime.townReturnPending || runtime.townReturnOp24Sent || runtime.townReturnAckSent ||
		runtime.townReturnRequestMsgID != 0 || runtime.townReturnSource != "" ||
		len(runtime.townReturnTransition.Body) != 0 {
		t.Fatalf("return attempt reset left state=%+v", runtime)
	}

	service := &Service{options: options{gameUpperBodyCodec: gameUpperBodyCodecPlain}}
	connection := &bufferConn{}
	session := &gameSession{
		conn:                connection,
		selectedCharacterID: 99,
		dungeon:             dungeonSessionState{runtime: runtime},
	}
	runtime.townReturnPending = true
	runtime.townReturnOp24Sent = true
	runtime.townReturnRequestMsgID = uint16(dnfenum.CmdPacketGiveupGame)
	session.dungeon.mu.Lock()
	err := service.acknowledgeCurrentDungeonGiveupReturnLocked(session, runtime)
	session.dungeon.mu.Unlock()
	if err != nil {
		t.Fatalf("acknowledge after reset: %v", err)
	}
	packets := completedReturnAckPacketsForTest(t, connection.write.Bytes())
	if len(packets) != 1 || packets[0].classification != 1 ||
		packets[0].msgID != uint16(dnfenum.CmdPacketGiveupGame) ||
		!bytes.Equal(packets[0].body, []byte{1}) || !runtime.townReturnAckSent {
		t.Fatalf("acknowledgement after reset packets=%+v state=%+v", packets, runtime)
	}
	before := connection.write.Len()
	session.dungeon.mu.Lock()
	err = service.acknowledgeCurrentDungeonGiveupReturnLocked(session, runtime)
	session.dungeon.mu.Unlock()
	if err != nil || connection.write.Len() != before {
		t.Fatalf("repeated acknowledgement after reset err=%v wrote=%x", err, connection.write.Bytes()[before:])
	}
}

func prepareCompletedReturnAckOrdinaryRouteForTest(
	t *testing.T,
) (*Service, *runtimeDungeonState, *gameSession, *bufferConn, currentDungeonTownTransition) {
	t.Helper()
	service, runtime, session, connection := prepareOrdinaryCompletedSettlementRuntimeForExitTest(t)
	completeSettlementExitBarriersForTest(t, service, runtime, session)
	connection.write.Reset()
	transition, err := service.prepareCurrentDungeonTownTransition(
		context.Background(),
		session.selectedCharacterID,
	)
	if err != nil {
		t.Fatalf("prepare completed return acknowledgement route: %v", err)
	}
	return service, runtime, session, connection, transition
}
