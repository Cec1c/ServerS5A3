package mysql

import (
	"context"
	"errors"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/repository"
)

func TestLevelUpRecoveryIndexNewAndExistingShards(t *testing.T) {
	options := SchemaOptions{AutoCreate: true, DatabasePlan: repository.DatabasePlan{WriteDatabases: []string{"recovery_a", "recovery_b"}}}
	ddl, err := MySQLSchema(options)
	if err != nil {
		t.Fatal(err)
	}
	migrations, err := mysqlSchemaMigrations(options)
	if err != nil {
		t.Fatal(err)
	}
	for _, shard := range options.DatabasePlan.WriteDatabases {
		var created, migrated bool
		for _, statement := range ddl {
			if strings.HasPrefix(statement, "CREATE TABLE IF NOT EXISTS `"+shard+"`.`dnf_character_stats`") {
				created = strings.Contains(statement, "KEY idx_stats_pending_level (stat_key, stat_value, character_id)")
			}
		}
		for _, statement := range migrations {
			if statement == "ALTER TABLE `"+shard+"`.`dnf_character_stats` ADD INDEX idx_stats_pending_level (stat_key,stat_value,character_id)" {
				migrated = true
			}
		}
		if !created || !migrated {
			t.Fatalf("shard %s: create=%t migrate=%t", shard, created, migrated)
		}
	}
	exec := &fakeSchemaExec{failAt: len(ddl) + len(migrations)}
	if err := EnsureMySQLSchema(context.Background(), exec, options); !errors.Is(err, errFakeSchemaExec) {
		t.Fatalf("index failure must surface: %v", err)
	}
	options.AutoCreate = false
	exec = &fakeSchemaExec{}
	if err := EnsureMySQLSchema(context.Background(), exec, options); err != nil || len(exec.statements) != 0 {
		t.Fatalf("disabled migration executed DDL: %v", err)
	}
}
