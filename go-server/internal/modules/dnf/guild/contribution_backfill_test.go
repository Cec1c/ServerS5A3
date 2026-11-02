package guild

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	driver "github.com/go-sql-driver/mysql"
	r "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	repomysql "longheng.io/server/internal/modules/dnf/repository/mysql"
)

func guildReadTestMySQL(t *testing.T) r.Group {
	t.Helper()
	dsn := os.Getenv("DNFBRIDGE_PLAYER_TEST_DSN")
	if dsn == "" {
		t.Skip("disposable MySQL DSN not configured")
	}
	cfg, err := driver.ParseDSN(dsn)
	if err != nil {
		t.Fatal("invalid disposable DSN")
	}
	cfg.DBName, cfg.ParseTime = "", true
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal("open disposable DB")
	}
	t.Cleanup(func() { db.Close() })
	prefix := fmt.Sprintf("guild_read_test_%d", time.Now().UnixNano())
	plan := r.DatabasePlan{ReadDatabases: []string{prefix + "_absent_replica"}, WriteDatabases: []string{prefix + "_a", prefix + "_b"}}
	if err := repomysql.EnsureMySQLSchema(context.Background(), db, repomysql.SchemaOptions{AutoCreate: true, CreateDatabases: true, DatabasePlan: r.DatabasePlan{WriteDatabases: plan.WriteDatabases, ReadDatabases: plan.WriteDatabases}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, name := range plan.WriteDatabases {
			if _, err := db.Exec("DROP DATABASE `" + name + "`"); err != nil {
				t.Error(err)
			}
		}
	})
	group, err := repomysql.NewMySQLGroupFromDB(db, repomysql.MySQLGroupOptions{DatabasePlan: plan})
	if err != nil {
		t.Fatal(err)
	}
	return group
}

// Every test database is disposable; the absent replica proves reads are
// pinned to the writer. The two writer schemas exercise cross-shard snapshots.
func TestGuildReadSnapshotAndContributionBackfill(t *testing.T) {
	for _, backend := range []string{"memory", "mysql"} {
		t.Run(backend, func(t *testing.T) {
			repos := memory.NewMemoryGroup()
			if backend == "mysql" {
				repos = guildReadTestMySQL(t)
			}
			testGuildReadSnapshot(t, repos)
			testGuildContributionBackfill(t, repos)
		})
	}
}

func guildReadSeed(t *testing.T, repos r.Group) (time.Time, r.GuildOwner) {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	reg := NewRegistry()
	for i := 1; i <= 2; i++ {
		c := r.CharacterRecord{CharacterID: fmt.Sprint(i), AccountID: fmt.Sprint("a", i), Name: fmt.Sprint("Member", i), Level: 90, Job: "1", Stats: map[string]int64{"grow_type": 33, "gold": 123}}
		if err := repos.Character.Save(ctx, c); err != nil {
			t.Fatal(err)
		}
		if i == 1 {
			if _, err := reg.Create(c, "ReadGuild", now); err != nil {
				t.Fatal(err)
			}
		} else if _, err := reg.JoinDirect(1, c, now); err != nil {
			t.Fatal(err)
		}
	}
	if err := Save(ctx, repos.Settings, reg, now); err != nil {
		t.Fatal(err)
	}
	return now, r.GuildOwner{CharacterID: "1", AccountID: "a1"}
}

