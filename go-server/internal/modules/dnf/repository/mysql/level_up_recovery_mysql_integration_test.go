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
	"strings"
	"testing"
	"time"

	driver "github.com/go-sql-driver/mysql"
	"longheng.io/server/internal/modules/dnf/repository"
)

func TestLevelUpRecoveryIndexedDisposableMySQL(t *testing.T) {
	if os.Getenv("DNF_RUNTIME_DISPOSABLE_DB") != "1" {
		t.Fatal("requires disposable database")
	}
	cfg, err := driver.ParseDSN(os.Getenv("DNFBRIDGE_PLAYER_TEST_DSN"))
	if err != nil || cfg.Net != "tcp" || cfg.DBName != "" {
		t.Fatal("requires loopback DSN without existing schema")
	}
	host, _, err := net.SplitHostPort(cfg.Addr)
	if err != nil || host != "127.0.0.1" {
		t.Fatal("requires isolated loopback")
	}
	cfg.ParseTime = true
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	name := fmt.Sprintf("level_recovery_%d", time.Now().UnixNano())
	t.Cleanup(func() {
		if _, err := db.Exec("DROP DATABASE IF EXISTS `" + name + "`"); err != nil {
			t.Error(err)
		}
	})
	plan := repository.DatabasePlan{ReadDatabases: []string{name}, WriteDatabases: []string{name}}
	options := SchemaOptions{AutoCreate: true, CreateDatabases: true, DatabasePlan: plan}
	if err := EnsureMySQLSchema(ctx, db, options); err != nil {
		t.Fatal(err)
	}
	stats, chars := mysqlTable(name, "dnf", mysqlCharacterStatsTable), mysqlTable(name, "dnf", mysqlCharTable)
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	// Exercise the existing-table migration as well as fresh schema creation.
	exec("ALTER TABLE " + stats + " DROP INDEX idx_stats_pending_level")
	for i := 1; i <= 1000; i++ {
		id := fmt.Sprintf("%06d", i)
		exec("INSERT INTO "+chars+" (character_id,account_id,delete_flag) VALUES (?,?,?)", id, "account-"+id, i == 5)
		values, args := make([]string, 0, 250), make([]any, 0, 750)
		for n := 0; n < 250; n++ {
			values = append(values, "(?,?,?)")
			args = append(args, id, fmt.Sprintf("ordinary_%03d", n), n)
		}
		exec("INSERT INTO "+stats+" (character_id,stat_key,stat_value) VALUES "+strings.Join(values, ","), args...)
		if i <= 320 {
			value := int64(100)
			if i > 160 {
				value = -100
			}
			exec("INSERT INTO "+stats+" VALUES (?,?,?)", id, repository.LevelUpEventKey(85), value)
		}
	}
	// A wildcard lookalike is unrelated and must not become a reward event.
	exec("INSERT INTO " + stats + " VALUES ('000321','levelXupXeventX85',100)")
	group, err := NewMySQLGroupFromDB(db, MySQLGroupOptions{DatabasePlan: plan})
	if err != nil {
		t.Fatal(err)
	}
	reader := group.Character.(repository.PendingLevelUpReader)
	readAll := func() []string {
		t.Helper()
		var all []string
		cursor := ""
		for pages := 0; pages < 5; pages++ {
			ids, err := reader.ListPendingLevelUpCharacters(ctx, cursor, 128)
			if err != nil {
				t.Fatal(err)
			}
			all = append(all, ids...)
			if len(ids) < 128 {
				return all
			}
			cursor = ids[len(ids)-1]
		}
		t.Fatal("pagination did not terminate")
		return nil
	}
	query := fmt.Sprintf(pendingLevelUpCharactersSQL, stats, chars)
	analyze := func() (float64, string) {
		t.Helper()
		var raw string
		if err := db.QueryRowContext(ctx, "ANALYZE FORMAT=JSON "+query, levelUpEventLikePrefix, "", 128).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var tree any
		if err := json.Unmarshal([]byte(raw), &tree); err != nil {
			t.Fatal(err)
		}
		var reads float64
		var key string
		var walk func(any)
		walk = func(v any) {
			switch x := v.(type) {
			case []any:
				for _, y := range x {
					walk(y)
				}
			case map[string]any:
				if x["table_name"] == "s" {
					if n, ok := x["r_rows"].(float64); ok {
						loops, _ := x["r_loops"].(float64)
						reads += n * loops
					}
					key, _ = x["key"].(string)
				}
				for _, y := range x {
					walk(y)
				}
			}
		}
		walk(tree)
		return reads, key
	}
	before := readAll()
	oldReads, oldKey := analyze()
	if err := EnsureMySQLSchema(ctx, db, options); err != nil {
		t.Fatal(err)
	}
	if err := EnsureMySQLSchema(ctx, db, options); err != nil {
		t.Fatal("migration reentry", err)
	}
	after := readAll()
	newReads, newKey := analyze()
	if len(after) != 159 || !reflect.DeepEqual(before, after) {
		t.Fatalf("recovery changed: before=%d after=%d", len(before), len(after))
	}
	if newKey != "idx_stats_pending_level" || newReads <= 0 || newReads >= 1000 || oldReads < newReads*10 {
		t.Fatalf("not a selective seek: old=%g/%s new=%g/%s", oldReads, oldKey, newReads, newKey)
	}
	t.Logf("250321 stats: actual analyzed rows old=%g/%s indexed=%g/%s; pending=%d", oldReads, oldKey, newReads, newKey, len(after))
	// Uncommitted and rolled-back pending facts must never escape recovery.
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO "+stats+" VALUES ('000600',?,100)", repository.LevelUpEventKey(86)); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, readAll()) {
		tx.Rollback()
		t.Fatal("uncommitted event visible")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, readAll()) {
		t.Fatal("rollback changed recovery")
	}
	exec("INSERT INTO "+stats+" VALUES ('000600',?,100)", repository.LevelUpEventKey(86))
	if len(readAll()) != 160 {
		t.Fatal("committed legacy event not recovered")
	}
}
