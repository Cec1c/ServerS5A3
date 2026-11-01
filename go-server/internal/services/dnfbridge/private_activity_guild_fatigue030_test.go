package dnfbridge

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"testing"

	"longheng.io/server/internal/modules/dnf/activeness"
	pvf "longheng.io/server/internal/modules/dnf/pvf"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// Use the actual bridge/catalog hook with an isolated in-memory catalog cache.
// No archive files, environment variables or player repositories are required.
func privateActivityGuildFatigue030Fixture(t *testing.T, count int) (*guildDungeonEntryFatigueFixture, *activeness.Catalog) {
	t.Helper()
	f := newGuildDungeonEntryFatigueFixture(t, count)
	doc, err := pvf.Parse(activeness.PVFPath, `[private activity tasks]
1 1 10 2 1 10 3 1 50 4 1 100 5 1 10 6 100 20 7 1 10 8 1 10
[/private activity tasks]
[private activity rewards]
1 30 500 990027401 1 0 0 2 50 500 990027402 1 3330 500 3 100 5500 990027403 1 3330 500
[/private activity rewards]`)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := activeness.ParseCatalog(doc)
	if err != nil || catalog == nil {
		t.Fatal(catalog, err)
	}
	archive := &platformpvf.Archive{}
	f.service.initialEquipmentArchive = archive
	privateActivityCatalogs.Store(archive, privateActivityCatalogResult{catalog: catalog})
	t.Cleanup(func() { privateActivityCatalogs.Delete(archive) })
	queue := newFakeCurrentDungeonDeathTimerQueue()
	queue.now = f.entries[0].JoinedAt
	f.service.gameplayTimers = queue
	return f, catalog
}

