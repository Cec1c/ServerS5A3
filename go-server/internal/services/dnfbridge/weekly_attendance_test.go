package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	onlineevent "longheng.io/server/internal/modules/dnf/onlineevent"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"testing"
	"time"
)

func weeklyBridgeSeed(t *testing.T, s *Service, a *gameSession, n int) {
	t.Helper()
	season, e := s.currentWeeklySeason(a)
	if e != nil {
		t.Fatal(e)
	}
	c, e := s.currentWeeklyCatalog(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	g, _ := s.repositoryGroup()
	account, _, _ := g.Account.Load(nil, s.accountIDForSession(a))
	for i := 0; i < n; i++ {
		if e := onlineevent.RecordWeeklyClear(&account, c, season, s.gameplayNow()); e != nil {
			t.Fatal(e)
		}
	}
	if e := g.Account.Save(nil, account); e != nil {
		t.Fatal(e)
	}
}
func TestWeeklyBridgeActual1113OpaqueAndNative1093Replay(t *testing.T) {
	s, a, b, conn := prepareMonthlyBridge(t)
	weeklyBridgeSeed(t, s, a, 4)
	if e := s.handleGameCommand(a, 1, 1113, bytes.Repeat([]byte{255}, 13)); e != nil {
		t.Fatal(e)
	}
	if monthlyMailCount(t, s, "1") != 1 {
		t.Fatal("no weekly mail")
	}
	ack, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
	if ack.Header.MsgID != 1113 || !bytes.Equal(ack.Body, []byte{1, 0, 0, 0, 0}) {
		t.Fatalf("ack%+v", ack)
	}
	state, _ := splitGameServerUpperPacket(t, rest)
	if state.Header.MsgID != 1093 || len(state.Body) != 459 || binary.LittleEndian.Uint32(state.Body[4:]) != 1 || state.Body[0x19c] != 1 || binary.LittleEndian.Uint32(state.Body[0x1c4:]) != 1 {
		t.Fatalf("state%+v", state)
	}
	g, _ := s.repositoryGroup()
	m, _, _ := g.Mailbox.Load(nil, "1")
	var item repo.MailAttachment
	for _, mail := range m.Mails {
		item = mail.Attachments[0]
	}
	if item.ItemID != 490009970 || item.Count != 1 || !item.ExpireAt.After(s.gameplayNow()) {
		t.Fatalf("actual grant%+v", item)
	}
	conn.write.Reset()
	if e := s.handleGameCommand(a, 1, 1113, make([]byte, 13)); e != nil {
		t.Fatal(e)
	}
	ack, _ = splitGameServerUpperPacket(t, conn.write.Bytes())
	if !bytes.Equal(ack.Body, []byte{1, 1, 0, 0, 0}) || monthlyMailCount(t, s, "1") != 1 {
		t.Fatal("replay success UI increment or duplicate")
	}
	if e := s.handleGameCommand(b, 1, 1113, make([]byte, 13)); e != nil {
		t.Fatal(e)
	}
	if monthlyMailCount(t, s, "5") != 0 {
		t.Fatal("foreign reward")
	}
}
func TestWeeklyBridgeProducerShapeAndOwnershipReject(t *testing.T) {
	for _, tc := range []struct {
		op    uint16
		class byte
		body  []byte
	}{{1113, 1, nil}, {1113, 1, make([]byte, 12)}, {1113, 0, make([]byte, 13)}, {689, 1, []byte{0xa0, 8, 0, 0}}, {689, 1, append([]byte{0xa0, 8, 0, 0, 1}, make([]byte, 11)...)}, {689, 0, append([]byte{0xa0, 8, 0, 0}, make([]byte, 12)...)}} {
		s, a, _, _ := prepareMonthlyBridge(t)
		weeklyBridgeSeed(t, s, a, 4)
		_, _ = s.handleCurrentWeeklyAttendanceClaim(a, tc.op, tc.class, tc.body)
		if monthlyMailCount(t, s, "1") != 0 {
			t.Fatalf("malformed rewarded%+v", tc)
		}
	}
	s, a, _, _ := prepareMonthlyBridge(t)
	a.accountFromToken = false
	_, _ = s.handleCurrentWeeklyAttendanceClaim(a, 1113, 1, make([]byte, 13))
	g, _ := s.repositoryGroup()
	if _, found, _ := g.Settings.Load(nil, onlineevent.WeeklySeasonScope); found {
		t.Fatal("untrusted activation")
	}
}
func TestWeeklyBridgeAcceptedSettlementReceiptCountsOnce(t *testing.T) {
	s, r, a, _, _ := prepareCompletedSettlementRuntime(t)
	s.options.currentClientVersion = "official-24.9"
	loadCurrentAdmissionArchive(t, s)
	g, _ := s.repositoryGroup()
	ch, _, _ := g.Character.Load(nil, "99")
	id := ch.AccountID
	a.accountID = id
	a.loginPacketSeen = true
	a.accountFromToken = true
	s.mu.Lock()
	if s.gameSessions == nil {
		s.gameSessions = make(map[uint16]*gameSession)
	}
	s.gameSessions[99] = a
	a.characterGeneration = 1
	s.mu.Unlock()
	_ = g.Account.Save(nil, repo.AccountRecord{AccountID: id, Metadata: map[string]string{"keep": "yes"}})
	if e := s.initializeActivityControls(context.Background()); e != nil {
		t.Fatal(e)
	}
	ch.Level = 1
	ch.Stats["exp"] = 0
	_ = g.Character.Save(nil, ch)
	_ = g.Skill.Save(nil, repo.SkillRecord{CharacterID: "99", Points: repo.SkillPointState{SyncedLevel: 1}})
	_ = g.Inventory.Save(nil, repo.InventoryRecord{CharacterID: "99", Slots: map[string]repo.ItemStack{}})
	r.Character = ch
	r.Dungeon.Metadata.RecommendedLevels = []int64{1, 90}
	r.ordinaryFinalRoomClearAccepted = true
	resources := currentDungeonSettlementTestResources(t)
	original := g
	g.CharacterSettlement = monthlyFailingSettlementUOW{g.CharacterSettlement}
	s.repositoryProvider = func() (repo.Group, bool) { return g, true }
	r.clearMapCompletionKey = "weekly-failed"
	if _, e := s.commitCurrentDungeonSettlement(context.Background(), a, r, resources.Progression, 1); e == nil {
		t.Fatal("settlement failure accepted")
	}
	season0, e := s.currentWeeklySeason(a)
	if e != nil {
		t.Fatal(e)
	}
	failed, _, _ := g.Account.Load(nil, id)
	if failed.Metadata[season0.Key()] != "" {
		t.Fatal("failed receipt leaked weekly clear")
	}
	g = original
	s.repositoryProvider = func() (repo.Group, bool) { return g, true }
	for i, key := range []string{"weekly1", "weekly1", "weekly2"} {
		r.clearMapCompletionKey = key
		r.Character, _, _ = g.Character.Load(nil, "99")
		result, e := s.commitCurrentDungeonSettlement(context.Background(), a, r, resources.Progression, 1)
		if e != nil || (i == 1) != result.Replayed {
			t.Fatalf("clear%d %+v %v", i, result, e)
		}
	}
	season, e := s.currentWeeklySeason(a)
	if e != nil {
		t.Fatal(e)
	}
	c, e := s.currentWeeklyCatalog(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	state, e := onlineevent.SettleWeeklyAttendance(nil, g, c, onlineevent.WeeklyCommand{AccountID: id, CharacterID: "99", Now: s.gameplayNow(), Season: season, Deliver: func(repo.MailboxRepository, onlineevent.AttendancePVFItemReward, string) (string, error) {
		t.Fatal("query delivered")
		return "", nil
	}})
	if e != nil || state.Clears != 2 {
		t.Fatalf("count%+v %v", state, e)
	}
}

func TestWeeklyBridgeActualBonus689OwnedZerosAndReplay(t *testing.T) {
	s, a, _, conn := prepareMonthlyBridge(t)
	now := s.gameplayNow()
	s.gameplayTimers = &processGameplayTimeQueue{now: func() time.Time { return now }}
	for i := 0; i < 3; i++ {
		weeklyBridgeSeed(t, s, a, 4)
		if e := s.sendCurrentWeeklyAttendance(a, true, false); e != nil {
			t.Fatal(e)
		}
		if i < 2 {
			now = now.AddDate(0, 0, 7)
		}
	}
	conn.write.Reset()
	body := make([]byte, 16)
	binary.LittleEndian.PutUint32(body, 2208)
	if e := s.handleGameCommand(a, 1, 689, body); e != nil {
		t.Fatal(e)
	}
	if monthlyMailCount(t, s, "1") != 4 {
		t.Fatal("actual bonus not mailed")
	}
	state, _ := splitGameServerUpperPacket(t, conn.write.Bytes())
	if state.Header.MsgID != 1093 || binary.LittleEndian.Uint32(state.Body[8:]) != 8 || state.Body[0x1ca] != 0 {
		t.Fatalf("bonus state%+v", state)
	}
	conn.write.Reset()
	if e := s.handleGameCommand(a, 1, 689, body); e != nil {
		t.Fatal(e)
	}
	if monthlyMailCount(t, s, "1") != 4 {
		t.Fatal("bonus duplicated")
	}
	foreign := make([]byte, 16)
	binary.LittleEndian.PutUint32(foreign, 2370)
	if handled, e := s.handleCurrentWeeklyAttendanceClaim(a, 689, 1, foreign); handled || e != nil {
		t.Fatal("foreign event hijacked")
	}
}
func TestWeeklyBridgeCatalog2208ExactGenericNativeRow(t *testing.T) {
	body, e := buildCurrentActivityEventInfoBodyWithWeekly([]uint32{490006574}, 10800, false, false, false, false, false, 0, false, false, true)
	if e != nil || len(body) != 68 || binary.LittleEndian.Uint16(body) != 1 || binary.LittleEndian.Uint16(body[2:]) != 2208 || binary.LittleEndian.Uint32(body[4:]) != 1 || !bytes.Equal(body[8:17], make([]byte, 9)) || body[17] != 1 || binary.LittleEndian.Uint16(body[18:]) != 2208 || !bytes.Equal(body[20:], make([]byte, 48)) {
		t.Fatalf("catalog%x %v", body, e)
	}
}
