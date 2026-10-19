package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"os"
	"strconv"
	"testing"
	"time"

	oe "longheng.io/server/internal/modules/dnf/onlineevent"
	proto "longheng.io/server/internal/modules/dnf/protocol"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestOnlinePanelCatalogPreservesOtherActivitiesAndLegacyShape(t *testing.T) {
	ids := currentSpendTimeJoustCatalogGoldenRewards
	old, err := buildCurrentActivityEventInfoBodyWithEpicRoad(ids, 10800, false, false, true, true, true, 2418, true, true, true, true, true)
	if err != nil {
		t.Fatal(err)
	}
	native, err := buildCurrentActivityEventInfoBodyWithOnlinePanel(ids, 10800, false, false, true, true, true, 2418, true, true, true, true, true, true)
	if err != nil {
		t.Fatal(err)
	}
	a, b := parseCurrentSpendTimeEventInfoBody(t, old), parseCurrentSpendTimeEventInfoBody(t, native)
	if !b.hasEvent(228) || b.hasEvent(2347) || b.hasEvent(2348) || a.baseCount != b.baseCount || a.activeCount != b.activeCount {
		t.Fatalf("native shape=%+v", b)
	}
	row := b.baseRows[0]
	if row.eventID != 228 || !row.hasExtended || row.route != currentSpendTimeEventRoute {
		t.Fatalf("native online activity has no visible click route: %+v", row)
	}
	reader := &currentSpendTimeCatalogReader{body: row.raw}
	reader.take(t, 6)
	reader.dstr(t)
	reader.dstr(t)
	if reader.byteValue(t) != 1 || reader.byteValue(t) != 0 || reader.byteValue(t) != 4 || len(reader.dstr(t)) == 0 {
		t.Fatal("native online activity has no regular title/tag")
	}
	if row.raw[len(row.raw)-1] != 1 {
		t.Fatal("native online activity is hidden from list")
	}
	for _, row := range a.baseRows {
		if row.eventID == 2347 {
			continue
		}
		found := false
		for _, other := range b.baseRows {
			if row.eventID == other.eventID {
				found = bytes.Equal(row.raw, other.raw)
			}
		}
		if !found {
			t.Fatalf("unrelated base activity %d changed", row.eventID)
		}
	}
	for _, row := range a.activeRows {
		if row.eventID == 2347 {
			continue
		}
		found := false
		for _, other := range b.activeRows {
			if row.eventID == other.eventID {
				found = bytes.Equal(row.raw, other.raw)
			}
		}
		if !found {
			t.Fatalf("unrelated active activity %d changed", row.eventID)
		}
	}
	legacy, err := buildCurrentActivityEventInfoBodyWithOnlinePanel(ids, 10800, false, false, true, true, true, 2418, true, true, true, true, true, false)
	if err != nil || !bytes.Equal(old, legacy) {
		t.Fatal("legacy payload changed")
	}
}

func TestPCRoomWireStateAndStrictNativeRequest(t *testing.T) {
	body := buildCurrentPCRoomTimePointBody(oe.PCRoomTimePointSnapshot{Seconds: 7200, Total: 17, DailyMask: 5, PeriodMask: 3})
	if len(body) != 17 || binary.LittleEndian.Uint32(body) != 7200 || binary.LittleEndian.Uint32(body[4:]) != 17 || body[8] != 5 || binary.LittleEndian.Uint32(body[9:]) != 0 || binary.LittleEndian.Uint32(body[13:]) != 3 {
		t.Fatalf("state=%x", body)
	}
	for _, raw := range [][]byte{{0, 255}, {1, 255}, {2, 255}, {4, 255}, {8, 255}, {16, 0}, {16, 1}, {16, 2}, {16, 3}} {
		if _, _, err := parseCurrentPCRoomTimePointRequest(raw); err != nil {
			t.Fatalf("native request %x: %v", raw, err)
		}
	}
	for _, raw := range [][]byte{nil, {0}, {0, 255, 0}, {3, 255}, {15, 255}, {16, 4}, {1, 0}, {0, 0}} {
		if _, _, err := parseCurrentPCRoomTimePointRequest(raw); err == nil {
			t.Fatalf("invalid request accepted: %x", raw)
		}
	}
}

func TestCurrentSpendTimeCatalogDoesNotMaskCachedPCRoomFailure(t *testing.T) {
	want := errors.New("corrupt pcroom catalog")
	s := &Service{
		spendTimeCatalogState:   &currentSpendTimeRuntimeCatalog{definition: oe.Definition{ID: "2347"}},
		spendTimeCatalogLoadErr: want,
	}
	catalog, available, err := s.currentSpendTimeRuntimeCatalogIfAvailable(context.Background())
	if catalog != nil || !available || !errors.Is(err, want) {
		t.Fatalf("cached failure catalog=%+v available=%v err=%v", catalog, available, err)
	}
}

