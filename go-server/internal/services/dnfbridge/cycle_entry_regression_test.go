package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	dnfdungeon "longheng.io/server/internal/modules/dnf/dungeon"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
	"os"
	"testing"
	"time"
)

func TestRealPVFCycleEntryCommitsBeforeClear(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("requires published PVF")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	table, resolver, monsters := loadBridgeDungeonStaticData(t, doc94EkernBossRoomPVFSource())
	repos := dnfrepomemory.NewMemoryGroup()
	ctx := context.Background()
	if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{
		CharacterID: "94",
		AccountID:   "doc94-account",
		Name:        "DOC94",
		Job:         "0",
		Level:       90,
		Stats: map[string]int64{
			"fatigue": 100, "town_id": 107, "area_id": 0,
			"pos_x": 250, "pos_y": 224, "direction": 5, "area_state": 3,
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "94", Slots: map[string]dnfrepo.ItemStack{}}); err != nil {
		t.Fatal(err)
	}
	queue := newFakeCurrentDungeonDeathTimerQueue()
	queue.now = time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	service := &Service{
		options: options{
			currentClientVersion: "official-24.9",
			accountID:            "doc94-account",
			gameUpperBodyCodec:   gameUpperBodyCodecPlain,
			pvfPath:              path,
		},
		worldMapTable:           table,
		worldMapResolver:        resolver,
		dungeonMonsterTable:     monsters,
		initialEquipmentArchive: archive,
		gameplayTimers:          queue,
		dungeonChoice:           func(int) (int, error) { return 0, nil },
		dungeonSeed:             func() (uint32, error) { return 1, nil },
		repositoryProvider:      func() (dnfrepo.Group, bool) { return repos, true },
	}
	requirement, declared, err := service.currentDungeonCycleRequirementForDungeon(5097)
	if err != nil || !declared {
		t.Fatalf("real v106 etc/dungeoncyclesystem.etc must declare 5097: declared=%t err=%v", declared, err)
	}
	if requirement.DailyCount != 3 {
		t.Fatalf("real v106 5097 daily count=%d, want the published 3", requirement.DailyCount)
	}
	session := seedDungeonDailyCountCharacter(t, repos, "94", 94, "doc94-account")
	bindDungeonSelectorOriginForTestAt(t, service, session, 107, 0, 250, 224)

	storedCount := func() int64 {
		t.Helper()
		stored, found, err := repos.Character.Load(ctx, "94")
		if err != nil || !found {
			t.Fatalf("load character 94: err=%v found=%t", err, found)
		}
		used, err := dnfdungeon.DailyEntryUsed(stored, 5097, queue.now)
		if err != nil {
			t.Fatal(err)
		}
		return used
	}

	if _, err := enterDungeonOnce(t, service, session, 5097); err != nil {
		t.Fatal(err)
	}
	if cleared := storedCount(); cleared != 0 {
		t.Fatalf("entry incorrectly committed a clear: %d", cleared)
	}
	stored, _, err := repos.Character.Load(ctx, "94")
	if err != nil {
		t.Fatal(err)
	}
	if used := stored.Stats["dungeon_cycle_entry_5097_used"]; used != 1 {
		t.Fatalf("successful entry did not consume an independent cycle allowance: used=%d want1", used)
	}
	assertProjection := func(wantA, wantC uint32) {
		t.Helper()
		connection := session.conn.(*bufferConn)
		packets := currentExperienceItemWirePackets(t, connection)
		found := false
		for _, packet := range packets {
			if packet.Header.MsgID != 656 {
				continue
			}
			if packet.Header.Classification != 0 || len(packet.Body) != 65 || packet.Body[0] != 4 {
				t.Fatalf("wrong native cycle packet: %+v", packet)
			}
			for i := 0; i < 4; i++ {
				off := 1 + i*16
				if binary.LittleEndian.Uint32(packet.Body[off:]) == 5097 {
					A := binary.LittleEndian.Uint32(packet.Body[off+4:])
					B := binary.LittleEndian.Uint32(packet.Body[off+8:])
					C := binary.LittleEndian.Uint32(packet.Body[off+12:])
					if A == wantA && C == wantC {
						if B != 0 {
							t.Fatal("unused counter changed")
						}
						found = true
					}
				}
			}
		}
		if !found {
			t.Fatalf("missing cycle projection A=%d C=%d", wantA, wantC)
		}
	}
	assertProjection(0, 1)
	if err := service.commitDungeonRuntime(session, session.dungeon.runtime); !errors.Is(err, errDungeonAlreadyActive) {
		t.Fatalf("duplicate active commit err=%v", err)
	}
	if err := service.handleDungeonMonsterDeath(session, doc94Official249DeathBody(doc94EkernClearConditionObjectID)); err != nil {
		t.Fatal(err)
	}
	if cleared := storedCount(); cleared != 1 {
		t.Fatalf("clear progress=%d", cleared)
	}
	assertProjection(1, 1)
	if err := service.handleDungeonMonsterDeath(session, doc94Official249DeathBody(doc94EkernClearConditionObjectID)); err != nil {
		t.Fatal(err)
	}
	if storedCount() != 1 {
		t.Fatal("replayed death changed clear progress")
	}
	for entry := int64(2); entry <= 3; entry++ {
		// First run completed; subsequent runtime removal is an explicit abandon
		// fixture. Admission still goes through prepare/freeze/commit.
		session.dungeon.runtime = nil
		if _, err := enterDungeonOnce(t, service, session, 5097); err != nil {
			t.Fatalf("entry%d: %v", entry, err)
		}
		c, _, _ := repos.Character.Load(ctx, "94")
		used, _ := dnfdungeon.CycleEntryUsed(c, 5097, queue.now)
		if used != entry || storedCount() != 1 {
			t.Fatalf("entry%d used%d clear%d", entry, used, storedCount())
		}
		assertProjection(1, uint32(entry))
	}
	session.dungeon.runtime = nil
	before, _, _ := repos.Character.Load(ctx, "94")
	if _, err := enterDungeonOnce(t, service, session, 5097); !errors.Is(err, dnfdungeon.ErrDailyEntryLimit) {
		t.Fatalf("fourth entry err=%v", err)
	}
	trial, _, err := service.currentCycleAdmission(5098)
	if err != nil {
		t.Fatal(err)
	}
	if err := dnfdungeon.ValidateCycleEntryAllowance(before, trial, queue.now); !errors.Is(err, dnfdungeon.ErrEntryPrerequisiteMissing) {
		t.Fatalf("entry count unlocked trial: %v", err)
	}
	// A new service day projects zero without rewriting yesterday's history.
	queue.now = queue.now.AddDate(0, 0, 1)
	body, declared, err := service.buildCurrentDungeonCycleInfo(before)
	if err != nil || !declared {
		t.Fatalf("reset projection: %v %t", err, declared)
	}
	for i := 0; i < 4; i++ {
		off := 1 + i*16
		if binary.LittleEndian.Uint32(body[off+4:]) != 0 || binary.LittleEndian.Uint32(body[off+12:]) != 0 {
			t.Fatal("stale counters across reset")
		}
	}
}
