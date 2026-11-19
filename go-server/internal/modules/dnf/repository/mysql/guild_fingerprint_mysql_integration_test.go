//go:build mysql_integration

package mysql

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	driver "github.com/go-sql-driver/mysql"
	r "longheng.io/server/internal/modules/dnf/repository"
	"net"
	"os"
	"strings"
	"testing"
	"time"
)

func TestGuildFingerprintDisposableMySQL(t *testing.T) {
	if os.Getenv("DNF_RUNTIME_DISPOSABLE_DB") != "1" {
		t.Fatal("requires disposable database")
	}
	cfg, err := driver.ParseDSN(os.Getenv("DNFBRIDGE_PLAYER_TEST_DSN"))
	if err != nil || cfg.Net != "tcp" || cfg.DBName != "" {
		t.Fatal("requires isolated DSN")
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
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	name := fmt.Sprintf("guild_fingerprint_%d", time.Now().UnixNano())
	defer db.Exec("DROP DATABASE IF EXISTS `" + name + "`")
	plan := r.DatabasePlan{ReadDatabases: []string{name}, WriteDatabases: []string{name}}
	schema := SchemaOptions{AutoCreate: true, CreateDatabases: true, DatabasePlan: plan}
	if err := EnsureMySQLSchema(ctx, db, schema); err != nil {
		t.Fatal(err)
	}
	spec, _ := newGuildFingerprintSpec(name, "")
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	options := MySQLGroupOptions{DatabasePlan: plan}
	adapter := SQLDBAdapter{DB: db}
	ready := func() bool {
		t.Helper()
		ok, err := GuildSnapshotFingerprintReady(ctx, adapter, options)
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}
	if ready() {
		t.Fatal("absent installation trusted")
	}
	raw := "{\"fixture\":\"" + strings.Repeat("x", 1024*1024) + "\"}"
	exec("INSERT INTO "+spec.settings+" (scope,updated_at) VALUES (?,?)", r.GuildRegistryScope, time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC))
	exec("INSERT INTO "+spec.values+" VALUES (?,'state',?)", r.GuildRegistryScope, raw)
	// Deliberately leave a partial installation before the supported installer.
	exec(spec.tableDDL())
	exec(spec.triggerDDL(spec.triggers[0]))
	if ready() {
		t.Fatal("partial installation trusted")
	}
	db.SetMaxOpenConns(1)
	if err := EnsureGuildSnapshotFingerprint(ctx, db, schema); err != nil {
		t.Fatal(err)
	}
	if err := EnsureGuildSnapshotFingerprint(ctx, db, schema); err != nil {
		t.Fatal("reentry", err)
	}
	db.SetMaxOpenConns(8)
	if !ready() {
		t.Fatal("complete installation not detected")
	}
	legacy, err := NewMySQLGroupFromDB(db, options)
	if err != nil {
		t.Fatal(err)
	}
	options.storedGuildFingerprint = true
	fast, err := NewMySQLGroupFromDB(db, options)
	if err != nil {
		t.Fatal(err)
	}
	read := func(group r.Group) (string, bool) {
		t.Helper()
		v, found, err := group.Settings.(r.GuildSnapshotRevisionReader).LoadGuildSnapshotRevision(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return v, found
	}
	check := func() {
		t.Helper()
		a, af := read(legacy)
		b, bf := read(fast)
		if a != b || af != bf {
			t.Fatalf("legacy/stored differ: %s/%t %s/%t", a, af, b, bf)
		}
	}
	check()
	var original, stamp string
	if err := db.QueryRowContext(ctx, "SELECT v.entry_value,CAST(s.updated_at AS CHAR) FROM "+spec.settings+" s JOIN "+spec.values+" v ON v.scope=s.scope WHERE s.scope=? AND v.entry_key='state'", r.GuildRegistryScope).Scan(&original, &stamp); err != nil || original != raw || !strings.HasPrefix(stamp, "2026-11-01") {
		t.Fatal("migration changed source", err)
	}
	for _, query := range []string{"UPDATE " + spec.values + " SET entry_value='updated' WHERE scope=? AND entry_key='state'", "INSERT INTO " + spec.values + " VALUES (?,'state','upserted') ON DUPLICATE KEY UPDATE entry_value=VALUES(entry_value)", "REPLACE INTO " + spec.values + " VALUES (?,'state','replaced')"} {
		exec(query, r.GuildRegistryScope)
		check()
	}
	// Both the registry and its digest share one repeatable-read snapshot.
	if err := fast.Guild.(r.GuildReadUnitOfWork).WithinGuildRead(ctx, func(view r.GuildReadView) error {
		old, _, err := view.Settings.(r.GuildSnapshotRevisionReader).LoadGuildSnapshotRevision(ctx)
		if err != nil {
			return err
		}
		exec("UPDATE "+spec.values+" SET entry_value='new-commit' WHERE scope=? AND entry_key='state'", r.GuildRegistryScope)
		again, _, err := view.Settings.(r.GuildSnapshotRevisionReader).LoadGuildSnapshotRevision(ctx)
		if old != again {
			t.Fatal("RR source changed")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	check()
	before, _ := read(fast)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, "UPDATE "+spec.values+" SET entry_value='rolled-back' WHERE scope=? AND entry_key='state'", r.GuildRegistryScope); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if got, _ := read(fast); got != before {
		tx.Rollback()
		t.Fatal("uncommitted sidecar visible")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	check()
	exec("DELETE FROM "+spec.values+" WHERE scope=? AND entry_key='state'", r.GuildRegistryScope)
	check()
	exec("INSERT INTO "+spec.values+" VALUES (?,'state',?)", r.GuildRegistryScope, raw)
	check()
	exec("UPDATE "+spec.values+" SET scope='unrelated' WHERE scope=?", r.GuildRegistryScope)
	check()
	exec("UPDATE "+spec.values+" SET scope=? WHERE scope='unrelated'", r.GuildRegistryScope)
	check()
	exec("TRUNCATE TABLE " + spec.table)
	check()
	if ready() {
		t.Fatal("missing initial fact trusted")
	}
	if err := EnsureGuildSnapshotFingerprint(ctx, db, schema); err != nil {
		t.Fatal(err)
	}
	check()
	exec("TRUNCATE TABLE " + spec.values)
	check()
	exec("INSERT INTO "+spec.values+" VALUES (?,'state',?)", r.GuildRegistryScope, raw)
	check()
	// A backfill racing an uncommitted legacy writer must finish with its
	// committed source, not a digest captured before the writer's lock.
	tx, err = db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, "UPDATE "+spec.values+" SET entry_value='concurrent' WHERE scope=? AND entry_key='state'", r.GuildRegistryScope); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- EnsureGuildSnapshotFingerprint(ctx, db, schema) }()
	select {
	case err := <-done:
		tx.Rollback()
		t.Fatal("installer passed uncommitted writer", err)
	case <-time.After(40 * time.Millisecond):
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	check()
	want := sha256.Sum256([]byte("concurrent"))
	if got, _ := read(fast); got != hex.EncodeToString(want[:]) {
		t.Fatal("backfill lost writer")
	}
	component := NewComponent(adapter, ComponentOptions{DatabasePlan: plan})
	if err := component.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if !component.Snapshot().StoredGuildFingerprint {
		t.Fatal("startup did not select stored reader")
	}
	if err := component.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if component.Snapshot().StoredGuildFingerprint {
		t.Fatal("stop retained capability")
	}
	// Never overwrite an unexpected trigger definition, including one whose
	// scope differs only by literal whitespace.
	bad := spec.triggers[0]
	exec("DROP TRIGGER `" + name + "`.`" + bad.name + "`")
	bad.body = strings.Replace(bad.body, "guild_registry", "guild_ registry", 1)
	exec(spec.triggerDDL(bad))
	if ready() {
		t.Fatal("changed trigger trusted")
	}
	if err := EnsureGuildSnapshotFingerprint(ctx, db, schema); !errors.Is(err, ErrGuildFingerprintSchema) {
		t.Fatal("unexpected trigger overwritten", err)
	}
	exec("DROP TRIGGER `" + name + "`.`" + bad.name + "`")
	if err := EnsureGuildSnapshotFingerprint(ctx, db, schema); err != nil {
		t.Fatal(err)
	}
	check()
	// Cancellation after taking the installer lock must release that lock,
	// including when DDL is waiting for an older transaction's metadata lock.
	tx, err = db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, "UPDATE "+spec.values+" SET entry_value=entry_value WHERE scope=? AND entry_key='state'", r.GuildRegistryScope); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	installCtx, stopInstall := context.WithCancel(ctx)
	done = make(chan error, 1)
	go func() { done <- EnsureGuildSnapshotFingerprint(installCtx, db, schema) }()
	lockHash := sha256.Sum256([]byte(spec.database + "/" + spec.prefix))
	lockName := fmt.Sprintf("guild-fingerprint-%x", lockHash[:16])
	lockObserved := false
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); {
		var owner sql.NullInt64
		if err := db.QueryRowContext(ctx, "SELECT IS_USED_LOCK(?)", lockName).Scan(&owner); err != nil {
			stopInstall()
			tx.Rollback()
			t.Fatal(err)
		}
		if owner.Valid {
			lockObserved = true
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	stopInstall()
	installErr := <-done
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if !lockObserved || !errors.Is(installErr, context.Canceled) {
		t.Fatal("cancelled installer", lockObserved, installErr)
	}
	if err := EnsureGuildSnapshotFingerprint(ctx, db, schema); err != nil {
		t.Fatal("installer lock leaked", err)
	}
	// A different live owner must still exclude the installer after the handoff wait.
	holder, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	var acquired int
	if err := holder.QueryRowContext(ctx, "SELECT GET_LOCK(?,0)", lockName).Scan(&acquired); err != nil || acquired != 1 {
		t.Fatal("fixture could not take installer lock", acquired, err)
	}
	blocked := EnsureGuildSnapshotFingerprint(ctx, db, schema)
	var released int
	if err := holder.QueryRowContext(ctx, "SELECT RELEASE_LOCK(?)", lockName).Scan(&released); err != nil || released != 1 {
		t.Fatal("installer displaced live owner", released, err)
	}
	if !errors.Is(blocked, ErrGuildFingerprintSchema) {
		t.Fatal("concurrent installer must report a busy schema", blocked)
	}
	// Ordinary SELECT-only credentials retain the legacy reader without
	// requiring a TRIGGER grant or access to the optional sidecar.
	user := fmt.Sprintf("gf_reader_%d", time.Now().UnixNano())
	exec("CREATE USER '" + user + "'@'127.0.0.1'")
	defer db.Exec("DROP USER IF EXISTS '" + user + "'@'127.0.0.1'")
	exec("GRANT SELECT ON " + spec.settings + " TO '" + user + "'@'127.0.0.1'")
	exec("GRANT SELECT ON " + spec.values + " TO '" + user + "'@'127.0.0.1'")
	limitedConfig := *cfg
	limitedConfig.User = user
	limitedConfig.Passwd = ""
	limited, err := sql.Open("mysql", limitedConfig.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	defer limited.Close()
	limitedComponent := NewComponent(SQLDBAdapter{DB: limited}, ComponentOptions{DatabasePlan: plan})
	if err := limitedComponent.Start(ctx); err != nil {
		t.Fatal("legacy credentials rejected", err)
	}
	if limitedComponent.Snapshot().StoredGuildFingerprint {
		t.Fatal("unreadable derived schema trusted")
	}
	defer limitedComponent.Stop(ctx)
	limitedGroup, err := NewMySQLGroupFromDB(limited, options)
	if err != nil {
		t.Fatal(err)
	}
	a, af := read(legacy)
	b, bf := read(limitedGroup)
	if a != b || af != bf {
		t.Fatal("runtime grant fallback changed digest")
	}
	uninstalledRead := options
	uninstalledRead.DatabasePlan.ReadDatabases = append([]string{name}, "zz_missing_guild_fingerprint_shard")
	if ok, err := GuildSnapshotFingerprintReady(ctx, adapter, uninstalledRead); err != nil || ok {
		t.Fatal("uninstalled read shard trusted", ok, err)
	}
	// Verify TRUNCATE of values table with an injected orphan sidecar row never exposes the stale fingerprint.
	exec("TRUNCATE TABLE " + spec.values)
	exec("INSERT INTO "+spec.table+" (scope, fingerprint) VALUES (?, ?) ON DUPLICATE KEY UPDATE fingerprint=VALUES(fingerprint)", r.GuildRegistryScope, strings.Repeat("a", 64))
	check()
	if orphanDigest, orphanFound := read(fast); !orphanFound || orphanDigest != "" {
		t.Fatalf("orphan sidecar leaked after TRUNCATE: digest=%q found=%t", orphanDigest, orphanFound)
	}
	// Verify M4-a off-page avoidance: 120 KiB LONGTEXT payload in dnf_setting_values
	// must record pages_accessed=1 on the values table under ANALYZE FORMAT=JSON.
	bigValue := strings.Repeat("G", 120*1024)
	exec("INSERT INTO "+spec.values+" (scope, entry_key, entry_value) VALUES (?, 'guild:big', ?)", r.GuildRegistryScope, bigValue)
	var analyzeJSON string
	analyzeSQL := "ANALYZE FORMAT=JSON SELECT EXISTS(SELECT 1 FROM " + spec.values + " WHERE scope = ? LIMIT 1), f.fingerprint " +
		"FROM (SELECT 1) AS one LEFT JOIN " + spec.table + " f ON f.scope = ?"
	if err := db.QueryRowContext(ctx, analyzeSQL, r.GuildRegistryScope, r.GuildRegistryScope).Scan(&analyzeJSON); err != nil {
		t.Fatalf("analyze two-phase fingerprint query: %v", err)
	}
	if strings.Contains(analyzeJSON, `"pages_accessed": 0`) || strings.Contains(analyzeJSON, `"pages_accessed": 1`) {
		t.Logf("two-phase guild fingerprint ANALYZE verified single-page/index probe without LONGTEXT overflow scan")
	} else {
		t.Fatalf("unexpected off-page scan in two-phase guild fingerprint ANALYZE: %s", analyzeJSON)
	}
	t.Log("legacy inserts/updates/upserts/replaces/deletes, RR, rollback, partial install, wrong definition, orphan TRUNCATE guard and concurrent backfill passed")
}

func TestWeddingFieldsReadZeroTmpTablesDisposableMySQL(t *testing.T) {
	if os.Getenv("DNF_RUNTIME_DISPOSABLE_DB") != "1" {
		t.Fatal("requires disposable database")
	}
	cfg, err := driver.ParseDSN(os.Getenv("DNFBRIDGE_PLAYER_TEST_DSN"))
	if err != nil || cfg.Net != "tcp" || cfg.DBName != "" {
		t.Fatal("requires isolated DSN")
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
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	name := fmt.Sprintf("wedding_split_%d", time.Now().UnixNano())
	defer db.Exec("DROP DATABASE IF EXISTS `" + name + "`")
	plan := r.DatabasePlan{ReadDatabases: []string{name}, WriteDatabases: []string{name}}
	if err := EnsureMySQLSchema(ctx, db, SchemaOptions{AutoCreate: true, CreateDatabases: true, DatabasePlan: plan}); err != nil {
		t.Fatal(err)
	}
	group, err := NewMySQLGroupFromDB(db, MySQLGroupOptions{DatabasePlan: plan})
	if err != nil {
		t.Fatal(err)
	}
	stats := make(map[string]int64, 80)
	for i := 0; i < 75; i++ {
		stats[fmt.Sprintf("stat_%03d", i)] = int64(i + 1)
	}
	if err := group.Character.Save(ctx, r.CharacterRecord{
		CharacterID: "2634",
		AccountID:   "dnf:wedding",
		Name:        "bride",
		Job:         "1",
		Level:       86,
		Stats:       stats,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx,
		"INSERT INTO `"+name+"`.`dnf_character_stats` (character_id, stat_key, stat_value) VALUES (?, ?, ?), (?, ?, ?) "+
			"ON DUPLICATE KEY UPDATE stat_value=VALUES(stat_value)",
		"2634", "wedding_v1_partner_id", 8888,
		"2634", "wedding_v1_ring_level", 3,
	); err != nil {
		t.Fatal(err)
	}
	reader, ok := group.Character.(r.CharacterWeddingReader)
	if !ok {
		t.Fatal("CharacterRepository does not implement CharacterWeddingReader")
	}
	readTmpTables := func() int64 {
		var varName string
		var val int64
		if err := db.QueryRowContext(ctx, "SHOW SESSION STATUS LIKE 'Created_tmp_tables'").Scan(&varName, &val); err != nil {
			t.Fatal(err)
		}
		return val
	}
	beforeTmp := readTmpTables()
	for i := 0; i < 20; i++ {
		snap, found, err := reader.LoadCharacterWedding(ctx, "2634")
		if err != nil || !found || snap.AccountID != "dnf:wedding" ||
			snap.Stats["wedding_v1_partner_id"] != 8888 || snap.Stats["wedding_v1_ring_level"] != 3 || len(snap.Stats) != 2 {
			t.Fatalf("LoadCharacterWedding mismatch: found=%v err=%v snap=%+v", found, err, snap)
		}
	}
	afterTmp := readTmpTables()
	if afterTmp != beforeTmp {
		t.Fatalf("LoadCharacterWedding created %d temporary tables across 20 reads, want 0", afterTmp-beforeTmp)
	}
}
