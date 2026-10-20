package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/adventuregroup"
	"longheng.io/server/internal/modules/dnf/alignedcmd"
	dnfdungeon "longheng.io/server/internal/modules/dnf/dungeon"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestRealPVFDayDungeonAdmissionPreservesLevelAndSoloLimits(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("requires published PVF")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	queue := newFakeCurrentDungeonDeathTimerQueue()
	tables, err := adventuregroup.LoadComplete(context.Background(), archive)
	if err != nil {
		t.Fatal(err)
	}
	repos := dnfrepomemory.NewMemoryGroup()
	character := dnfrepo.CharacterRecord{CharacterID: "31", AccountID: "day-admission-account", Level: 90}
	if err := repos.Account.Save(context.Background(), dnfrepo.AccountRecord{AccountID: character.AccountID}); err != nil {
		t.Fatal(err)
	}
	if err := repos.Character.Save(context.Background(), character); err != nil {
		t.Fatal(err)
	}
	s := &Service{options: options{currentClientVersion: "official-24.9"}, gameplayTimers: queue, adventureGroupTable: tables, repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true }}
	session := &gameSession{selectedCharacterID: 31, accountID: character.AccountID}
	t.Setenv("DNFBRIDGE_DUNGEON_TOURNAMENT", "")
	for id, days := range dnfdungeon.DayDungeonSchedules() {
		path := fmt.Sprintf("dungeon/gcontents/171116_dayofdungeon/chn_daydungeon_%02d.dgn", id-4905)
		text, _, err := readInitialPVFText(archive, path)
		if err != nil {
			t.Fatal(err)
		}
		doc, err := dnfpvf.Parse(path, text)
		if err != nil {
			t.Fatal(err)
		}
		d := worldmap.ParseDungeon(id, path, doc)
		if !d.Metadata.MinimumRequiredLevel.Set || d.Metadata.MinimumRequiredLevel.Value != 50 || !d.Metadata.LimitPartyCount.Set || d.Metadata.LimitPartyCount.Value != 1 {
			t.Fatalf("unverified original admission metadata for %d", id)
		}
		for weekday := 0; weekday < 7; weekday++ {
			queue.now = time.Date(2026, 10, 5+weekday, 4, 0, 0, 0, time.UTC)
			day := dnfdungeon.DayDungeonEvent(queue.now)
			open := int64(day) == days[0] || int64(day) == days[1]
			err := s.validateDungeonEntryWithTournament(context.Background(), character, d, session)
			if open && err != nil || !open && !errors.Is(err, dnfdungeon.ErrDayDungeonClosed) {
				t.Fatalf("dungeon %d day %d open=%v err=%v", id, day, open, err)
			}
			if !open {
				continue
			}
			if err := s.validateDungeonEntryWithTournament(context.Background(), dnfrepo.CharacterRecord{Level: 49}, d, &gameSession{}); !errors.Is(err, dnfdungeon.ErrEntryLevelTooLow) {
				t.Fatalf("level gate lost: %v", err)
			}
			party := &gameSession{}
			party.party.state.Members = []alignedcmd.PartyMemberState{{UserID: 31}, {UserID: 32}}
			if err := s.validateDungeonEntryWithTournament(context.Background(), dnfrepo.CharacterRecord{Level: 90}, d, party); !errors.Is(err, dnfdungeon.ErrEntryPartyLimit) {
				t.Fatalf("solo gate lost: %v", err)
			}
		}
	}
	if reason, category := currentSelectDungeonEntryFailureReason(dnfdungeon.ErrDayDungeonClosed); reason != currentSelectDungeonUnavailableReason || category != "entry_day_dungeon_closed" {
		t.Fatal("closed day no longer uses existing client refusal")
	}
}

