package dnfbridge

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	dnfjoust "longheng.io/server/internal/modules/dnf/joust"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// This opt-in probe uses an actual archive containing an unsupported joust
// season. Run it in the shared serial PVF window.
func TestRealPVFIncompatibleJoustCatalogKeepsTownSnapshotAvailable(t *testing.T) {
	path := os.Getenv("DNF_JOUST_INCOMPATIBLE_REAL_PVF")
	if path == "" {
		t.Skip("set DNF_JOUST_INCOMPATIBLE_REAL_PVF to an archive with an unsupported joust season")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dnfjoust.LoadCatalog(context.Background(), archive); !errors.Is(err, dnfjoust.ErrPVFMalformed) {
		t.Fatalf("input must reproduce unsupported joust catalog: %v", err)
	} else {
		t.Logf("actual archive catalog error: %v", err)
	}
	queue := newFakeCurrentDungeonDeathTimerQueue()
	queue.now = time.Unix(7200*321, 0).UTC().Add(10 * time.Minute)
	conn := &bufferConn{}
	service := &Service{pvfItemCatalog: &pvfDungeonDropCatalog{source: archive}, gameplayTimers: queue}
	session := &gameSession{conn: conn, connID: "real-joust-unavailable", selectedCharacterID: 19}
	service.gameSessions = map[uint16]*gameSession{19: session}
	catalog := &currentSpendTimeRuntimeCatalog{rewardItemIDs: currentSpendTimeJoustCatalogGoldenRewards, totalStageSeconds: 10800}
	for _, first := range []bool{true, false, true} {
		conn.write.Reset()
		sent, err := service.sendCurrentSpendTimeProtocolState(session, catalog, 0, 0, first)
		if err != nil || sent != first {
			t.Fatalf("descriptor=%t sent=%t err=%v", first, sent, err)
		}
		rest := conn.write.Bytes()
		if first {
			packet, trailing := splitGameServerUpperPacketWithHeader(t, rest, dnfproto.GameServerUpperHeaderSize16)
			if packet.Header.MsgID != currentSpendTimeEventInfoMsgID {
				t.Fatalf("expected op108, got %+v", packet.Header)
			}
			body, err := zlibDecompress(packet.Body)
			if err != nil {
				t.Fatal(err)
			}
			shape := parseCurrentSpendTimeEventInfoBody(t, body)
			if shape.hasEvent(uint16(currentJoustEventID)) || !shape.hasEvent(uint16(currentSeriaLuckEventID)) {
				t.Fatalf("unexpected activity rows: %+v", shape)
			}
			rest = trailing
		}
		progress, rest := splitGameServerUpperPacket(t, rest)
		if progress.Header.MsgID != currentSpendTimeProgressMsgID || len(rest) != 0 {
			t.Fatalf("unexpected progress/pushes: header=%+v trailing=%x", progress.Header, rest)
		}
	}
	conn.write.Reset()
	service.handleCurrentJoustBoundary(queue.Now())
	if conn.write.Len() != 0 {
		t.Fatalf("boundary emitted unsupported joust packets: %x", conn.write.Bytes())
	}
}
