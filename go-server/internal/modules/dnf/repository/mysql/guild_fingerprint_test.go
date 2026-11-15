package mysql

import (
	"context"
	"errors"
	driver "github.com/go-sql-driver/mysql"
	r "longheng.io/server/internal/modules/dnf/repository"
	"strings"
	"testing"
)

func TestGuildFingerprintPlanIsAdditiveAndExplicit(t *testing.T) {
	options := SchemaOptions{DatabasePlan: r.DatabasePlan{WriteDatabases: []string{"guild_a", "guild_b"}}}
	plan, err := GuildSnapshotFingerprintDDL(options)
	if err != nil || len(plan) != 8 {
		t.Fatal(len(plan), err)
	}
	for _, q := range plan {
		for _, bad := range []string{"ALTER TABLE", "DROP ", "TRUNCATE ", "updated_at", "SET entry_value"} {
			if strings.Contains(q, bad) {
				t.Fatal("unexpected migration", q)
			}
		}
	}
	if err := EnsureGuildSnapshotFingerprint(context.Background(), nil, options); err != nil {
		t.Fatal("disabled installer touched database", err)
	}
	options.AutoCreate = true
	if err := EnsureGuildSnapshotFingerprint(context.Background(), nil, options); !errors.Is(err, ErrMySQLDBRequired) {
		t.Fatal(err)
	}
	for _, prefix := range []string{"bad-prefix", strings.Repeat("a", 64)} {
		options.TablePrefix = prefix
		if _, err := GuildSnapshotFingerprintDDL(options); err == nil {
			t.Fatal("invalid physical identifier accepted")
		}
	}
}

func TestGuildFingerprintDefinitionComparisonPreservesLiterals(t *testing.T) {
	a := "BEGIN INSERT INTO `db`.`hashes` VALUES ('global:guild_registry:v1','state'); END"
	b := "BEGIN\n INSERT INTO db.hashes VALUES ('global:guild_registry:v1', 'state');\nEND"
	if compactGuildFingerprintSQL(a) != compactGuildFingerprintSQL(b) {
		t.Fatal("formatting changed definition")
	}
	for _, changed := range []string{strings.Replace(a, "guild_registry", "guild_ registry", 1), strings.Replace(a, "'state'", "'State'", 1), strings.Replace(a, "`hashes`", "`HASHES`", 1), a + " -- extra"} {
		if compactGuildFingerprintSQL(a) == compactGuildFingerprintSQL(changed) {
			t.Fatal("changed definition trusted", changed)
		}
	}
}

func TestGuildFingerprintFastReadAndMissingSchemaFallback(t *testing.T) {
	for _, test := range []struct {
		name        string
		err         error
		wantQueries int
	}{{"stored", nil, 1}, {"missing-table", &driver.MySQLError{Number: 1146}, 2}, {"missing-column", &driver.MySQLError{Number: 1054}, 2}, {"no-derived-table-grant", &driver.MySQLError{Number: 1142}, 2}, {"incompatible-derived-collation", &driver.MySQLError{Number: 1267}, 2}, {"real-failure", errors.New("read failed"), 1}} {
		t.Run(test.name, func(t *testing.T) {
			database := &fakeSQLDB{rows: []fakeSQLRow{{values: []any{"digest"}}}}
			if test.err != nil {
				database.rows = append([]fakeSQLRow{{err: test.err}}, database.rows...)
			}
			store := &mysqlSettingsStore{mysqlStoreBase: mysqlStoreBase{router: mysqlRouter{db: database, readDBs: []string{"guild_a"}, writeDBs: []string{"guild_a"}, tablePrefix: "dnf", storedGuildFingerprint: true}}}
			digest, found, err := store.LoadGuildSnapshotRevision(context.Background())
			if test.name == "real-failure" {
				if !errors.Is(err, test.err) {
					t.Fatal(err)
				}
			} else if err != nil || !found || digest != "digest" {
				t.Fatal(digest, found, err)
			}
			if len(database.execs) != test.wantQueries || !strings.Contains(database.execs[0].Query, "COALESCE(f.fingerprint,CAST(SHA2(v.entry_value, 256) AS BINARY))") {
				t.Fatal(database.execs)
			}
			if test.wantQueries == 2 && strings.Contains(database.execs[1].Query, "guild_registry_fingerprints") {
				t.Fatal("fallback still requires sidecar")
			}
		})
	}
}

func TestGuildFingerprintOptionalMetadataFallback(t *testing.T) {
	for _, number := range []uint16{1044, 1054, 1142, 1143, 1146, 1227, 1267, 1205, 1213, 2006} {
		database := &fakeSQLDB{rows: []fakeSQLRow{{err: &driver.MySQLError{Number: number}}}}
		ready, err := GuildSnapshotFingerprintReady(context.Background(), database, MySQLGroupOptions{DatabasePlan: r.DatabasePlan{ReadDatabases: []string{"guild_a"}, WriteDatabases: []string{"guild_a"}}})
		optional := guildFingerprintOptionalSchemaError(&driver.MySQLError{Number: number})
		if ready || optional && err != nil || !optional && err == nil {
			t.Fatalf("number=%d ready=%t err=%v", number, ready, err)
		}
	}
}