func TestDayDungeonCatalogPreservesOriginalRowsAndAddsExactlyOneDay(t *testing.T) {
	for _, online := range []bool{true, false} {
		previous, err := buildCurrentActivityEventInfoBody(currentSpendTimeJoustCatalogGoldenRewards, 10800, false, true, true, true, online)
		if err != nil {
			t.Fatal(err)
		}
		old := parseCurrentSpendTimeEventInfoBody(t, previous)
		offset := 2
		for _, row := range old.baseRows {
			offset += len(row.raw)
		}
		for day := uint16(2418); day <= 2424; day++ {
			body, err := buildCurrentActivityEventInfoBodyWithDayDungeon(currentSpendTimeJoustCatalogGoldenRewards, 10800, false, true, true, true, online, day)
			if err != nil {
				t.Fatal(err)
			}
			shape := parseCurrentSpendTimeEventInfoBody(t, body)
			want := append([]byte(nil), previous[:offset]...)
			binary.LittleEndian.PutUint16(want, old.baseCount+1)
			row := make([]byte, 15)
			binary.LittleEndian.PutUint16(row, day)
			binary.LittleEndian.PutUint32(row[2:], 1)
			want = append(want, row...)
			want = append(want, old.activeCount+1)
			want = append(want, previous[offset+1:]...)
			active := make([]byte, 50)
			binary.LittleEndian.PutUint16(active, day)
			want = append(want, active...)
			if !bytes.Equal(body, want) {
				t.Fatal("existing activity bytes changed")
			}
			for flag := uint16(2418); flag <= 2424; flag++ {
				if shape.hasEvent(flag) != (flag == day) {
					t.Fatal("multiple weekdays advertised")
				}
			}
			if dir := os.Getenv("DNF90_DAY_DUNGEON_FIXTURES"); dir != "" && online {
				if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("day-%d.bin", day)), body, 0600); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	if _, err := buildCurrentActivityEventInfoBodyWithDayDungeon(currentSpendTimeJoustCatalogGoldenRewards, 10800, false, true, true, true, true, 2347); err == nil {
		t.Fatal("unverified day flag accepted")
	}
}

func TestRealPVFDayDungeonCatalogRefreshAndSameDayDedupe(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("requires published PVF")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	queue := newFakeCurrentDungeonDeathTimerQueue()
	queue.now = time.Date(2026, 10, 8, 21, 59, 59, 0, time.UTC)
	s := &Service{options: options{currentClientVersion: "official-24.9", joustEventDisabled: true}, initialEquipmentArchive: archive, gameplayTimers: queue}
	conn := &bufferConn{}
	session := &gameSession{conn: conn, connID: "day-catalog", selectedCharacterID: 31, characterGeneration: 1}
	catalog := &currentSpendTimeRuntimeCatalog{rewardItemIDs: currentSpendTimeJoustCatalogGoldenRewards, totalStageSeconds: 10800}
	s.spendTimeCatalogState = catalog
	for i, day := range []uint16{2421, 2421, 2422} {
		if i == 2 {
			queue.now = queue.now.Add(time.Second)
		}
		conn.write.Reset()
		sent, err := s.sendCurrentSpendTimeProtocolState(session, catalog, 123, 1, i == 0)
		if err != nil || sent != (i != 1) {
			t.Fatalf("iteration %d sent=%v err=%v", i, sent, err)
		}
		if session.activityCatalogDayEvent != day {
			t.Fatal("catalog day not committed")
		}
		if sent {
			p, rest := splitGameServerUpperPacketWithHeader(t, conn.write.Bytes(), 16)
			plain, err := zlibDecompress(p.Body)
			if err != nil {
				t.Fatal(err)
			}
			shape := parseCurrentSpendTimeEventInfoBody(t, plain)
			if p.Header.MsgID != 108 || !shape.hasEvent(day) || !shape.hasEvent(2347) || !shape.hasEvent(181) {
				t.Fatal("availability or prior activity lost")
			}
			for old := uint16(2418); old <= 2424; old++ {
				if old != day && shape.hasEvent(old) {
					t.Fatal("yesterday flag retained")
				}
			}
			progress, _ := splitGameServerUpperPacket(t, rest)
			if progress.Header.MsgID != 1206 {
				t.Fatal("progress no longer adjacent")
			}
		}
		if s.currentDayDungeonEvent() != dnfdungeon.DayDungeonEvent(queue.now) {
			t.Fatal("actual PVF schedule unavailable")
		}
	}
	if got := queue.tasks[0].delay; got != time.Second {
		t.Fatalf("crossday timer delay %v", got)
	}
	conn.write.Reset()
	task := queue.tasks[len(queue.tasks)-1]
	queue.now = queue.now.Add(24 * time.Hour)
	queue.fire(task, false)
	if session.activityCatalogDayEvent != 2423 || conn.write.Len() == 0 {
		t.Fatal("actual scheduled callback did not replace Friday with Saturday")
	}
	packet, rest := splitGameServerUpperPacketWithHeader(t, conn.write.Bytes(), 16)
	plain, err := zlibDecompress(packet.Body)
	if err != nil || packet.Header.MsgID != 108 {
		t.Fatal("scheduled full catalog missing", err)
	}
	shape := parseCurrentSpendTimeEventInfoBody(t, plain)
	if !shape.hasEvent(2423) || shape.hasEvent(2422) {
		t.Fatal("scheduled old weekday retained")
	}
	progress, _ := splitGameServerUpperPacket(t, rest)
	if progress.Header.MsgID != 1206 {
		t.Fatal("scheduled progress not adjacent")
	}
	conn.write.Reset()
	task = queue.tasks[len(queue.tasks)-1]
	session.characterGeneration++
	queue.now = queue.now.Add(24 * time.Hour)
	queue.fire(task, false)
	if conn.write.Len() != 0 {
		t.Fatal("retired character generation received refresh")
	}
	s.cancelCurrentDayDungeonCatalogRefresh(session)
}
