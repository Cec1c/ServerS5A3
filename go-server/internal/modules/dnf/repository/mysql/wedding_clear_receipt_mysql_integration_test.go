//go:build mysql_integration

package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"testing"
	"time"

	driver "github.com/go-sql-driver/mysql"
	"longheng.io/server/internal/modules/dnf/pvf"
	"longheng.io/server/internal/modules/dnf/quest"
	r "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/wedding"
)

type weddingReceiptQuestSource map[string]string

func (s weddingReceiptQuestSource) ReadText(path string) (string, error) {
	if text, ok := s[path]; ok {
		return text, nil
	}
	return "", fmt.Errorf("wedding receipt fixture missing %s", path)
}

// The DSN is consumed only from the process environment and never logged. No
// existing schema is accepted. Cleanup owns only the database created below.
func TestWeddingClearReceiptDisposableMySQL(t *testing.T) {
	if os.Getenv("DNF_RUNTIME_DISPOSABLE_DB") != "1" {
		t.Fatal("requires explicit disposable database authorization")
	}
	cfg, err := driver.ParseDSN(os.Getenv("DNFBRIDGE_PLAYER_TEST_DSN"))
	if err != nil || cfg.Net != "tcp" || cfg.DBName != "" {
		t.Fatal("requires TCP test DSN without a selected database")
	}
	host, _, err := net.SplitHostPort(cfg.Addr)
	if err != nil || host != "127.0.0.1" {
		t.Fatal("requires loopback test database connection")
	}
	cfg.ParseTime, cfg.Loc = true, time.UTC
	if cfg.Params == nil {
		cfg.Params = make(map[string]string)
	}
	// Only these test connections change their session mode; the server's
	// global mode and every existing database remain untouched.
	cfg.Params["sql_mode"] = "'STRICT_TRANS_TABLES,ERROR_FOR_DIVISION_BY_ZERO,NO_ENGINE_SUBSTITUTION'"
	openDB := func() *sql.DB {
		t.Helper()
		db, err := sql.Open("mysql", cfg.FormatDSN())
		if err != nil {
			t.Fatal("open isolated test connection")
		}
		return db
	}
	db := openDB()
	t.Cleanup(func() { _ = db.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	name := fmt.Sprintf("wedding_clear_receipt_%d", time.Now().UnixNano())
	safeName := regexp.MustCompile(`^wedding_clear_receipt_[0-9]+$`)
	if !safeName.MatchString(name) {
		t.Fatal("invalid generated disposable database name")
	}
	// CREATE without IF NOT EXISTS refuses a collision. Register DROP only
	// after this exact database has been created successfully by this test.
	if _, err := db.ExecContext(ctx, "CREATE DATABASE `"+name+"` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci"); err != nil {
		t.Fatal("create isolated wedding receipt database failed")
	}
	t.Cleanup(func() {
		if !safeName.MatchString(name) {
			t.Error("refused cleanup outside generated wedding receipt database")
			return
		}
		dropCtx, stop := context.WithTimeout(context.Background(), 20*time.Second)
		defer stop()
		if _, err := db.ExecContext(dropCtx, "DROP DATABASE `"+name+"`"); err != nil {
			t.Error("isolated wedding receipt database cleanup failed")
		}
	})
	plan := r.DatabasePlan{ReadDatabases: []string{name}, WriteDatabases: []string{name}}
	if err := EnsureMySQLSchema(ctx, db, SchemaOptions{AutoCreate: true, DatabasePlan: plan}); err != nil {
		t.Fatal(err)
	}
	var limit int
	if err := db.QueryRowContext(ctx, "SELECT character_maximum_length FROM information_schema.columns WHERE table_schema=? AND table_name='dnf_quest_state_extra' AND column_name='extra_key'", name).Scan(&limit); err != nil || limit != 128 {
		t.Fatalf("isolated schema extra_key limit=%d, expected original VARCHAR(128)", limit)
	}
	newGroup := func(pool *sql.DB) r.Group {
		t.Helper()
		g, err := NewMySQLGroupFromDB(pool, MySQLGroupOptions{DatabasePlan: plan})
		if err != nil {
			t.Fatal(err)
		}
		return g
	}
	g := newGroup(db)
	now := time.Date(2026, 11, 2, 15, 0, 0, 0, time.UTC)
	for i, job := range []string{"0", "11"} {
		id, account := fmt.Sprint(i+1), fmt.Sprint("wedding-receipt-account-", i+1)
		for _, save := range []func() error{
			func() error { return g.Account.Save(ctx, r.AccountRecord{AccountID: account}) },
			func() error {
				return g.Character.Save(ctx, r.CharacterRecord{CharacterID: id, AccountID: account, Name: "Wedding" + id, Job: job, Level: 30, UpdatedAt: now})
			},
			func() error {
				return g.Inventory.Save(ctx, r.InventoryRecord{CharacterID: id, Slots: map[string]r.ItemStack{}})
			},
		} {
			if err := save(); err != nil {
				t.Fatal(err)
			}
		}
	}
	marriage := wedding.Owner{Repositories: g, Rules: &wedding.Rules{MinimumLevel: 30}}
	if _, err := marriage.Engage(ctx, wedding.PairRequest{
		Actor:   wedding.Actor{AccountID: "wedding-receipt-account-1", CharacterID: "1"},
		Partner: wedding.Actor{AccountID: "wedding-receipt-account-2", CharacterID: "2"}, Now: now,
	}); err != nil {
		t.Fatal(err)
	}
	text, err := os.ReadFile(filepath.Join("..", "..", "quest", "testdata", "wedding_20737.qst"))
	if err != nil {
		t.Fatal(err)
	}
	index, err := pvf.Build(ctx, weddingReceiptQuestSource{
		quest.DefaultList:                       "20737 `event/chn_2014_wedding_01.qst`\n",
		"n_quest/event/chn_2014_wedding_01.qst": string(text),
	}, pvf.BuildOptions{Lists: []string{quest.DefaultList}})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := quest.Load(ctx, index)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := quest.NewOwner(g)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 2; i++ {
		if _, err := owner.ApplyAcceptAt(ctx, catalog, quest.CharacterEligibility{Level: 30}, quest.Command{
			AccountID: fmt.Sprint("wedding-receipt-account-", i), SelectedCharacterID: uint16(i), QuestID: 20737,
		}, now); err != nil {
			t.Fatal(err)
		}
	}
	loadFresh := func(id string) r.QuestRecord {
		t.Helper()
		freshDB := openDB()
		defer freshDB.Close()
		record, found, err := newGroup(freshDB).Quest.Load(ctx, id)
		if err != nil || !found {
			t.Fatalf("fresh quest repository load found=%t err=%v", found, err)
		}
		return record
	}
	longKey := fmt.Sprintf("dungeon-clear:char:1:dungeon:310:maze:0:map:310010:seed:4294967295:completed_at_unix_nano:%d:target:4294967295:source:clear_map_phase_a", now.Add(time.Minute).UnixNano())
	legacyMarker := "wedding_clear_run:" + longKey
	if len(legacyMarker) <= limit {
		t.Fatal("fixture must exercise real completion-key shape exceeding VARCHAR(128)")
	}
	before := loadFresh("1")
	for _, delta := range []bool{false, true} {
		t.Run(fmt.Sprintf("legacy-marker-1406-rollback/delta=%t", delta), func(t *testing.T) {
			bad := r.CloneQuest(before)
			state := bad.States[20737]
			state.ProgressValue, state.UpdatedAt = 4, now.Add(time.Minute)
			state.Extra["completion_key"], state.Extra[legacyMarker] = longKey, "1"
			bad.States[20737], bad.UpdatedAt = state, state.UpdatedAt
			var err error
			if delta {
				err = r.SaveQuestChanges(ctx, g.Quest, before, bad, r.QuestFieldStates)
			} else {
				err = r.SaveQuestFields(ctx, g.Quest, bad, r.QuestFieldStates)
			}
			var sqlErr *driver.MySQLError
			if !errors.As(err, &sqlErr) || sqlErr.Number != 1406 {
				t.Fatalf("legacy overlong marker must fail with MySQL1406, got %v", err)
			}
			if actual := loadFresh("1"); !reflect.DeepEqual(actual, before) {
				t.Fatal("failed long-key insert did not roll back complete quest record and extras")
			}
			t.Logf("strict MySQL1406 marker_length=%d schema_limit=%d full_record_rollback=true", len(legacyMarker), limit)
		})
	}
	for run := 0; run < 5; run++ {
		key := longKey + fmt.Sprint(":run=", run)
		input := quest.ClearMapCompletionInput{WeddingPartnerID: "2", DungeonID: 310, MapID: 310010,
			CompletionKey: key, CompletedAt: now.Add(time.Duration(run+1) * time.Minute)}
		result, err := owner.ApplyClearMapCompletion(ctx, catalog, "1", input)
		if err != nil || len(result.Completions) != 1 || result.Completions[0].RemainingTrigger != int64(4-run) {
			t.Fatalf("long run %d result=%+v err=%v", run, result, err)
		}
		stored := loadFresh("1")
		state := stored.States[20737]
		if state.ProgressValue != int64(4-run) || state.Extra["completion_key"] != key ||
			(state.Extra["reward_state"] == "pending") != (run == 4) {
			t.Fatalf("fresh long-run state=%+v", state)
		}
		for marker := range state.Extra {
			if len(marker) > limit {
				t.Fatal("durable wedding receipt exceeds unchanged schema limit")
			}
		}
		replayed, err := owner.ApplyClearMapCompletion(ctx, catalog, "1", input)
		if err != nil || !replayed.Idempotent || !reflect.DeepEqual(loadFresh("1"), stored) {
			t.Fatalf("same long run counted twice: %+v err=%v", replayed, err)
		}
		t.Logf("long_run=%d remaining=%d fresh_reload=true replay_unchanged=true", run+1, 4-run)
	}
	var maxKey int
	if err := db.QueryRowContext(ctx, "SELECT MAX(CHAR_LENGTH(extra_key)) FROM `"+name+"`.`dnf_quest_state_extra`").Scan(&maxKey); err != nil || maxKey > limit {
		t.Fatalf("persisted extra_key max=%d expected <=%d, err=%v", maxKey, limit, err)
	}
	if other := loadFresh("2"); other.States[20737].ProgressValue != 5 {
		t.Fatal("quest progress escaped the requested character")
	}
	for _, id := range []string{"1", "2"} {
		bag, _, err := g.Inventory.Load(ctx, id)
		if err != nil || len(bag.Slots) != 0 {
			t.Fatal("clear receipt granted an inventory reward")
		}
	}
	t.Logf("isolated_schema_limit=%d maximum_persisted_key=%d no_reward_granted=true", limit, maxKey)
}
