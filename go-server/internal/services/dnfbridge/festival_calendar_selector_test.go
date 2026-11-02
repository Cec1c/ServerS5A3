package dnfbridge

import (
	"bytes"
	"fmt"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/festival"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
)

func festivalSelectorFixture() (*Service, *gameSession, *bufferConn) {
	periods := &festival.Catalog{Periods: map[uint32]festival.Period{}}
	for _, id := range privateFestivalCalendarPeriods {
		periods.Periods[id] = festival.Period{ID: id}
	}
	queue := newFakeCurrentDungeonDeathTimerQueue()
	queue.now = time.Date(2026, 11, 3, 4, 0, 0, 0, time.UTC)
	conn := &bufferConn{}
	svc := &Service{
		gameplayTimers:  queue,
		ceraShopCatalog: &pvfCeraShopCatalog{festivals: periods},
		spendTimeCatalogState: &currentSpendTimeRuntimeCatalog{
			rewardItemIDs: currentSpendTimeJoustCatalogGoldenRewards, totalStageSeconds: 10800,
		},
	}
	return svc, &gameSession{conn: conn}, conn
}

func festivalSelectorWire(t *testing.T, conn *bufferConn) currentSpendTimeCatalogShape {
	t.Helper()
	packet, _ := splitGameServerUpperPacketWithHeader(t, conn.write.Bytes(), dnfproto.GameServerUpperHeaderSize16)
	if packet.Header.Classification != 0 || packet.Header.MsgID != currentSpendTimeEventInfoMsgID {
		t.Fatalf("unexpected descriptor header: %+v", packet.Header)
	}
	body, err := zlibDecompress(packet.Body)
	if err != nil {
		t.Fatal(err)
	}
	return parseCurrentSpendTimeEventInfoBody(t, body)
}

func requireFestivalSelectorRows(t *testing.T, shape currentSpendTimeCatalogShape) {
	t.Helper()
	for i, period := range privateFestivalCalendarPeriods {
		id := uint16(60001 + i)
		base, active := 0, 0
		for _, row := range shape.baseRows {
			if row.eventID == id {
				base++
				if !row.hasExtended || row.route != fmt.Sprintf("UI/Event/PrivateFestival005/%d.xui/0/0", period) {
					t.Fatalf("period %d lost its calendar route", period)
				}
			}
		}
		for _, row := range shape.activeRows {
			if row.eventID == id {
				active++
			}
		}
		if base != 1 || active != 1 {
			t.Fatalf("festival %d descriptor/active rows=%d/%d, want 1/1", id, base, active)
		}
	}
	if !shape.hasEvent(181) || !shape.hasEvent(uint16(currentSeriaLuckEventID)) {
		t.Fatal("unrelated activities disappeared")
	}
}

func TestFestivalCalendarSelectorBeforeTownPreservesGiftPages(t *testing.T) {
	svc, session, conn := festivalSelectorFixture()
	if err := svc.sendCurrentPortedSelectorCatalog(session); err != nil {
		t.Fatal(err)
	}
	requireFestivalSelectorRows(t, festivalSelectorWire(t, conn))
	if !session.spendTime.eventInfoSent {
		t.Fatal("successful selector descriptor was not committed")
	}
	before := append([]byte(nil), conn.write.Bytes()...)
	if err := svc.sendCurrentPortedSelectorCatalog(session); err != nil || !bytes.Equal(before, conn.write.Bytes()) {
		t.Fatal("selector deduplication changed", err)
	}
	send, ready := svc.beginCurrentSpendTimeEventInfo(session)
	if send || !ready {
		t.Fatal("town should reuse the already published complete descriptor")
	}
	conn.write.Reset()
	if sent, err := svc.sendCurrentSpendTimeProtocolState(session, svc.spendTimeCatalogState, 0, 0, send); err != nil || sent {
		t.Fatalf("town sent=%v err=%v", sent, err)
	}
}

func TestFestivalCalendarTownRefreshContainsEachGiftOnce(t *testing.T) {
	svc, session, conn := festivalSelectorFixture()
	for attempt := 0; attempt < 2; attempt++ {
		conn.write.Reset()
		if sent, err := svc.sendCurrentSpendTimeProtocolState(session, svc.spendTimeCatalogState, 0, 0, true); err != nil || !sent {
			t.Fatalf("refresh %d sent=%v err=%v", attempt, sent, err)
		}
		requireFestivalSelectorRows(t, festivalSelectorWire(t, conn))
	}
}

func TestFestivalCalendarSelectorFailedCompositionRemainsRetryable(t *testing.T) {
	svc, session, conn := festivalSelectorFixture()
	last := privateFestivalCalendarPeriods[5]
	delete(svc.ceraShopCatalog.festivals.Periods, last)
	if err := svc.sendCurrentPortedSelectorCatalog(session); err == nil {
		t.Fatal("incomplete configured festival was published as a complete catalog")
	}
	if conn.write.Len() != 0 || session.spendTime.eventInfoSent {
		t.Fatal("failed composition wrote or committed a descriptor")
	}
	svc.ceraShopCatalog.festivals.Periods[last] = festival.Period{ID: last}
	if err := svc.sendCurrentPortedSelectorCatalog(session); err != nil {
		t.Fatal(err)
	}
	requireFestivalSelectorRows(t, festivalSelectorWire(t, conn))
}

func TestFestivalCalendarSelectorWithoutConfiguredFestival(t *testing.T) {
	svc, session, conn := festivalSelectorFixture()
	svc.ceraShopCatalog.festivals = nil
	if err := svc.sendCurrentPortedSelectorCatalog(session); err != nil {
		t.Fatal(err)
	}
	shape := festivalSelectorWire(t, conn)
	for id := uint16(60001); id <= 60006; id++ {
		if shape.hasEvent(id) {
			t.Fatalf("unconfigured festival %d was invented", id)
		}
	}
	if !shape.hasEvent(181) || !shape.hasEvent(uint16(currentSeriaLuckEventID)) {
		t.Fatal("legacy activities disappeared")
	}
}
