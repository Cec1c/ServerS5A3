package dnfbridge

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	dnfjoust "longheng.io/server/internal/modules/dnf/joust"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
)

func TestSendCurrentSpendTimeProtocolStateOmitsUnavailableJoustAndContinues(t *testing.T) {
	validRules := testCurrentJoustPVFText()
	incompatibleRules := strings.Replace(validRules,
		"[min level]\n50\n[max betting]\n10000\n[reward]\n490005585\n"+
			"[betting reward]\n490005593\n[material]\n490005585 490005586\n[/material]\n",
		"[min level]\n50\n[max betting]\n1000\n[reward]\n490009181\n"+
			"[betting reward]\n490009180\n[material]\n490009181 490009182\n[/material]\n",
		1,
	)
	if incompatibleRules == validRules {
		t.Fatal("incompatible-rules fixture did not replace the event rule header")
	}

	tests := []struct {
		name       string
		eventRules string
	}{
		{name: "incompatible rules", eventRules: incompatibleRules},
		{name: "missing event document"},
		{name: "malformed event document", eventRules: "[min level]\n50\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source := bridgePVFSource{}
			if test.eventRules != "" {
				source[dnfjoust.EventPVFPath] = test.eventRules
			}
			if _, err := dnfjoust.LoadCatalog(context.Background(), source); err == nil {
				t.Fatal("real LoadCatalog accepted the unavailable fixture")
			}

			logPath := filepath.Join(t.TempDir(), "packet.log")
			logger, err := openPacketLogger(logPath)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = logger.Close() })
			queue := newFakeCurrentDungeonDeathTimerQueue()
			queue.now = time.Unix(7200*321, 0).UTC().Add(10 * time.Minute)
			connection := &bufferConn{}
			service := &Service{
				pvfItemCatalog: &pvfDungeonDropCatalog{source: source},
				gameplayTimers: queue,
				packetLog:      logger,
			}
			session := &gameSession{conn: connection, connID: "joust-catalog-login-unavailable"}
			spendTime := &currentSpendTimeRuntimeCatalog{
				rewardItemIDs:     currentSpendTimeJoustCatalogGoldenRewards,
				totalStageSeconds: 10800,
			}

			for attempt := 0; attempt < 2; attempt++ {
				sent, sendErr := service.sendCurrentSpendTimeProtocolState(
					session,
					spendTime,
					7199,
					2,
					true,
				)
				if sendErr != nil {
					t.Fatalf("town initialization attempt %d failed: %v", attempt+1, sendErr)
				}
				if !sent {
					t.Fatalf("town initialization attempt %d omitted op108", attempt+1)
				}
			}

			rest := connection.write.Bytes()
			for attempt := 0; attempt < 2; attempt++ {
				descriptor, trailing := splitGameServerUpperPacketWithHeader(
					t,
					rest,
					dnfproto.GameServerUpperHeaderSize16,
				)
				if descriptor.Header.Classification != 0 ||
					descriptor.Header.MsgID != currentSpendTimeEventInfoMsgID {
					t.Fatalf("attempt %d initial descriptor header=%+v", attempt+1, descriptor.Header)
				}
				body, err := zlibDecompress(descriptor.Body)
				if err != nil {
					t.Fatalf("decompress attempt %d op108: %v", attempt+1, err)
				}
				shape := parseCurrentSpendTimeEventInfoBody(t, body)
				if shape.hasEvent(uint16(currentJoustEventID)) {
					t.Fatalf("attempt %d op108 still published joust event 2365", attempt+1)
				}
				if !shape.hasEvent(181) || !shape.hasEvent(uint16(currentSeriaLuckEventID)) {
					t.Fatalf("attempt %d op108 lost unrelated activity rows: base=%+v active=%+v",
						attempt+1, shape.baseRows, shape.activeRows)
				}

				progress, trailing := splitGameServerUpperPacket(t, trailing)
				if progress.Header.Classification != 0 ||
					progress.Header.MsgID != currentSpendTimeProgressMsgID {
					t.Fatalf("attempt %d progress header=%+v", attempt+1, progress.Header)
				}
				rest = trailing
			}
			if len(rest) != 0 {
				t.Fatalf("unavailable joust emitted push packets after op108/progress: %x", rest)
			}

			if err := logger.Close(); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(logPath)
			if err != nil {
				t.Fatal(err)
			}
			logged := string(data)
			if !strings.Contains(logged, "kind=game-joust-catalog-unavailable") ||
				!strings.Contains(logged, "event_id=2365") {
				t.Fatalf("unavailable catalog log=%q", logged)
			}
		})
	}
}

func TestCurrentJoustBoundaryStopsBeforeStateWhenCatalogUnavailable(t *testing.T) {
	source := bridgePVFSource{
		dnfjoust.EventPVFPath: strings.Replace(testCurrentJoustPVFText(),
			"[max betting]\n10000", "[max betting]\n1000", 1),
	}
	queue := newFakeCurrentDungeonDeathTimerQueue()
	queue.now = time.Unix(7200*321, 0).UTC().Add(10 * time.Minute)
	connection := &bufferConn{}
	service := &Service{
		pvfItemCatalog: &pvfDungeonDropCatalog{source: source},
		gameplayTimers: queue,
	}
	session := &gameSession{conn: connection, connID: "joust-catalog-boundary", selectedCharacterID: 19}
	service.gameSessions = map[uint16]*gameSession{19: session}

	service.handleCurrentJoustBoundary(queue.Now())

	if packets := connection.write.Bytes(); len(packets) != 0 {
		t.Fatalf("unavailable catalog boundary emitted packets: %x", packets)
	}
}

func TestCurrentJoustSendErrorsStillPropagateWithoutCatalogFallback(t *testing.T) {
	for _, failAt := range []int{3, 4} {
		t.Run([]string{"op1240", "op1241"}[failAt-3], func(t *testing.T) {
			sentinel := errors.New("joust protocol write failed")
			connection := &failNthDungeonWriteConn{failAt: failAt, err: sentinel}
			queue := newFakeCurrentDungeonDeathTimerQueue()
			queue.now = time.Unix(7200*321, 0).UTC().Add(10 * time.Minute)
			service := &Service{
				pvfItemCatalog: &pvfDungeonDropCatalog{source: bridgePVFSource{
					dnfjoust.EventPVFPath: testCurrentJoustPVFText(),
				}},
				gameplayTimers: queue,
			}
			session := &gameSession{conn: connection, connID: "joust-catalog-send-error"}
			spendTime := &currentSpendTimeRuntimeCatalog{
				rewardItemIDs:     currentSpendTimeJoustCatalogGoldenRewards,
				totalStageSeconds: 10800,
			}

			sent, err := service.sendCurrentSpendTimeProtocolState(session, spendTime, 7199, 2, true)
			if !sent {
				t.Fatal("op108 was not reported before the injected joust write failure")
			}
			if !errors.Is(err, sentinel) {
				t.Fatalf("send error=%v want=%v", err, sentinel)
			}
		})
	}
}
