//go:build mysql_integration

package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	driver "github.com/go-sql-driver/mysql"
	r "longheng.io/server/internal/modules/dnf/repository"
)

// The opt-in fixture creates only fresh, generated schemas. No game schema,
// account, character or balance is read or restored by this test.
func TestExperimentalPeakSeventyDisposableMySQL(t *testing.T) {
	if os.Getenv("DNF_RUNTIME_DISPOSABLE_DB") != "1" || os.Getenv("DNF_PEAK101_SQL_BENCH") != "1" {
		t.Skip("explicit disposable SQL benchmark required")
	}
	cfg, err := driver.ParseDSN(os.Getenv("DNFBRIDGE_PLAYER_TEST_DSN"))
	if err != nil || cfg.Net != "unix" || cfg.Addr != "/run/mysqld/mysqld.sock" || cfg.DBName != "" {
		t.Fatal("requires local socket and no selected game schema")
	}
	cfg.ParseTime = true
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(8)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	type result struct {
		Mode              string
		Calls, Rows       int64
		Micros, P95Micros int64
		CharacterCount    int
	}
	var results []result
	for _, mode := range []string{"off", "on"} {
		t.Setenv("DNFBRIDGE_EXPERIMENTAL_PEAK", mode)
		schema := fmt.Sprintf("peak101_disposable_%s_%d", mode, time.Now().UnixNano())
		if !strings.HasPrefix(schema, "peak101_disposable_") {
			t.Fatal("schema guard")
		}
		func() {
			defer func() {
				if _, e := db.Exec("DROP DATABASE `" + schema + "`"); e != nil {
					t.Error(e)
				}
			}()
			plan := r.DatabasePlan{ReadDatabases: []string{schema}, WriteDatabases: []string{schema}}
			if e := EnsureMySQLSchema(ctx, db, SchemaOptions{AutoCreate: true, CreateDatabases: true, DatabasePlan: plan}); e != nil {
				t.Fatal(e)
			}
			seed, e := NewMySQLGroup(SQLDBAdapter{DB: db}, MySQLGroupOptions{DatabasePlan: plan})
			if e != nil {
				t.Fatal(e)
			}
			for actor := 1; actor <= 70; actor++ {
				stats := map[string]int64{"gold": 123456, "peak_counter": 0}
				for k := 0; k < 100; k++ {
					stats[fmt.Sprintf("synthetic_%03d", k)] = int64(k)
				}
				if e := seed.Character.Save(ctx, r.CharacterRecord{CharacterID: fmt.Sprint(actor), AccountID: fmt.Sprintf("synthetic-account-%d", actor), Name: fmt.Sprintf("synthetic-%d", actor), Job: "0", Level: 90, Stats: stats, Location: r.CharacterLocation{TownID: 38, ChannelID: 7}}); e != nil {
					t.Fatal(e)
				}
			}
			observation := &SQLObservation{}
			group, e := NewMySQLGroup(&ObservedSQLDB{SQLDB: SQLDBAdapter{DB: db}, Observation: observation}, MySQLGroupOptions{DatabasePlan: plan})
			if e != nil {
				t.Fatal(e)
			}
			var wg sync.WaitGroup
			gate := make(chan struct{})
			errs := make(chan error, 70)
			durations := make(chan int64, 70)
			start := time.Now()
			for actor := 1; actor <= 70; actor++ {
				wg.Add(1)
				go func(actor int) {
					defer wg.Done()
					<-gate
					began := time.Now()
					id := fmt.Sprint(actor)
					for round := 0; round < 3; round++ {
						e := group.CharacterAssets.WithinCharacterAssets(ctx, id, func(characters r.CharacterRepository, _ r.InventoryRepository, _ r.EquipmentRepository) error {
							var character r.CharacterRecord
							for read := 0; read < 3; read++ {
								var found bool
								var e error
								character, found, e = characters.Load(ctx, id)
								if e != nil {
									return e
								}
								if !found || character.CharacterID != id || character.AccountID != fmt.Sprintf("synthetic-account-%d", actor) {
									return fmt.Errorf("wrong synthetic owner")
								}
							}
							character.Stats["peak_counter"]++
							if e := r.SaveCharacterFields(ctx, characters, character, r.CharacterFieldStats); e != nil {
								return e
							}
							fresh, found, e := characters.Load(ctx, id)
							if e != nil {
								return e
							}
							if !found || fresh.Stats["peak_counter"] != int64(round+1) || fresh.Stats["gold"] != 123456 {
								return fmt.Errorf("post-write snapshot mismatch")
							}
							return nil
						})
						if e != nil {
							errs <- e
							return
						}
					}
					durations <- time.Since(began).Microseconds()
				}(actor)
			}
			close(gate)
			wg.Wait()
			elapsed := time.Since(start).Microseconds()
			close(errs)
			close(durations)
			for e := range errs {
				t.Fatal(e)
			}
			var micros []int64
			for n := range durations {
				micros = append(micros, n)
			}
			sort.Slice(micros, func(i, j int) bool { return micros[i] < micros[j] })
			if len(micros) != 70 {
				t.Fatalf("completed synthetic actors=%d", len(micros))
			}
			var calls, rows int64
			for family := range observation.families {
				for kind := range observation.families[family] {
					calls += observation.families[family][kind].count.Load()
					rows += observation.families[family][kind].rows.Load()
				}
			}
			for actor := 1; actor <= 70; actor++ {
				c, found, e := seed.Character.Load(ctx, fmt.Sprint(actor))
				if e != nil || !found || c.Stats["peak_counter"] != 3 || c.Stats["gold"] != 123456 {
					t.Fatal("final synthetic asset mismatch", e)
				}
			}
			results = append(results, result{Mode: mode, Calls: calls, Rows: rows, Micros: elapsed, P95Micros: micros[66], CharacterCount: 70})
		}()
	}
	if results[1].Calls >= results[0].Calls || results[1].Rows >= results[0].Rows {
		t.Fatalf("expected fewer SQL calls/rows: %+v", results)
	}
	t.Logf("70 synthetic actors, 3 transactions each, identical data and 8-connection pool: %+v", results)
	t.Log("Wall/P95 durations include connection-pool wait, cache warmup and test scheduling; not production player latency.")
}
