package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"log/slog"
	"longheng.io/server/internal/modules/dnf/fatigue"
	"longheng.io/server/internal/modules/dnf/premium"
	repo "longheng.io/server/internal/modules/dnf/repository"
	memory "longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/modules/dnf/worldmap"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestDungeonFatigueVisitsArePersonalDurableAndNeverNegative(t *testing.T) {
	repos := memory.NewMemoryGroup()
	ctx := context.Background()
	if e := repos.Account.Save(ctx, repo.AccountRecord{AccountID: "account-1"}); e != nil {
		t.Fatal(e)
	}
	for _, id := range []string{"99", "100"} {
		if e := repos.Character.Save(ctx, repo.CharacterRecord{CharacterID: id, AccountID: "account-1", Stats: map[string]int64{"fatigue": 2}}); e != nil {
			t.Fatal(e)
		}
	}
	s := &Service{options: options{accountID: "account-1"}, repositoryProvider: func() (repo.Group, bool) { return repos, true }}
	sessions := []*gameSession{{selectedCharacterID: 99}, {selectedCharacterID: 100}}
	runtimes := []*runtimeDungeonState{{}, {}}
	for i, session := range sessions {
		for _, position := range []worldmap.RoomCoordinate{{X: 0, Y: 0}, {X: 1, Y: 0}, {X: 0, Y: 0}, {X: 2, Y: 0}} {
			if e := s.debitDungeonRoomFatigue(session, runtimes[i], worldmap.DungeonRoomScene{Coordinate: position}); e != nil {
				t.Fatal(e)
			}
		}
		if runtimes[i].Character.Stats["fatigue"] != 0 {
			t.Fatal("fatigue below zero or revisit debit incorrect")
		}
	}
	loaded, _, _ := repos.Character.Load(ctx, "100")
	if loaded.Stats["fatigue"] != 0 {
		t.Fatal("member debit was not persisted")
	}
	if len(runtimes[0].fatigueRooms) != 3 || len(runtimes[1].fatigueRooms) != 3 {
		t.Fatal("room identity tracking incorrect")
	}
	// A session replacement refreshes the same durable balance instead of refilling.
	replacement := &gameSession{selectedCharacterID: 100}
	again, e := s.updateCurrentCharacterFatigue(ctx, replacement, false)
	if e != nil || again.Stats["fatigue"] != 0 {
		t.Fatalf("relog reset: %v %v", again, e)
	}
}

func TestOnlineFatigueAndPotionAllowanceRefreshPreciselyAtSixAM(t *testing.T) {
	queue := newFakeCurrentDungeonDeathTimerQueue()
	zone := time.FixedZone("UTC+8", 8*3600)
	queue.now = time.Date(2026, 10, 1, 5, 59, 59, 0, zone)
	repos := memory.NewMemoryGroup()
	ctx := context.Background()
	account := repo.AccountRecord{AccountID: "account-1", Metadata: map[string]string{premium.MetadataKey(premium.TypeBlackDiamond): strconv.FormatInt(queue.Now().Add(time.Hour).Unix(), 10)}}
	repos.Account.Save(ctx, account)
	c := repo.CharacterRecord{CharacterID: "99", AccountID: account.AccountID, Stats: map[string]int64{"fatigue": 0, "fatigue_limit": 188, fatigue.DayKey: premium.ServiceDayID(queue.Now()), "fatigue_potion_used_123": 1, "fatigue_potion_day_123": premium.ServiceDayID(queue.Now())}}
	repos.Character.Save(ctx, c)
	catalog, err := newPVFDungeonDropCatalog(dungeonDropCatalogTestSource{
		"monster/monster.lst":     "",
		"equipment/equipment.lst": "",
		"stackable/stackable.lst": "123 `test/123.stk`\n",
		"stackable/test/123.stk":  "[action type]\n`[add fatigue]` 50\n[/action type]\n[total usable count]\n3\n[cool time]\n1000\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	s := &Service{logger: slog.New(slog.NewTextHandler(&logs, nil)), options: options{accountID: account.AccountID, gameUpperBodyCodec: gameUpperBodyCodecPlain}, pvfItemCatalog: catalog, gameplayTimers: queue, repositoryProvider: func() (repo.Group, bool) { return repos, true }}
	conn := &bufferConn{}
	session := &gameSession{connID: "six-AM-test", conn: conn, selectedCharacterID: 99, characterGeneration: 2}
	s.armCurrentFatigueDailyRefresh(session)
	task := queue.task(t, 0)
	if task.delay != time.Second {
		t.Fatalf("refresh due in %s", task.delay)
	}
	queue.now = queue.now.Add(time.Second)
	task.callback(queue.Now())
	if strings.Contains(logs.String(), "game-character-fatigue-daily-refresh-deferred") {
		t.Fatalf("refresh callback deferred: %s", logs.String())
	}
	loaded, _, _ := repos.Character.Load(ctx, "99")
	if loaded.Stats["fatigue"] != 188 || loaded.Stats["fatigue_potion_used_123"] != 0 {
		t.Fatalf("online refresh=%v", loaded.Stats)
	}
	if conn.write.Len() == 0 {
		t.Fatalf("refresh produced no packet; logs=%s", logs.String())
	}
	packet, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
	if packet.Header.MsgID != currentFatigueMsgID || binary.LittleEndian.Uint16(packet.Body[2:]) != 188 || binary.LittleEndian.Uint16(packet.Body) != 0 {
		t.Fatalf("online fatigue UI=%x", packet.Body)
	}
	usage, rest := splitGameServerUpperPacket(t, rest)
	if len(rest) != 0 || usage.Header.MsgID != currentFatiguePotionUsageMsgID || usage.Header.Classification != 0 || string(usage.Body) != string([]byte{1, 123, 0, 0, 0, 0, 0}) {
		t.Fatalf("online potion usage UI header=%+v body=%x rest=%d", usage.Header, usage.Body, len(rest))
	}
	if queue.task(t, 1).delay != 24*time.Hour {
		t.Fatal("next six-AM refresh not scheduled")
	}
}

func TestFatigueSixAMResetsPotionLimitsAndAuctionIsNotBlackDiamond(t *testing.T) {
	zone := time.FixedZone("UTC+8", 8*3600)
	before := time.Date(2026, 10, 1, 5, 59, 59, 0, zone)
	after := before.Add(time.Second)
	account := repo.AccountRecord{Metadata: map[string]string{premium.MetadataKey(29): "1999999999"}}
	c := repo.CharacterRecord{Stats: map[string]int64{"fatigue": 0, "fatigue_limit": 156, fatigue.DayKey: premium.ServiceDayID(before), "fatigue_potion_used_123": 1, "fatigue_potion_day_123": premium.ServiceDayID(before), "fatigue_potion_time_123": before.UnixMilli()}}
	if fatigue.Limit(account, before) != 156 {
		t.Fatal("auction coupon incorrectly activated black diamond")
	}
	fatigue.Refresh(&c, account, after)
	if c.Stats["fatigue"] != 156 || c.Stats["fatigue_potion_used_123"] != 0 || c.Stats["fatigue_potion_time_123"] != before.UnixMilli() || c.Stats["fatigue_potion_day_123"] != premium.ServiceDayID(after) {
		t.Fatalf("six AM refill=%v", c.Stats)
	}
}
