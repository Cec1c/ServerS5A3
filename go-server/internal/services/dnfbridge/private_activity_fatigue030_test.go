package dnfbridge

import (
	"context"
	"errors"
	"os"
	"reflect"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/fatigue"
	"longheng.io/server/internal/modules/dnf/premium"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

func TestPrivateActivityFatigue030DailyEntryTransaction(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("real PVF required")
	}
	s, g, fail, clock := newDailyCountAtomicTestService(t, path)
	clock.now = time.Date(2026, 10, 30, 12, 0, 0, 0, time.FixedZone("UTC+8", 8*3600))
	ctx := context.Background()
	for _, test := range []struct {
		name     string
		saveFail bool
		invalid  bool
	}{
		{"actual_ekern_entry_99_to_100", false, false},
		{"save_failure_rolls_back_fatigue_and_progress", true, false},
		{"invalid_activity_rolls_back_whole_entry", false, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			session := seedDungeonDailyCountCharacter(t, g, "99", 99, "fatigue030")
			if err := g.Account.Save(ctx, repo.AccountRecord{AccountID: "fatigue030"}); err != nil {
				t.Fatal(err)
			}
			bindDungeonSelectorOriginForTestAt(t, s, session, 107, 0, 250, 224)
			ch, _, err := g.Character.Load(ctx, "99")
			if err != nil {
				t.Fatal(err)
			}
			fatigue.Refresh(&ch, repo.AccountRecord{AccountID: "fatigue030"}, clock.now)
			if _, err = s.applyPrivateActivity(&ch, 6, 99, clock.now, clock.now); err != nil {
				t.Fatal(err)
			}
			if test.invalid {
				ch.Stats["private_activity_v1_fingerprint"]++
			}
			if err = g.Character.Save(ctx, ch); err != nil {
				t.Fatal(err)
			}
			before := snapshotDailyCountAssets(t, g, "99")
			fail.armed = test.saveFail
			run, err := enterDungeonOnce(t, s, session, 5097)
			fail.armed = false
			after := snapshotDailyCountAssets(t, g, "99")
			if test.saveFail || test.invalid {
				if err == nil {
					t.Fatal("accepted rejected entry")
				}
				if test.saveFail && !errors.Is(err, errInjectedCounterSave) {
					t.Fatal(err)
				}
				requireSameAssets(t, before, after)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if after.stats["fatigue"] != before.stats["fatigue"]-1 || after.stats["private_activity_v1_progress_6"] != 100 || after.stats["private_activity_v1_points"] != 20 {
				t.Fatalf("successful entry did not count exactly once: %v", after.stats)
			}
			scene, ok := run.Session.Scene()
			if !ok {
				t.Fatal("missing scene")
			}
			if err = s.consumeCurrentDailyCountEntry(session, run, scene, true); err != nil {
				t.Fatal(err)
			}
			replay := snapshotDailyCountAssets(t, g, "99")
			requireSameAssets(t, after, replay)
		})
	}
}