func onlinePanelFixture(t *testing.T) (*Service, *gameSession, *bufferConn, *currentSpendTimeRuntimeCatalog, repo.Group) {
	t.Helper()
	g := memory.NewMemoryGroup()
	for _, err := range []error{
		g.Account.Save(nil, repo.AccountRecord{AccountID: "a"}),
		g.Character.Save(nil, repo.CharacterRecord{CharacterID: "1", AccountID: "a"}),
		g.Inventory.Save(nil, repo.InventoryRecord{CharacterID: "1", Slots: map[string]repo.ItemStack{}}),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	c := &currentSpendTimeRuntimeCatalog{definition: oe.Definition{ID: "2347"}, totalStageSeconds: 10800,
		nativePCRoom: &oe.PCRoomTimePointCatalog{Seconds: [4]uint32{1800, 3600, 7200, 10800}, Counts: [4]uint32{5, 10, 15, 20}},
	}
	for i, seconds := range c.nativePCRoom.Seconds {
		c.rewardItemIDs = append(c.rewardItemIDs, 42)
		c.definition.Stages = append(c.definition.Stages, oe.Stage{ID: "stage-" + strconv.Itoa(i+1), RequiredSeconds: uint64(seconds), Items: []oe.ItemReward{{ItemID: 42, Count: 1}}})
		c.nativePCRoom.Daily[i] = oe.AttendancePVFItemReward{ItemID: 42, Count: 1}
		c.nativePCRoom.Period[i] = oe.AttendancePVFItemReward{ItemID: 42, Count: 1}
	}
	queue := newFakeCurrentDungeonDeathTimerQueue()
	queue.now = time.Date(2026, 10, 19, 12, 0, 0, 0, oe.ChinaCalendar)
	s := &Service{options: options{currentClientVersion: "official-24.9", joustEventDisabled: true}, gameplayTimers: queue,
		repositoryProvider: func() (repo.Group, bool) { return g, true }, spendTimeCatalogState: c,
		pvfItemCatalog: &pvfDungeonDropCatalog{source: bridgePVFSource{}, itemCache: map[uint32]dungeonDropItemDefinition{42: {ItemID: 42, Kind: dungeonDropItemStackable, StackLimit: 1000, SlotStart: 9, SlotEnd: 56}}},
	}
	conn := &bufferConn{}
	session := &gameSession{conn: conn, connID: "online-native", accountID: "a", selectedCharacterID: 1, loginPacketSeen: true, accountFromToken: true}
	s.bindGameSessionCharacter(session, 1)
	o, _ := oe.NewOwner(g)
	if _, err := o.Observe(context.Background(), oe.ObserveCommand{AccountID: "a", CharacterID: "1", Definition: c.definition, IntervalFrom: queue.now.Add(-time.Hour), IntervalTo: queue.now}); err != nil {
		t.Fatal(err)
	}
	return s, session, conn, c, g
}

func TestOnlinePanelInitialPairManualMailAndBothDispatchers(t *testing.T) {
	s, a, conn, _, g := onlinePanelFixture(t)
	if err := s.sendCurrentSpendTimeInitialStateOnce(a, "native_test"); err != nil {
		t.Fatal(err)
	}
	descriptor, rest := splitGameServerUpperPacketWithHeader(t, conn.write.Bytes(), proto.GameServerUpperHeaderSize16)
	if descriptor.Header.MsgID != 108 {
		t.Fatalf("first=%d", descriptor.Header.MsgID)
	}
	plain, err := zlibDecompress(descriptor.Body)
	if err != nil {
		t.Fatal(err)
	}
	shape := parseCurrentSpendTimeEventInfoBody(t, plain)
	if !shape.hasEvent(228) || shape.hasEvent(2347) {
		t.Fatal("wrong UI owner")
	}
	state, _ := splitGameServerUpperPacket(t, rest)
	if state.Header.MsgID != 561 || len(state.Body) != 17 || binary.LittleEndian.Uint32(state.Body) != 3600 || state.Body[8] != 0 {
		t.Fatalf("initial state=%+v", state)
	}
	mail, _, _ := g.Mailbox.Load(nil, "1")
	if len(mail.Mails) != 0 {
		t.Fatal("initialization auto-granted native manual rewards")
	}
	conn.write.Reset()
	if err := s.handleGameCommand(a, 1, 632, []byte{1, 255}); err != nil {
		t.Fatal(err)
	}
	state, _ = splitGameServerUpperPacket(t, conn.write.Bytes())
	if state.Header.MsgID != 561 || state.Body[8] != 1 {
		t.Fatal("legacy claim projection")
	}
	frame, err := proto.BuildChannelPacket(632, []byte{2, 255}, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.handleGameUpper(a, frame); err != nil {
		t.Fatal(err)
	}
	if err := s.handleGameCommand(a, 1, 632, []byte{1, 255}); err != nil {
		t.Fatal(err)
	}
	if err := s.handleGameUpper(a, frame); err != nil {
		t.Fatal(err)
	}
	mail, _, _ = g.Mailbox.Load(nil, "1")
	if len(mail.Mails) != 2 {
		t.Fatalf("duplicate or missing mail: %d", len(mail.Mails))
	}
	for _, m := range mail.Mails {
		if len(m.Attachments) != 1 || m.Attachments[0].ItemID != 42 || m.Attachments[0].Count != 1 {
			t.Fatalf("mail=%+v", m)
		}
	}
	a.characterGeneration++
	before := conn.write.Len()
	if handled, err := s.handleCurrentPCRoomTimePoint(a, 632, 1, []byte{4, 255}); !handled || err != nil {
		t.Fatal(err)
	}
	if conn.write.Len() != before {
		t.Fatal("stale generation wrote state")
	}
}

func TestOnlinePanelRealPVFSelectsActualFourPlusFourEdition(t *testing.T) {
	p := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if p == "" {
		t.Skip("requires actual deployed PVF")
	}
	a, err := platformpvf.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	s := &Service{initialEquipmentArchive: a}
	c, ok, err := s.currentSpendTimeRuntimeCatalogIfAvailable(context.Background())
	if err != nil || !ok || c == nil || c.nativePCRoom == nil {
		t.Fatalf("native catalog available=%v err=%v", ok, err)
	}
	if c.nativePCRoom.Counts != [4]uint32{5, 10, 15, 20} || c.nativePCRoom.Daily[2].ItemID != 490009860 || c.nativePCRoom.Daily[2].Count != 5 || c.nativePCRoom.Daily[3].Count != 2 {
		t.Fatalf("wrong actual edition=%+v", c.nativePCRoom)
	}
	if c.cumulative == nil || c.cumulative.Stages[3].RequiredDays != 25 {
		t.Fatal("legacy cutover definition was changed")
	}
	items, err := s.currentPVFItemCatalog()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, oe.ChinaCalendar)
	rewards := append(append([]oe.AttendancePVFItemReward{}, c.nativePCRoom.Daily[:]...), c.nativePCRoom.Period[:]...)
	for _, reward := range rewards {
		definition, err := items.ResolveItem(uint32(reward.ItemID))
		if err != nil {
			t.Fatalf("reward item %d: %v", reward.ItemID, err)
		}
		definition, err = currentPVFItemDefinitionForGrantAt(definition, now)
		if err != nil || definition.Kind != dungeonDropItemStackable || (!definition.ExpirationDate.IsZero() && !definition.ExpirationDate.After(now)) {
			t.Fatalf("reward item %d is not deliverable: %+v err=%v", reward.ItemID, definition, err)
		}
	}
}

func TestPCRoomClockBoundaryCountsFinishedDayBeforeReset(t *testing.T) {
	s, session, conn, c, g := onlinePanelFixture(t)
	c.definition.Boundary = &oe.DailyBoundary{Hour: 6}
	account, _, err := g.Account.Load(context.Background(), "a")
	if err != nil {
		t.Fatal(err)
	}
	account.Metadata = map[string]string{}
	if err := g.Account.Save(context.Background(), account); err != nil {
		t.Fatal(err)
	}
	queue := s.gameplayTimers.(*fakeCurrentDungeonDeathTimerQueue)
	queue.now = time.Date(2026, 10, 20, 3, 0, 0, 0, oe.ChinaCalendar)
	if err := s.sendCurrentSpendTimeInitialStateOnce(session, "boundary_test"); err != nil {
		t.Fatal(err)
	}
	conn.write.Reset()
	queue.now = queue.now.Add(3 * time.Hour)
	s.runCurrentSpendTimeTick(session, session.spendTime.generation)
	state, _ := splitGameServerUpperPacket(t, conn.write.Bytes())
	if state.Header.MsgID != 561 || binary.LittleEndian.Uint32(state.Body) != 0 || binary.LittleEndian.Uint32(state.Body[4:]) != 1 || state.Body[8] != 0 {
		t.Fatalf("06:00 boundary lost completed day or daily reset: %+v", state)
	}
	conn.write.Reset()
	queue.now = queue.now.Add(time.Second)
	s.runCurrentSpendTimeTick(session, session.spendTime.generation)
	state, _ = splitGameServerUpperPacket(t, conn.write.Bytes())
	if state.Header.MsgID != 561 || binary.LittleEndian.Uint32(state.Body) != 1 || binary.LittleEndian.Uint32(state.Body[4:]) != 1 {
		t.Fatalf("completed day counted twice after boundary: %+v", state)
	}
	mailbox, _, err := g.Mailbox.Load(context.Background(), "1")
	if err != nil || len(mailbox.Mails) != 0 {
		t.Fatalf("manual rewards auto-delivered at boundary: %+v err=%v", mailbox, err)
	}
}
