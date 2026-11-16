//go:build mysql_integration

package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"os"
	"reflect"
	"testing"
	"time"

	driver "github.com/go-sql-driver/mysql"
	r "longheng.io/server/internal/modules/dnf/repository"
)

func TestOptimizationSchemaDisposableMySQL(t *testing.T) {
	if os.Getenv("DNF_RUNTIME_DISPOSABLE_DB") != "1" {
		t.Fatal("requires disposable DB")
	}
	cfg, err := driver.ParseDSN(os.Getenv("DNFBRIDGE_PLAYER_TEST_DSN"))
	if err != nil || cfg.Net != "tcp" || cfg.DBName != "" {
		t.Fatal("requires isolated DSN")
	}
	host, _, err := net.SplitHostPort(cfg.Addr)
	if err != nil || host != "127.0.0.1" {
		t.Fatal("requires isolated loopback")
	}
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	db.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	create := func() string {
		name := fmt.Sprintf("optimization_schema_%d", time.Now().UnixNano())
		exec("CREATE DATABASE `" + name + "`")
		t.Cleanup(func() {
			if _, err := db.Exec("DROP DATABASE `" + name + "`"); err != nil {
				t.Error(err)
			}
		})
		exec("CREATE TABLE `" + name + "`.dnf_settings(scope VARCHAR(128) PRIMARY KEY,updated_at DATETIME(6) NOT NULL) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci")
		exec("CREATE TABLE `" + name + "`.dnf_setting_values(scope VARCHAR(128) NOT NULL,entry_key VARCHAR(128) NOT NULL,entry_value LONGTEXT NOT NULL,PRIMARY KEY(scope,entry_key)) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci")
		exec("CREATE TABLE `" + name + "`.dnf_character_stats(character_id VARCHAR(128) NOT NULL,stat_key VARCHAR(128) NOT NULL,stat_value BIGINT NOT NULL,PRIMARY KEY(character_id,stat_key)) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci")
		exec("INSERT INTO `"+name+"`.dnf_settings VALUES (?, '2026-11-01 01:02:03.123456')", r.GuildRegistryScope)
		exec("INSERT INTO `"+name+"`.dnf_setting_values VALUES (?,'state','保持原值')", r.GuildRegistryScope)
		exec("INSERT INTO `" + name + "`.dnf_character_stats VALUES ('owned','level_up_event_85',85),('owned','ordinary',987)")
		return name
	}
	options := func(read, write []string) SchemaOptions {
		return SchemaOptions{AutoCreate: true, DatabasePlan: r.DatabasePlan{ReadDatabases: read, WriteDatabases: write}}
	}
	objectCount := func(database string) int {
		t.Helper()
		var n int
		err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA=? AND TABLE_NAME='dnf_guild_registry_fingerprints'", database).Scan(&n)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	t.Run("readonly_replica_then_explicit_all_targets", func(t *testing.T) {
		write, read := create(), create()
		opt := options([]string{write, read}, []string{write})
		before, err := InspectOptimizationSchema(ctx, db, opt)
		if err != nil || before.Ready || objectCount(write) != 0 {
			t.Fatal(before, err)
		}
		partial, err := ApplyOptimizationSchema(ctx, db, opt)
		if err != nil || partial.Ready || objectCount(write) != 1 || objectCount(read) != 0 {
			t.Fatal("replica was mutated or partial misreported", partial, err)
		}
		opt = options([]string{write, read}, []string{write, read})
		after, err := ApplyOptimizationSchema(ctx, db, opt)
		if err != nil || !after.Ready {
			t.Fatal(after, err)
		}
		for _, name := range []string{write, read} {
			var source, stamp string
			var total int
			if err := db.QueryRowContext(ctx, "SELECT v.entry_value,CAST(s.updated_at AS CHAR) FROM `"+name+"`.dnf_settings s JOIN `"+name+"`.dnf_setting_values v ON v.scope=s.scope WHERE v.entry_key='state'").Scan(&source, &stamp); err != nil || source != "保持原值" || stamp != "2026-11-01 01:02:03.123456" {
				t.Fatal("source changed", source, stamp, err)
			}
			if err := db.QueryRowContext(ctx, "SELECT SUM(stat_value) FROM `"+name+"`.dnf_character_stats").Scan(&total); err != nil || total != 1072 {
				t.Fatal("reward stat changed", total, err)
			}
		}
		counts := func() map[string]string {
			t.Helper()
			rows, err := db.QueryContext(ctx, "SHOW GLOBAL STATUS WHERE Variable_name IN ('Com_insert','Com_update','Com_delete','Com_create_table','Com_create_trigger','Com_alter_table')")
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			out := map[string]string{}
			for rows.Next() {
				var k, v string
				if err := rows.Scan(&k, &v); err != nil {
					t.Fatal(err)
				}
				out[k] = v
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			return out
		}
		old := counts()
		reentered, err := ApplyOptimizationSchema(ctx, db, opt)
		if err != nil || !reentered.Ready || !reflect.DeepEqual(old, counts()) {
			t.Fatal("ready reentry wrote SQL", err)
		}
		// The caller can lose an apply reply and reconcile by inspection only.
		if reconciled, err := InspectOptimizationSchema(ctx, db, opt); err != nil || !reflect.DeepEqual(reconciled, reentered) || !reflect.DeepEqual(old, counts()) {
			t.Fatal("readonly reconciliation differs", err)
		}
		exec("UPDATE `" + write + "`.dnf_setting_values SET entry_value='legacy writer committed' WHERE entry_key='state'")
		if latest, err := InspectOptimizationSchema(ctx, db, opt); err != nil || !latest.Ready {
			t.Fatal("legacy source writer invalidated derived state", err)
		}
	})
	t.Run("known_partial_guild_install", func(t *testing.T) {
		name := create()
		s, _ := newGuildFingerprintSpec(name, "")
		exec(s.tableDDL())
		exec(s.triggerDDL(s.triggers[0]))
		opt := options([]string{name}, []string{name})
		before, err := InspectOptimizationSchema(ctx, db, opt)
		if err != nil || before.Ready || before.Databases[0].GuildTriggers != 1 {
			t.Fatal(before, err)
		}
		after, err := ApplyOptimizationSchema(ctx, db, opt)
		if err != nil || !after.Ready {
			t.Fatal(after, err)
		}
	})
	for _, variant := range []string{"wrong_order", "unique", "prefix", "ignored", "unknown_trigger", "wrong_sidecar"} {
		t.Run(variant, func(t *testing.T) {
			first, bad := create(), create()
			switch variant {
			case "wrong_order":
				exec("ALTER TABLE `" + bad + "`.dnf_character_stats ADD INDEX idx_stats_pending_level(character_id,stat_key,stat_value)")
			case "unique":
				exec("ALTER TABLE `" + bad + "`.dnf_character_stats ADD UNIQUE INDEX idx_stats_pending_level(stat_key,stat_value,character_id)")
			case "prefix":
				exec("ALTER TABLE `" + bad + "`.dnf_character_stats ADD INDEX idx_stats_pending_level(stat_key(12),stat_value,character_id)")
			case "ignored":
				exec(recoveryIndexDDL(bad, "dnf"))
				exec("ALTER TABLE `" + bad + "`.dnf_character_stats ALTER INDEX idx_stats_pending_level IGNORED")
			case "unknown_trigger":
				exec("CREATE TRIGGER `" + bad + "`.dnf_guild_fingerprint_i AFTER INSERT ON `" + bad + "`.dnf_setting_values FOR EACH ROW SET @unrelated=1")
			case "wrong_sidecar":
				exec("CREATE TABLE `" + bad + "`.dnf_guild_registry_fingerprints(scope INT PRIMARY KEY) ENGINE=InnoDB")
			}
			opt := options([]string{first, bad}, []string{first, bad})
			report, err := ApplyOptimizationSchema(ctx, db, opt)
			if err == nil || report.Ready || objectCount(first) != 0 {
				t.Fatal("all shards were not checked before mutation", report, err)
			}
		})
	}
}
