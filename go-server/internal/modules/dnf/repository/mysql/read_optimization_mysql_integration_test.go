//go:build mysql_integration

package mysql

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"reflect"
	"runtime"
	"testing"
	"time"

	driver "github.com/go-sql-driver/mysql"
	"longheng.io/server/internal/modules/dnf/progression"
	r "longheng.io/server/internal/modules/dnf/repository"
)

// Hide optional projections to execute the original full-read path against
// exactly the same synthetic records, schema, connection pool and server.
type optimizationLegacyCharacters struct{ r.CharacterRepository }

func optimizationCounts(o *SQLObservation) (calls, rows int64) {
	for family := range o.families {
		for kind := range o.families[family] {
			calls += o.families[family][kind].count.Load()
			rows += o.families[family][kind].rows.Load()
		}
	}
	return
}

func TestReadOptimizationDisposableMySQL(t *testing.T) {
	if os.Getenv("DNF_RUNTIME_DISPOSABLE_DB") != "1" {
		t.Fatal("requires explicit disposable database")
	}
	cfg, err := driver.ParseDSN(os.Getenv("DNFBRIDGE_PLAYER_TEST_DSN"))
	if err != nil || cfg.Net != "tcp" || cfg.DBName != "" {
		t.Fatal("invalid disposable DSN")
	}
	host, _, err := net.SplitHostPort(cfg.Addr)
	if err != nil || host != "127.0.0.1" {
		t.Fatal("requires loopback")
	}
	cfg.ParseTime = true
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(8)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	schema := fmt.Sprintf("read_opt_%d", time.Now().UnixNano())
	defer func() {
		if _, err := db.Exec("DROP DATABASE IF EXISTS `" + schema + "`"); err != nil {
			t.Error(err)
		}
	}()
	plan := r.DatabasePlan{ReadDatabases: []string{schema}, WriteDatabases: []string{schema}}
	if err = EnsureMySQLSchema(ctx, db, SchemaOptions{AutoCreate: true, CreateDatabases: true, DatabasePlan: plan}); err != nil {
		t.Fatal(err)
	}
	obs := &SQLObservation{}
	group, err := NewMySQLGroup(&ObservedSQLDB{SQLDB: SQLDBAdapter{DB: db}, Observation: obs}, MySQLGroupOptions{DatabasePlan: plan})
	if err != nil {
		t.Fatal(err)
	}
	stats := map[string]int64{"exp": 0, "gold": 999, "user_state_bits": 1}
	for i := 0; i < 1000; i++ {
		stats[fmt.Sprintf("synthetic_extra_%04d", i)] = int64(i)
	}
	character := r.CharacterRecord{CharacterID: "99", AccountID: "isolated-account", Name: "Synthetic", Job: "0", Level: 20, Stats: stats, UpdatedAt: time.Unix(1700000000, 0),
		Location: r.CharacterLocation{ChannelID: 7, TownID: 38},
		Roster: r.CharacterRoster{Header: r.CharacterRosterHeader{TotalOrSlotLimit: 32, UsedOrRemain: 1}, Entry: r.CharacterRosterEntry{
			ObjectID: 17, Flags: []int64{1}, EquipSummary: []r.CharacterRosterEquipSummary{{Slot: 1, ItemIDOrIcon: 3330}},
		}},
	}
	if err = group.Character.Save(ctx, character); err != nil {
		t.Fatal(err)
	}
	legacy := optimizationLegacyCharacters{group.Character}
	full, found, err := group.Character.Load(ctx, "99")
	if err != nil || !found {
		t.Fatal(found, err)
	}
	for _, id := range []string{"99", "missing"} {
		old, of, oe := r.ProbeLevelUpRewards(ctx, legacy, id)
		fresh, nf, ne := r.ProbeLevelUpRewards(ctx, group.Character, id)
		if oe != nil || ne != nil || of != nf || old != fresh {
			t.Fatal("probe parity", old, fresh, oe, ne)
		}
	}
	type measurement struct {
		Path                    string
		Iterations              int
		Calls, Rows, AllocBytes int64
		WallMillis              float64
	}
	var measured []measurement
	for _, round := range []int{0, 1} {
		paths := []string{"legacy-probe", "narrow-probe", "legacy-growth", "narrow-growth"}
		if round == 1 {
			paths = []string{"narrow-growth", "legacy-growth", "narrow-probe", "legacy-probe"}
		}
		for _, path := range paths {
			runtime.GC()
			var start, end runtime.MemStats
			runtime.ReadMemStats(&start)
			calls, rows := optimizationCounts(obs)
			at := time.Now()
			const n = 20
			for i := 0; i < n; i++ {
				switch path {
				case "legacy-probe", "narrow-probe":
					var store r.CharacterRepository = group.Character
					if path == "legacy-probe" {
						store = legacy
					}
					p, found, err := r.ProbeLevelUpRewards(ctx, store, "99")
					if err != nil || !found || p.Pending || p.Level != 20 || p.AccountID != "isolated-account" {
						t.Fatal(p, found, err)
					}
				case "legacy-growth", "narrow-growth":
					var store r.CharacterRepository = group.Character
					if path == "legacy-growth" {
						store = legacy
					}
					c, found, err := r.LoadProgressionCharacter(ctx, store, "99")
					if err != nil || !found || c.CharacterID != full.CharacterID || c.AccountID != full.AccountID || c.Level != full.Level || !c.UpdatedAt.Equal(full.UpdatedAt) || !reflect.DeepEqual(c.Stats, full.Stats) {
						t.Fatal("growth projection mismatch", found, err)
					}
				}
			}
			afterCalls, afterRows := optimizationCounts(obs)
			elapsed := time.Since(at)
			runtime.ReadMemStats(&end)
			m := measurement{path, n, afterCalls - calls, afterRows - rows, int64(end.TotalAlloc - start.TotalAlloc), float64(elapsed) / float64(time.Millisecond)}
			want := int64(n * 6)
			if path == "narrow-probe" {
				want = n
			}
			if path == "narrow-growth" {
				want = n * 2
			}
			if m.Calls != want {
				t.Fatalf("%s logical SQL calls=%d want %d", path, m.Calls, want)
			}
			measured = append(measured, m)
		}
	}
	// Fresh reads observe outside writers; no cached absence may skip an event.
	statsTable := mysqlTable(schema, defaultTablePrefix, mysqlCharacterStatsTable)
	for _, key := range []string{"level_up_event_085", "level_up_event_256", "level_up_event_x"} {
		if _, err = db.ExecContext(ctx, "INSERT INTO "+statsTable+" (character_id,stat_key,stat_value) VALUES ('99',?,1)", key); err != nil {
			t.Fatal(err)
		}
	}
	p, _, err := r.ProbeLevelUpRewards(ctx, group.Character, "99")
	if err != nil || p.Pending {
		t.Fatal("noncanonical candidates", p, err)
	}
	// Invalid-key filtering is a probe test, not a new asset-save contract.
	// Remove precisely these synthetic keys before exercising the unchanged
	// delivery transaction: its existing protected-history SQL can reject such
	// malformed keys as duplicate inserts. Keep that condition out of this
	// ordinary canonical-event transaction fixture.
	if _, err = db.ExecContext(ctx, "DELETE FROM "+statsTable+" WHERE character_id='99' AND stat_key IN ('level_up_event_085','level_up_event_256','level_up_event_x')"); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, "INSERT INTO "+statsTable+" (character_id,stat_key,stat_value) VALUES ('99','level_up_event_85',1)"); err != nil {
		t.Fatal(err)
	}
	p, _, err = r.ProbeLevelUpRewards(ctx, group.Character, "99")
	if err != nil || !p.Pending {
		t.Fatal("new external event not observed", p, err)
	}
	// Deliver through the existing asset transaction and permanent receipts.
	catalog, err := progression.ParseLevelUpRewards("[level up reward]\n85 1110 1111 3330 500\n[/level up reward]")
	if err != nil {
		t.Fatal(err)
	}
	// A probe is not a delivery authority. If another worker consumes the
	// event after our probe, the existing locked transaction must deliver none.
	cmd := progression.LevelUpRewardCommand{CharacterID: "99", Now: time.Unix(1700000010, 0), ResolveAttachment: func(v progression.LevelUpReward) (r.MailAttachment, error) {
		return r.MailAttachment{ItemID: v.ItemID, Count: v.Count}, nil
	}}
	if _, err = db.ExecContext(ctx, "UPDATE "+statsTable+" SET stat_value=-1 WHERE character_id='99' AND stat_key='level_up_event_85'"); err != nil {
		t.Fatal(err)
	}
	deliveries, err := progression.DeliverLevelUpRewards(ctx, group, catalog, cmd)
	if err != nil || len(deliveries) != 0 {
		t.Fatal("preflight bypassed consumed event", deliveries, err)
	}
	if _, err = db.ExecContext(ctx, "UPDATE "+mysqlTable(schema, defaultTablePrefix, mysqlCharTable)+" SET level=85 WHERE character_id='99'"); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, "UPDATE "+statsTable+" SET stat_value=1 WHERE character_id='99' AND stat_key='level_up_event_85'"); err != nil {
		t.Fatal(err)
	}
	deliveries, err = progression.DeliverLevelUpRewards(ctx, group, catalog, cmd)
	if err != nil || len(deliveries) != 1 {
		t.Fatal("pending delivery", deliveries, err)
	}
	deliveries, err = progression.DeliverLevelUpRewards(ctx, group, catalog, cmd)
	if err != nil || len(deliveries) != 0 {
		t.Fatal("duplicate delivery", deliveries, err)
	}
	p, _, err = r.ProbeLevelUpRewards(ctx, group.Character, "99")
	if err != nil || p.Pending {
		t.Fatal("consumed canonical event", p, err)
	}
	version := ""
	if err = db.QueryRowContext(ctx, "SELECT VERSION()").Scan(&version); err != nil {
		t.Fatal(err)
	}
	data, err := json.MarshalIndent(map[string]any{"databaseVersion": version, "isolated": true, "syntheticOnly": true, "fixture": "1000 extra stats plus location and existing roster/equipment/list rows; full read executes six SQL calls; absent roster uses four", "logicalSQLCallsNotProtocolPrepareCounts": true, "measurements": measured, "correctness": true}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	t.Log(string(data))
	if path := os.Getenv("READ_OPT_MEASUREMENTS"); path != "" {
		if err = os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
}
