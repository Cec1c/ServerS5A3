//go:build mysql_integration

package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	driver "github.com/go-sql-driver/mysql"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// This test must use a disposable MySQL server. It creates and removes its own
// uniquely named database, and never accepts a database name from the DSN.
func TestMySQLCharacterCrystalSelectionNonePersistsAcrossFreshRepositories(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("DNFBRIDGE_PLAYER_TEST_DSN"))
	if dsn == "" {
		t.Fatal("DNFBRIDGE_PLAYER_TEST_DSN must point at a disposable MySQL server")
	}
	cfg, err := driver.ParseDSN(dsn)
	if err != nil {
		t.Fatal("invalid disposable test DSN")
	}
	cfg.DBName, cfg.ParseTime = "", true
	controlDB, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal("open disposable test server without exposing DSN")
	}
	defer controlDB.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	name := fmt.Sprintf("mr219_crystal_none_%d", time.Now().UnixNano())
	if !regexp.MustCompile(`^mr219_crystal_none_[0-9]+$`).MatchString(name) {
		t.Fatalf("unsafe generated disposable database name %q", name)
	}
	defer func() {
		dropCtx, dropCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer dropCancel()
		if _, err := controlDB.ExecContext(dropCtx, "DROP DATABASE IF EXISTS `"+name+"`"); err != nil {
			t.Errorf("drop disposable database %s: %v", name, err)
		}
	}()
	plan := dnfrepo.DatabasePlan{ReadDatabases: []string{name}, WriteDatabases: []string{name}}
	if err := EnsureMySQLSchema(ctx, controlDB, SchemaOptions{AutoCreate: true, CreateDatabases: true, DatabasePlan: plan}); err != nil {
		t.Fatal(err)
	}

	openClientDB := func() *sql.DB {
		t.Helper()
		clientDB, err := sql.Open("mysql", cfg.FormatDSN())
		if err != nil {
			t.Fatal("open disposable client pool without exposing DSN")
		}
		if err := clientDB.PingContext(ctx); err != nil {
			clientDB.Close()
			t.Fatalf("ping disposable client pool: %v", err)
		}
		return clientDB
	}
	newRepositories := func(clientDB *sql.DB) dnfrepo.Group {
		t.Helper()
		repositories, err := NewMySQLGroupFromDB(clientDB, MySQLGroupOptions{DatabasePlan: plan})
		if err != nil {
			t.Fatal(err)
		}
		return repositories
	}
	writeDB := openClientDB()
	defer writeDB.Close()
	repositories := newRepositories(writeDB)
	record := dnfrepo.CharacterRecord{
		CharacterID: "mr219-crystal-none",
		AccountID:   "mr219-test-account",
		Slot:        0,
		Name:        "NoneKeeper",
		Job:         "0",
		Level:       1,
		Stats: map[string]int64{
			mysqlCharacterCrystalSelectionStatColumn: -1,
			"gold":                                  42,
		},
		UpdatedAt: time.Now().UTC(),
	}
	if err := repositories.Character.Save(ctx, record); err != nil {
		t.Fatal(err)
	}
	if err := writeDB.Close(); err != nil {
		t.Fatal(err)
	}

	assertFreshSelection := func() {
		t.Helper()
		readDB := openClientDB()
		defer readDB.Close()
		loaded, found, err := newRepositories(readDB).Character.Load(ctx, record.CharacterID)
		if err != nil || !found {
			t.Fatalf("fresh repository load found=%t err=%v", found, err)
		}
		selection, present := loaded.Stats[mysqlCharacterCrystalSelectionStatColumn]
		if !present || selection != -1 {
			t.Fatalf("fresh repository crystal selection present=%t value=%d, want explicit -1", present, selection)
		}
		if loaded.Stats["gold"] != 42 {
			t.Fatalf("unrelated gold=%d, want 42", loaded.Stats["gold"])
		}
	}
	assertFreshSelection()

	var persisted int64
	query := "SELECT `" + mysqlCharacterCrystalSelectionStatColumn + "` FROM `" + name + "`.`dnf_characters` WHERE character_id = ?"
	if err := controlDB.QueryRowContext(ctx, query, record.CharacterID).Scan(&persisted); err != nil {
		t.Fatal(err)
	}
	if persisted != -1 {
		t.Fatalf("raw MySQL premium_crystal_selection=%d, want -1", persisted)
	}

	partialDB := openClientDB()
	defer partialDB.Close()
	partialRepositories := newRepositories(partialDB)
	loaded, found, err := partialRepositories.Character.Load(ctx, record.CharacterID)
	if err != nil || !found {
		t.Fatalf("repository load for partial save found=%t err=%v", found, err)
	}
	loaded.Stats["gold"] = 43
	loaded.UpdatedAt = time.Now().UTC()
	if err := dnfrepo.SaveCharacterFields(ctx, partialRepositories.Character, loaded, dnfrepo.CharacterFieldStats); err != nil {
		t.Fatal(err)
	}
	if err := partialDB.Close(); err != nil {
		t.Fatal(err)
	}
	reloadDB := openClientDB()
	defer reloadDB.Close()
	reloaded, found, err := newRepositories(reloadDB).Character.Load(ctx, record.CharacterID)
	if err != nil || !found {
		t.Fatalf("fresh repository reload found=%t err=%v", found, err)
	}
	selection, present := reloaded.Stats[mysqlCharacterCrystalSelectionStatColumn]
	if !present || selection != -1 {
		t.Fatalf("after unrelated partial save crystal selection present=%t value=%d, want explicit -1", present, selection)
	}
	if reloaded.Stats["gold"] != 43 {
		t.Fatalf("after partial save gold=%d, want 43", reloaded.Stats["gold"])
	}
}