func TestPrivateActivityFatigue030AllRoomPaths(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("real PVF required")
	}
	s, g, fail, clock := newDailyCountAtomicTestService(t, path)
	clock.now = time.Date(2026, 10, 30, 12, 0, 0, 0, time.FixedZone("UTC+8", 8*3600))
	ctx := context.Background()
	if err := g.Account.Save(ctx, repo.AccountRecord{AccountID: "fatigue030"}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{1, 316, 3570, 3571, 5097, 5098, 6004, 6013, 6062, 6063, 6064, 6065, 7100, 7165} {
		d, found := s.worldMapTable.FindDungeon(id)
		if !found {
			t.Fatalf("missing dungeon %d", id)
		}
		session := &gameSession{accountID: "fatigue030", selectedCharacterID: 99}
		seed := repo.CharacterRecord{CharacterID: "99", AccountID: "fatigue030", Stats: map[string]int64{"fatigue": 156, "fatigue_limit": 156, fatigue.DayKey: premium.ServiceDayID(clock.now)}}
		if err := g.Character.Save(ctx, seed); err != nil {
			t.Fatal(err)
		}
		runtime := &runtimeDungeonState{Dungeon: d}
		scene := worldmap.DungeonRoomScene{Coordinate: worldmap.RoomCoordinate{X: 1, Y: 1}}
		for i := 0; i < 2; i++ {
			if err := s.debitDungeonRoomFatigue(session, runtime, scene); err != nil {
				t.Fatalf("id=%d %v", id, err)
			}
		}
		ch, _, err := g.Character.Load(ctx, "99")
		if err != nil {
			t.Fatal(err)
		}
		spent := int64(156) - ch.Stats["fatigue"]
		want := int64(1)
		if currentDungeonUsesNoFatigue(d) {
			want = 0
		}
		if spent != want || ch.Stats["private_activity_v1_progress_6"] != spent {
			t.Fatalf("id=%d spent=%d progress=%d", id, spent, ch.Stats["private_activity_v1_progress_6"])
		}
		t.Logf("dungeon=%d no_fatigue=%t actual_debit=%d counted=%d", id, currentDungeonUsesNoFatigue(d), spent, ch.Stats["private_activity_v1_progress_6"])
	}
	session := &gameSession{accountID: "fatigue030", selectedCharacterID: 99}
	seed := repo.CharacterRecord{CharacterID: "99", AccountID: "fatigue030", Stats: map[string]int64{"fatigue": 156, "fatigue_limit": 156, fatigue.DayKey: premium.ServiceDayID(clock.now)}}
	if err := g.Character.Save(ctx, seed); err != nil {
		t.Fatal(err)
	}
	if _, err := s.updateCurrentCharacterFatigueAmount(ctx, session, 99); err != nil {
		t.Fatal(err)
	}
	ch, _, _ := g.Character.Load(ctx, "99")
	// A potion restores balance, but must not erase 99 already consumed FP.
	ch.Stats["fatigue"] = 156
	if err := g.Character.Save(ctx, ch); err != nil {
		t.Fatal(err)
	}
	if _, err := s.updateCurrentCharacterFatigue(ctx, session, false); err != nil {
		t.Fatal(err)
	}
	ch, _, _ = g.Character.Load(ctx, "99")
	if ch.Stats["private_activity_v1_progress_6"] != 99 {
		t.Fatal(ch.Stats)
	}
	fail.armed = true
	if _, err := s.updateCurrentCharacterFatigueAmount(ctx, session, 1); err == nil {
		t.Fatal("save failure accepted")
	}
	fail.armed = false
	after, _, _ := g.Character.Load(ctx, "99")
	if !reflect.DeepEqual(ch.Stats, after.Stats) {
		t.Fatal("failed debit changed stats")
	}
	if _, err := s.updateCurrentCharacterFatigueAmount(ctx, session, 1); err != nil {
		t.Fatal(err)
	}
	after, _, _ = g.Character.Load(ctx, "99")
	if after.Stats["private_activity_v1_progress_6"] != 100 || after.Stats["private_activity_v1_points"] != 20 {
		t.Fatal(after.Stats)
	}
	if _, err := s.updateCurrentCharacterFatigueAmount(ctx, session, 10); err != nil {
		t.Fatal(err)
	}
	after, _, _ = g.Character.Load(ctx, "99")
	if after.Stats["private_activity_v1_points"] != 20 {
		t.Fatal("double awarded")
	}
	clock.now = clock.now.Add(24 * time.Hour)
	if _, err := s.updateCurrentCharacterFatigueAmount(ctx, session, 1); err != nil {
		t.Fatal(err)
	}
	after, _, _ = g.Character.Load(ctx, "99")
	if after.Stats["private_activity_v1_progress_6"] != 1 || after.Stats["private_activity_v1_points"] != 0 {
		t.Fatal(after.Stats)
	}
}