func TestPrivateActivityGuildFatigue030RealBridgeParty(t *testing.T) {
	f, catalog := privateActivityGuildFatigue030Fixture(t, 4)
	ctx := context.Background()
	initial := []uint32{0, 92, 99, 100}
	for i, progress := range initial {
		id := strconv.Itoa(i + 1)
		c, _, err := f.repos.Character.Load(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if progress > 0 {
			if _, err := activeness.Apply(&c, catalog, 6, progress, f.service.gameplayNow(), f.service.gameplayNow()); err != nil {
				t.Fatal(err)
			}
		}
		if err := f.repos.Character.Save(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.commit(t); err != nil {
		t.Fatal(err)
	}
	for i, progress := range []int64{8, 100, 100, 100} {
		id := strconv.Itoa(i + 1)
		c, _, err := f.repos.Character.Load(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		points := int64(0)
		if progress == 100 {
			points = 20
		}
		if c.Stats["fatigue"] != 72 || c.Stats["private_activity_v1_progress_6"] != progress || c.Stats["private_activity_v1_points"] != points {
			t.Fatalf("member %s: fatigue=%d progress6=%d points=%d; want 72/%d/%d", id, c.Stats["fatigue"], c.Stats["private_activity_v1_progress_6"], c.Stats["private_activity_v1_points"], progress, points)
		}
		if f.runtimes[i].Character.Stats["private_activity_v1_progress_6"] != progress {
			t.Fatal("runtime snapshot omitted committed activity", id)
		}
	}
	if !f.receiptExists(t) {
		t.Fatal("successful entry omitted receipt")
	}
}

func privateActivityGuildFatigue030Snapshot(t *testing.T, f *guildDungeonEntryFatigueFixture) map[string]repo.CharacterRecord {
	t.Helper()
	rows := map[string]repo.CharacterRecord{}
	for i := range f.sessions {
		id := strconv.Itoa(i + 1)
		c, found, err := f.repos.Character.Load(context.Background(), id)
		if err != nil || !found {
			t.Fatal(id, found, err)
		}
		rows[id] = c
	}
	return rows
}

func TestPrivateActivityGuildFatigue030ReplayAndRoomsDoNotApplyAgain(t *testing.T) {
	f, _ := privateActivityGuildFatigue030Fixture(t, 4)
	if err := f.commit(t); err != nil {
		t.Fatal(err)
	}
	before := privateActivityGuildFatigue030Snapshot(t, f)
	// A failing catalog proves a replay does not call the bridge activity hook.
	privateActivityCatalogs.Store(f.service.initialEquipmentArchive, privateActivityCatalogResult{err: activeness.ErrCatalog})
	for _, runtime := range f.runtimes {
		runtime.fatigueStartDebited = false
	}
	if err := f.commit(t); err != nil {
		t.Fatal("receipt replay called activity hook", err)
	}
	for i, runtime := range f.runtimes {
		for room := int64(0); room < 3; room++ {
			if err := f.service.debitDungeonRoomFatigue(f.sessions[i], runtime, worldmap.DungeonRoomScene{Coordinate: worldmap.RoomCoordinate{X: room}}); err != nil {
				t.Fatal(err)
			}
		}
	}
	if !reflect.DeepEqual(before, privateActivityGuildFatigue030Snapshot(t, f)) {
		t.Fatal("receipt replay or later room re-counted fatigue/activity")
	}
}

func TestPrivateActivityGuildFatigue030OnePointThresholdAndZeroCost(t *testing.T) {
	for _, zero := range []bool{false, true} {
		t.Run(strconv.FormatBool(zero), func(t *testing.T) {
			f, catalog := privateActivityGuildFatigue030Fixture(t, 1)
			ctx := context.Background()
			c, _, _ := f.repos.Character.Load(ctx, "1")
			if _, err := activeness.Apply(&c, catalog, 6, 99, f.service.gameplayNow(), f.service.gameplayNow()); err != nil {
				t.Fatal(err)
			}
			if err := f.repos.Character.Save(ctx, c); err != nil {
				t.Fatal(err)
			}
			f.runtimes[0].Dungeon.Metadata.IntegerValues[worldmap.DungeonUseFatigueOnlyStartKey] = 1
			if zero {
				f.runtimes[0].Dungeon.Metadata.NoFatigue = true
				privateActivityCatalogs.Store(f.service.initialEquipmentArchive, privateActivityCatalogResult{err: activeness.ErrCatalog})
			}
			if err := f.commit(t); err != nil {
				t.Fatal(err)
			}
			got, _, _ := f.repos.Character.Load(ctx, "1")
			if zero {
				if got.Stats["fatigue"] != 80 || got.Stats["private_activity_v1_progress_6"] != 99 || got.Stats["private_activity_v1_points"] != 0 {
					t.Fatal("zero cost counted", got.Stats)
				}
			} else if got.Stats["fatigue"] != 79 || got.Stats["private_activity_v1_progress_6"] != 100 || got.Stats["private_activity_v1_points"] != 20 || got.Stats["private_activity_v1_done"]&(1<<5) == 0 {
				t.Fatal("99+1 did not finish exactly once", got.Stats)
			}
		})
	}
}

type privateActivityGuildFatigue030FailUOW struct {
	repo.GuildUnitOfWork
	failID      string
	failReceipt bool
}

func (u privateActivityGuildFatigue030FailUOW) WithinGuildDungeonEntry(ctx context.Context, owners []repo.GuildOwner, scope string, apply func(repo.Group) error) error {
	return u.GuildUnitOfWork.(repo.GuildDungeonEntryUnitOfWork).WithinGuildDungeonEntry(ctx, owners, scope, func(tx repo.Group) error {
		if u.failID != "" {
			tx.Character = &guildDungeonFailingCharacters{CharacterRepository: tx.Character, failID: u.failID, err: errors.New("injected activity character save failure")}
		}
		if u.failReceipt {
			tx.Settings = privateActivityGuildFatigue030FailSettings{SettingsRepository: tx.Settings, scope: scope}
		}
		return apply(tx)
	})
}

type privateActivityGuildFatigue030FailSettings struct {
	repo.SettingsRepository
	scope string
}

func (s privateActivityGuildFatigue030FailSettings) Save(ctx context.Context, record repo.SettingsRecord) error {
	if record.Scope == s.scope {
		return errors.New("injected activity receipt save failure")
	}
	return s.SettingsRepository.Save(ctx, record)
}

func TestPrivateActivityGuildFatigue030BridgeFailuresRollBackAllMembers(t *testing.T) {
	for _, failure := range []string{"activity_2", "activity_4", "save_2", "save_4", "receipt"} {
		t.Run(failure, func(t *testing.T) {
			f, catalog := privateActivityGuildFatigue030Fixture(t, 4)
			ctx := context.Background()
			for id, c := range privateActivityGuildFatigue030Snapshot(t, f) {
				if _, err := activeness.Apply(&c, catalog, 6, 99, f.service.gameplayNow(), f.service.gameplayNow()); err != nil {
					t.Fatal(err)
				}
				if failure == "activity_"+id {
					c.Stats["private_activity_v1_progress_6"] = 101
				}
				if err := f.repos.Character.Save(ctx, c); err != nil {
					t.Fatal(err)
				}
			}
			if failure == "save_2" || failure == "save_4" {
				f.repos.Guild = privateActivityGuildFatigue030FailUOW{GuildUnitOfWork: f.repos.Guild, failID: failure[len("save_"):]}
			} else if failure == "receipt" {
				f.repos.Guild = privateActivityGuildFatigue030FailUOW{GuildUnitOfWork: f.repos.Guild, failReceipt: true}
			}
			before := privateActivityGuildFatigue030Snapshot(t, f)
			if err := f.commit(t); err == nil {
				t.Fatal("activity/save failure was ignored")
			} else if (failure == "activity_2" || failure == "activity_4") && !errors.Is(err, activeness.ErrState) {
				t.Fatal("bridge activity error was not propagated", err)
			}
			if !reflect.DeepEqual(before, privateActivityGuildFatigue030Snapshot(t, f)) || f.receiptExists(t) {
				t.Fatal("failed bridge entry retained fatigue, activity or receipt")
			}
			for _, runtime := range f.runtimes {
				if runtime.fatigueStartDebited || len(runtime.fatigueRooms) != 0 {
					t.Fatal("failed transaction marked runtime committed")
				}
			}
		})
	}
}