func testGuildReadSnapshot(t *testing.T, repos r.Group) {
	t.Helper()
	now, owner := guildReadSeed(t, repos)
	u := repos.Guild.(r.GuildReadUnitOfWork)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := u.WithinGuildRead(ctx, func(view r.GuildReadView) error {
		before, err := Load(ctx, view.Settings)
		if err != nil {
			return err
		}
		chars, err := view.Characters.LoadGuildCharacters(ctx, []string{"1", "2"})
		if err != nil {
			return err
		}
		if len(chars) != 2 || chars["1"].GrowType != 33 {
			return fmt.Errorf("batch identity: %+v", chars)
		}
		if err := view.Settings.Save(ctx, r.SettingsRecord{Scope: RegistryScope}); err == nil {
			return errors.New("read view permitted Save")
		}
		if _, _, err := view.Settings.Load(ctx, "unrelated-scope"); err == nil {
			return errors.New("read view permitted unrelated read")
		}
		// Complete a writer while this snapshot is still open. A locking
		// display deadlocks here until ctx expires; a read view permits commit.
		started := time.Now()
		err = repos.Guild.WithinGuild(ctx, []r.GuildOwner{owner, {CharacterID: "2", AccountID: "a2"}}, func(tx r.Group) error {
			reg, err := Load(ctx, tx.Settings)
			if err != nil {
				return err
			}
			reg.Guilds[1].Notice = "writer-committed"
			delete(reg.Guilds[1].Members, "2")
			for _, id := range []string{"1", "2"} {
				c, _, err := tx.Character.Load(ctx, id)
				if err != nil {
					return err
				}
				c.Name, c.Level = "Changed", 91
				if err := tx.Character.Save(ctx, c); err != nil {
					return err
				}
			}
			return Save(ctx, tx.Settings, reg, now.Add(time.Minute))
		})
		if err != nil {
			return fmt.Errorf("writer blocked by display snapshot: %w", err)
		}
		t.Logf("writer completed with read snapshot open: %s", time.Since(started))
		after, err := Load(ctx, view.Settings)
		if err != nil {
			return err
		}
		again, err := view.Characters.LoadGuildCharacters(ctx, []string{"1", "2"})
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(chars, again) {
			return errors.New("membership/identity changed inside snapshot")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	err = u.WithinGuildRead(ctx, func(view r.GuildReadView) error {
		reg, err := Load(ctx, view.Settings)
		if err != nil {
			return err
		}
		chars, err := view.Characters.LoadGuildCharacters(ctx, []string{"1", "2"})
		if err != nil {
			return err
		}
		if len(reg.Guilds[1].Members) != 1 || reg.Guilds[1].Notice != "writer-committed" || chars["2"].Level != 91 {
			return errors.New("new snapshot missed writer commit")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

type observedBackfillUOW struct {
	r.GuildReadUnitOfWork
	scans, compares int
	beforeCompare   func() error
}
type observedBackfillSettings struct {
	r.SettingsRepository
	u *observedBackfillUOW
}

func (s observedBackfillSettings) WalkGuildSettlementReceipts(ctx context.Context, id uint32, since time.Time, visit func(r.SettingsRecord) error) error {
	s.u.scans++
	return s.SettingsRepository.(r.GuildSettlementReceiptReader).WalkGuildSettlementReceipts(ctx, id, since, visit)
}
func (u *observedBackfillUOW) WithinGuildRead(ctx context.Context, fn func(r.GuildReadView) error) error {
	return u.GuildReadUnitOfWork.WithinGuildRead(ctx, func(view r.GuildReadView) error {
		view.Settings = observedBackfillSettings{view.Settings, u}
		return fn(view)
	})
}
func (u *observedBackfillUOW) CompareGuildRegistry(ctx context.Context, before, after r.SettingsRecord) (bool, error) {
	u.compares++
	if u.beforeCompare != nil {
		if err := u.beforeCompare(); err != nil {
			return false, err
		}
	}
	return u.GuildReadUnitOfWork.CompareGuildRegistry(ctx, before, after)
}

func testGuildContributionBackfill(t *testing.T, repos r.Group) {
	t.Helper()
	now, owner := guildReadSeed(t, repos)
	ctx := context.Background()
	u := &observedBackfillUOW{GuildReadUnitOfWork: repos.Guild.(r.GuildReadUnitOfWork)}
	// Simulate two old memberships. The second leaves/rejoins during backfill.
	err := repos.Guild.WithinGuild(ctx, []r.GuildOwner{owner}, func(tx r.Group) error {
		reg, err := Load(ctx, tx.Settings)
		if err != nil {
			return err
		}
		m := reg.Guilds[1].Members["1"]
		m.ContributionPoints, m.DungeonContributionPoints = 74, nil
		reg.Guilds[1].Members["1"] = m
		m = reg.Guilds[1].Members["2"]
		m.ContributionPoints, m.DungeonContributionPoints = 40, nil
		reg.Guilds[1].Members["2"] = m
		return Save(ctx, tx.Settings, reg, now)
	})
	if err != nil {
		t.Fatal(err)
	}
	addReceipt := func(key string, o r.GuildOwner, guildID, contribution uint32, at time.Time) {
		t.Helper()
		value, _ := json.Marshal(map[string]any{"Version": 1, "Owner": o, "GuildDungeon": DungeonClearResult{GuildID: guildID, Contribution: contribution}})
		if err := repos.Settings.Save(ctx, r.SettingsRecord{Scope: r.GuildSettlementReceiptScope(o, key), Values: map[string]string{"receipt": string(value)}, UpdatedAt: at}); err != nil {
			t.Fatal(err)
		}
	}
	addReceipt("backfill-current", owner, 1, 60, now.Add(time.Second))
	addReceipt("backfill-rejoin", r.GuildOwner{CharacterID: "2", AccountID: "a2"}, 1, 40, now.Add(time.Second))
	addReceipt("backfill-before-join", owner, 1, 20, now.Add(-time.Hour))
	addReceipt("backfill-old-guild", owner, 2, 20, now.Add(time.Second))
	addReceipt("backfill-old-owner", r.GuildOwner{CharacterID: "1", AccountID: "old"}, 1, 20, now.Add(time.Second))
	addReceipt("backfill-departed", r.GuildOwner{CharacterID: "3", AccountID: "a3"}, 1, 20, now.Add(time.Second))
	// A competing ordinary contribution commit must survive CAS retry.
	u.beforeCompare = func() error {
		u.beforeCompare = nil
		return repos.Guild.WithinGuild(ctx, []r.GuildOwner{owner, {CharacterID: "2", AccountID: "a2"}}, func(tx r.Group) error {
			reg, err := Load(ctx, tx.Settings)
			if err != nil {
				return err
			}
			m := reg.Guilds[1].Members["1"]
			m.ContributionPoints += 2
			reg.Guilds[1].Members["1"] = m
			delete(reg.Guilds[1].Members, "2")
			c, _, err := tx.Character.Load(ctx, "2")
			if err != nil {
				return err
			}
			if _, err := reg.JoinDirect(1, c, now.Add(time.Minute)); err != nil {
				return err
			}
			return Save(ctx, tx.Settings, reg, now.Add(time.Minute))
		})
	}
	if count, err := InitializeContributionProjection(ctx, u, now.Add(2*time.Minute)); err != nil || count != 1 || u.scans != 2 || u.compares != 2 {
		t.Fatalf("backfill=%d scans=%d CAS=%d err=%v", count, u.scans, u.compares, err)
	}
	u.scans, u.compares = 0, 0
	if count, err := InitializeContributionProjection(ctx, u, now.Add(3*time.Minute)); err != nil || count != 0 || u.scans != 0 || u.compares != 0 {
		t.Fatalf("idempotency=%d scans=%d CAS=%d err=%v", count, u.scans, u.compares, err)
	}
	err = u.WithinGuildRead(ctx, func(view r.GuildReadView) error {
		reg, err := Load(ctx, view.Settings)
		if err != nil {
			return err
		}
		m := reg.Guilds[1].Members["1"]
		if m.ContributionPoints != 76 || m.DungeonContributionPoints == nil || *m.DungeonContributionPoints != 60 {
			return fmt.Errorf("lost contribution or bad backfill: %+v", m)
		}
		rejoined := reg.Guilds[1].Members["2"]
		if rejoined.ContributionPoints != 0 || rejoined.DungeonContributionPoints == nil || *rejoined.DungeonContributionPoints != 0 || !rejoined.JoinedAt.Equal(now.Add(time.Minute)) {
			return fmt.Errorf("backfill overwrote rejoined membership: %+v", rejoined)
		}
		members, err := HongbaoContributionMembers(ctx, view.Settings, reg.Guilds[1])
		if err != nil {
			return err
		}
		for _, m := range members {
			if m.CharacterID == "1" && m.ContributionPoints != 16 {
				return fmt.Errorf("earned=%d", m.ContributionPoints)
			}
		}
		return nil
	})
	if err != nil || u.scans != 0 {
		t.Fatalf("initialized display rescanned: %d err=%v", u.scans, err)
	}
}

type conflictingBackfill struct {
	r.GuildReadUnitOfWork
	attempts int
}

func (u *conflictingBackfill) CompareGuildRegistry(context.Context, r.SettingsRecord, r.SettingsRecord) (bool, error) {
	u.attempts++
	return false, nil
}

func TestContributionBackfillConflictBoundAndInvalidTotal(t *testing.T) {
	repos := memory.NewMemoryGroup()
	now, _ := guildReadSeed(t, repos)
	reg, _ := Load(context.Background(), repos.Settings)
	m := reg.Guilds[1].Members["1"]
	m.DungeonContributionPoints = nil
	reg.Guilds[1].Members["1"] = m
	if err := Save(context.Background(), repos.Settings, reg, now); err != nil {
		t.Fatal(err)
	}
	u := &conflictingBackfill{GuildReadUnitOfWork: repos.Guild.(r.GuildReadUnitOfWork)}
	if _, err := InitializeContributionProjection(context.Background(), u, now); !errors.Is(err, ErrContributionBackfillConflict) || u.attempts != 3 {
		t.Fatal("unbounded/stale overwrite", u.attempts, err)
	}
	points := uint32(1)
	m.DungeonContributionPoints = &points
	reg.Guilds[1].Members["1"] = m
	if err := Save(context.Background(), repos.Settings, reg, now); !errors.Is(err, ErrContributionState) {
		t.Fatal("counter exceeded total", err)
	}
}
