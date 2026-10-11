package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	"testing"
)

// Synthetic700 fixture validates the completed route wire prefix, not real223
// or native selectedactor/camera behavior. No client callback is fabricated.
func TestPreparedCompletedTownLocationPrefixBeforeAreaRoster(t *testing.T) {
	service, session, runtime := newBackToVillageRuntime(t)
	if err := runtime.Session.CompleteCurrentRoom(); err != nil {
		t.Fatal(err)
	}
	transition, err := service.prepareCurrentDungeonTownTransition(context.Background(), session.selectedCharacterID)
	if err != nil {
		t.Fatal(err)
	}
	connection := session.conn.(*bufferConn)
	connection.write.Reset()
	if err := service.sendCurrentCompletedDungeonReturnToTown(session, runtime, transition, uint16(dnfenum.CmdPacketEplpCommand), "prepared_completed_location_prefix"); err != nil {
		t.Fatal(err)
	}
	want22 := buildCurrentTownUserPositionNotificationBody(transition.ActorObjectKey, uint16(transition.PositionX), uint16(transition.PositionY), transition.Direction)
	want23 := buildCurrentTownUserAreaNotificationBody(transition.ActorObjectKey, transition.TownID, transition.AreaID, uint16(transition.PositionX), uint16(transition.PositionY), transition.Direction, transition.AreaState)
	counts := map[uint16]int{}
	order := []uint16{}
	for stream := connection.write.Bytes(); len(stream) > 0; {
		packet, next := splitCurrentGameServerUpperPacketAuto(t, stream)
		stream = next
		if packet.Header.Classification != 0 {
			continue
		}
		id := packet.Header.MsgID
		if id == 22 || id == 23 || id == 24 {
			counts[id]++
			order = append(order, id)
			if id == 22 && !bytes.Equal(packet.Body, want22) {
				t.Fatal("owned op22 body mismatch")
			}
			if id == 23 && !bytes.Equal(packet.Body, want23) {
				t.Fatal("owned op23 body mismatch")
			}
		}
	}
	if counts[22] != 1 || counts[23] != 1 || counts[24] != 1 || len(order) != 3 || order[0] != 22 || order[1] != 23 || order[2] != 24 {
		t.Fatalf("completed town lacks exact owned22/23-before24 prefix: counts=%v order=%v", counts, order)
	}
	if session.initialTownLegacySceneReadyAccepted || containsClass0Op2ModeForTest(t, connection.write.Bytes(), 3) {
		t.Fatal("location prefix fabricated callback or fullmode3")
	}
}

// Failure injection is at the actual per-session socket Write boundary.
// A failed23 retries the already written22 unchanged; the old existing helper
// owns that whole prefix, and no actor generation or callback is replayed.
type preparedCompletedLocationFailureConn struct {
	*bufferConn
	messageID uint16
	failOnce  bool
}

func (c *preparedCompletedLocationFailureConn) Write(body []byte) (int, error) {
	if c.failOnce && len(body) >= 3 && body[0] == 0 && binary.LittleEndian.Uint16(body[1:3]) == c.messageID {
		c.failOnce = false
		return 0, errors.New("prepared owned location socket failure")
	}
	return c.bufferConn.Write(body)
}
func TestPreparedCompletedTownLocationFailureRetries(t *testing.T) {
	for _, msgID := range []uint16{22, 23, 24} {
		name := map[uint16]string{22: "failed22", 23: "failed23", 24: "failed24"}[msgID]
		t.Run(name, func(t *testing.T) {
			service, session, runtime := newBackToVillageRuntime(t)
			if err := runtime.Session.CompleteCurrentRoom(); err != nil {
				t.Fatal(err)
			}
			transition, err := service.prepareCurrentDungeonTownTransition(context.Background(), session.selectedCharacterID)
			if err != nil {
				t.Fatal(err)
			}
			base := session.conn.(*bufferConn)
			base.write.Reset()
			connection := &preparedCompletedLocationFailureConn{bufferConn: base, messageID: msgID, failOnce: true}
			session.conn = connection
			if err := service.sendCurrentCompletedDungeonReturnToTown(session, runtime, transition, uint16(dnfenum.CmdPacketEplpCommand), "prepared_fail_prefix"); err == nil {
				t.Fatal("requested real write failure did not occur")
			}
			if connection.failOnce || session.dungeon.runtime != runtime || session.initialTownLegacySceneReadyAccepted {
				t.Fatal("failed prefix consumed runtime/callback or missed write target")
			}
			if err := service.sendCurrentCompletedDungeonReturnToTown(session, runtime, transition, uint16(dnfenum.CmdPacketEplpCommand), "prepared_retry_prefix"); err != nil {
				t.Fatal(err)
			}
			counts := map[uint16]int{}
			var last uint16
			for stream := base.write.Bytes(); len(stream) > 0; {
				packet, next := splitCurrentGameServerUpperPacketAuto(t, stream)
				stream = next
				if packet.Header.Classification != 0 {
					continue
				}
				id := packet.Header.MsgID
				if id == 22 || id == 23 || id == 24 {
					counts[id]++
					last = id
				}
				if id == 22 && !bytes.Equal(packet.Body, buildCurrentTownUserPositionNotificationBody(transition.ActorObjectKey, uint16(transition.PositionX), uint16(transition.PositionY), transition.Direction)) {
					t.Fatal("retry22 changed owned position")
				}
				if id == 23 && !bytes.Equal(packet.Body, buildCurrentTownUserAreaNotificationBody(transition.ActorObjectKey, transition.TownID, transition.AreaID, uint16(transition.PositionX), uint16(transition.PositionY), transition.Direction, transition.AreaState)) {
					t.Fatal("retry23 changed owned area")
				}
			}
			want22 := 1
			if msgID == 23 {
				want22 = 2
			}
			if counts[22] != want22 || counts[23] != 1 || counts[24] != 1 || last != 24 {
				t.Fatalf("retry prefix counts=%v last=%d", counts, last)
			}
			if session.dungeon.runtime != nil || !session.sceneBootstrapTailDeferred || session.initialTownLegacySceneReadyAccepted || containsClass0Op2ModeForTest(t, base.write.Bytes(), 3) {
				t.Fatal("retry changed completed/cold callback contract")
			}
		})
	}
}
