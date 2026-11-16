//go:build mysql_integration

package mysql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"net"
	"os"
	"reflect"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	mysqlDriver "github.com/go-sql-driver/mysql"
	r "longheng.io/server/internal/modules/dnf/repository"
)

func activityArchiveDB(t *testing.T) (*sql.DB, *mysqlDriver.Config, context.Context) {
	t.Helper()
	if os.Getenv("DNF_RUNTIME_DISPOSABLE_DB") != "1" {
		t.Fatal("requires explicitly disposable database")
	}
	cfg, err := mysqlDriver.ParseDSN(os.Getenv("DNFBRIDGE_PLAYER_TEST_DSN"))
	if err != nil || cfg.Net != "tcp" || cfg.DBName != "" {
		t.Fatal("requires isolated DSN without database")
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
	db.SetMaxOpenConns(8)
	t.Cleanup(func() { db.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	t.Cleanup(cancel)
	return db, cfg, ctx
}

func activityArchiveExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatalf("fixture SQL %s: %v", strings.SplitN(query, " ", 3)[0], err)
	}
}

func activityArchiveSchema(t *testing.T, db *sql.DB, ctx context.Context, name, prefix string) {
	t.Helper()
	opts := SchemaOptions{AutoCreate: true, CreateDatabases: true, TablePrefix: prefix, DatabasePlan: r.DatabasePlan{WriteDatabases: []string{name}}}
	if err := EnsureMySQLSchema(ctx, db, opts); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := db.Exec("DROP DATABASE " + quoteSQLIdentifier(name)); err != nil {
			t.Error(err)
		}
	})
}

// This is a fixture cutover with no concurrent writers, not the production
// installer. It deliberately uses the same public contract and actual old rows.
func activityArchiveInstallFixture(t *testing.T, db *sql.DB, name, prefix string) ActivityArchiveLayout {
	t.Helper()
	s, err := ActivityArchivePlan(name, prefix)
	if err != nil {
		t.Fatal(err)
	}
	activityArchiveExec(t, db, s.ReceiptDDL)
	activityArchiveExec(t, db, s.StateDDL)
	activityArchiveExec(t, db, s.BackfillSQL())
	activityArchiveExec(t, db, "RENAME TABLE "+s.Hot+" TO "+s.Cold)
	activityArchiveExec(t, db, s.HotDDL())
	for _, trigger := range s.Triggers {
		activityArchiveExec(t, db, s.TriggerDDL(trigger))
	}
	digest, _ := hex.DecodeString(s.ContractSHA256)
	activityArchiveExec(t, db, "INSERT INTO "+s.State+" VALUES(1,?,'active',?)", ActivityArchiveFormat, digest)
	return s
}

func activityArchiveRecord(ctx context.Context, group r.Group, e r.ActivityEvent, apply func(r.Group) error) (bool, error) {
	var added bool
	err := group.CharacterSettlement.WithinCharacterSettlement(ctx, e.CharacterID, func(tx r.Group) error {
		var err error
		added, err = tx.Activity.Record(ctx, e)
		if err != nil {
			return err
		}
		if added && apply != nil {
			return apply(tx)
		}
		return nil
	})
	return added, err
}

func activityArchiveActor(t *testing.T, ctx context.Context, group r.Group, account, character string) {
	t.Helper()
	if err := group.Account.Save(ctx, r.AccountRecord{AccountID: account, Metadata: map[string]string{}}); err != nil {
		t.Fatal(err)
	}
	if err := group.Character.Save(ctx, r.CharacterRecord{CharacterID: character, AccountID: account, Name: "合成角色", Level: 90, Job: "0", Stats: map[string]int64{}}); err != nil {
		t.Fatal(err)
	}
}

func TestActivityArchiveRealRepositoryHistoryAndTransactions(t *testing.T) {
	db, _, ctx := activityArchiveDB(t)
	name := fmt.Sprintf("activity_archive_%d", time.Now().UnixNano())
	activityArchiveSchema(t, db, ctx, name, "arc_1")
	options := MySQLGroupOptions{TablePrefix: "arc_1", DatabasePlan: r.DatabasePlan{WriteDatabases: []string{name}}}
	legacy, err := NewQualifiedMySQLGroupFromDB(ctx, db, options)
	if err != nil {
		t.Fatal(err)
	}
	activityArchiveActor(t, ctx, legacy, "account", "19")
	at := time.Date(2026, 11, 14, 23, 59, 0, 123456000, time.UTC)
	event := func(id string, when time.Time, epic bool) r.ActivityEvent {
		e := r.ActivityEvent{ID: id, AccountID: "account", CharacterID: "19", Metric: r.ActivityFatigue, Amount: 1, OccurredAt: when, DungeonID: 310, Source: "dungeon_room"}
		if epic {
			e.Metric, e.Source, e.ItemID, e.Rarity = r.ActivityEpic, "dungeon_pickup", math.MaxUint32, 4
		}
		return e
	}
	old := []r.ActivityEvent{event("cold:a", at, false), event("cold:b", at, true), event("cold:c", at.Add(-24*time.Hour), true)}
	for _, e := range old {
		if added, err := activityArchiveRecord(ctx, legacy, e, nil); err != nil || !added {
			t.Fatalf("seed: %v %v", added, err)
		}
	}
	s := activityArchiveInstallFixture(t, db, name, options.TablePrefix)
	group, err := NewQualifiedMySQLGroupFromDB(ctx, db, options)
	if err != nil {
		t.Fatal(err)
	}
	if !group.Activity.(*mysqlActivityStore).router.activityArchive {
		t.Fatal("archive mode not published")
	}

	t.Run("replay-and-conflict-across-cold-boundary", func(t *testing.T) {
		replay := old[0]
		replay.OccurredAt = at.Add(72 * time.Hour)
		replay.CharacterName = "重命名"
		if added, err := activityArchiveRecord(ctx, group, replay, nil); err != nil || added {
			t.Fatalf("cold replay: %v %v", added, err)
		}
		replay.Amount++
		if _, err := activityArchiveRecord(ctx, group, replay, nil); !errors.Is(err, r.ErrActivityConflict) {
			t.Fatalf("conflicting receipt: %v", err)
		}
	})
	t.Run("sql-and-go-fingerprints", func(t *testing.T) {
		for _, e := range old {
			var raw []byte
			if err := db.QueryRow("SELECT payload FROM "+s.Receipts+" WHERE account_id=? AND event_id=?", e.AccountID, e.ID).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			want := r.ActivityPayloadFingerprintV1(e)
			if string(raw) != string(want[:]) {
				t.Fatal("SQL and Go payload formats differ")
			}
		}
	})
	t.Run("late-rollback-includes-character-receipt-event-and-counters", func(t *testing.T) {
		e := event("hot:abort", at.Add(time.Hour), false)
		before, err := group.Activity.Summary(ctx, r.ActivityQuery{AccountID: e.AccountID})
		if err != nil {
			t.Fatal(err)
		}
		failure := errors.New("late fixture failure")
		_, err = activityArchiveRecord(ctx, group, e, func(tx r.Group) error {
			character, _, err := tx.Character.Load(ctx, "19")
			if err != nil {
				return err
			}
			character.Stats["archive_probe"] = 17
			if err := r.SaveCharacterFields(ctx, tx.Character, character, r.CharacterFieldStats); err != nil {
				return err
			}
			return failure
		})
		if !errors.Is(err, failure) {
			t.Fatal(err)
		}
		for _, table := range []string{s.Hot, s.Receipts} {
			var n int
			if err := db.QueryRow("SELECT COUNT(*) FROM "+table+" WHERE event_id=?", e.ID).Scan(&n); err != nil || n != 0 {
				t.Fatalf("late row persisted %s: %d %v", table, n, err)
			}
		}
		character, _, err := group.Character.Load(ctx, "19")
		if err != nil || character.Stats["archive_probe"] != 0 {
			t.Fatal("late character update persisted", err)
		}
		after, err := group.Activity.Summary(ctx, r.ActivityQuery{AccountID: e.AccountID})
		if err != nil || !reflect.DeepEqual(before, after) {
			t.Fatal("late counter update persisted", err)
		}
	})
	hot := []r.ActivityEvent{event("hot:a", at.Add(time.Hour), true), event("hot:b", at, true), event("hot:c", at.Add(-48*time.Hour), false)}
	for _, e := range hot {
		if added, err := activityArchiveRecord(ctx, group, e, nil); err != nil || !added {
			t.Fatalf("hot write: %v %v", added, err)
		}
	}
	t.Run("complete-pagination-and-source-summary-with-overlap", func(t *testing.T) {
		// A complete identical cold copy may be committed before a recovery
		// reconciles its hot delete. Both readers must still count it once.
		activityArchiveExec(t, db, "INSERT INTO "+s.Cold+" SELECT * FROM "+s.Hot+" WHERE event_id=?", hot[0].ID)
		var actual []string
		q := r.ActivityQuery{AccountID: "account", Limit: 2}
		for i := 0; i < 5; i++ {
			page, err := group.Activity.Events(ctx, q)
			if err != nil {
				t.Fatal(err)
			}
			if len(page) == 0 {
				break
			}
			for _, e := range page {
				actual = append(actual, e.ID)
			}
			last := page[len(page)-1]
			q.BeforeTime, q.BeforeID = last.OccurredAt, last.ID
		}
		all := append(append([]r.ActivityEvent{}, old...), hot...)
		sort.Slice(all, func(i, j int) bool {
			if all[i].OccurredAt.Equal(all[j].OccurredAt) {
				return all[i].ID > all[j].ID
			}
			return all[i].OccurredAt.After(all[j].OccurredAt)
		})
		var expected []string
		for _, e := range all {
			expected = append(expected, e.ID)
		}
		if !reflect.DeepEqual(actual, expected) {
			t.Fatalf("history lost or duplicated rows: %v / %v", actual, expected)
		}
		for _, query := range []r.ActivityQuery{{AccountID: "account", IncludeEpicSources: true}, {AccountID: "account", CharacterID: "19", FromDay: r.ActivityDay(at), ThroughDay: r.ActivityDay(at.Add(time.Hour)), IncludeEpicSources: true}} {
			summary, err := group.Activity.Summary(ctx, query)
			if err != nil {
				t.Fatal(err)
			}
			var wanted int64
			for _, e := range all {
				if e.Metric == r.ActivityEpic && (query.FromDay == "" || (r.ActivityDay(e.OccurredAt) >= query.FromDay && r.ActivityDay(e.OccurredAt) <= query.ThroughDay)) {
					wanted++
				}
			}
			var sourced int64
			for _, value := range summary.EpicSources {
				sourced += value.Amount
			}
			if summary.Metrics[r.ActivityEpic] != wanted || sourced != wanted {
				t.Fatalf("counter/source mismatch %v %d want%d", summary.Metrics, sourced, wanted)
			}
		}
		activityArchiveExec(t, db, "DELETE FROM "+s.Hot+" WHERE event_id=?", hot[0].ID)
	})
	t.Run("immutable-history-and-safe-movement", func(t *testing.T) {
		queries := []string{"UPDATE " + s.Hot + " SET amount=amount+1", "UPDATE " + s.Cold + " SET character_name='changed'", "DELETE FROM " + s.Cold,
			"DELETE FROM " + s.Receipts, "UPDATE " + s.Receipts + " SET payload=REPEAT('x',32)", "DELETE FROM " + s.Hot}
		for _, query := range queries {
			if _, err := db.Exec(query); err == nil {
				t.Fatalf("destructive history change accepted: %s", query)
			}
		}
		if _, err := db.Exec("INSERT INTO " + s.Cold + " SELECT account_id,CONCAT(event_id,':bad'),character_id,character_name,metric,amount,occurred_at,service_day,dungeon_id,item_id,rarity,source FROM " + s.Hot + " LIMIT 1"); err == nil {
			t.Fatal("unreceipted cold history accepted")
		}
	})
	t.Run("ownership-and-concurrent-replay", func(t *testing.T) {
		wrong := event("hot:foreign", at, false)
		wrong.AccountID = "foreign"
		if _, err := activityArchiveRecord(ctx, group, wrong, nil); !errors.Is(err, r.ErrActivityTransaction) {
			t.Fatalf("foreign ownership: %v", err)
		}
		e := event("hot:concurrent", at, false)
		var winners atomic.Int32
		failures := make(chan error, 2)
		for i := 0; i < 2; i++ {
			go func() {
				added, err := activityArchiveRecord(ctx, group, e, nil)
				if added {
					winners.Add(1)
				}
				failures <- err
			}()
		}
		for i := 0; i < 2; i++ {
			if err := <-failures; err != nil {
				t.Fatal(err)
			}
		}
		if winners.Load() != 1 {
			t.Fatalf("concurrent awards: %d", winners.Load())
		}
	})
	t.Run("component-detects-active-layout", func(t *testing.T) {
		component := NewComponent(SQLDBAdapter{DB: db}, ComponentOptions{DatabasePlan: options.DatabasePlan, TablePrefix: options.TablePrefix})
		if err := component.Start(ctx); err != nil {
			t.Fatal(err)
		}
		defer component.Stop(ctx)
		if !component.Snapshot().ActivityArchive {
			t.Fatal("component lost archive mode")
		}
	})
	t.Run("committed-but-response-lost-is-recovered-without-second-award", func(t *testing.T) {
		_, cfg, _ := activityArchiveDB(t)
		connector, err := (&mysqlDriver.MySQLDriver{}).OpenConnector(cfg.FormatDSN())
		if err != nil {
			t.Fatal(err)
		}
		armed := &atomic.Bool{}
		unknown := sql.OpenDB(&activityArchiveLostConnector{base: connector, armed: armed})
		defer unknown.Close()
		uncertain, err := NewQualifiedMySQLGroupFromDB(ctx, unknown, options)
		if err != nil {
			t.Fatal(err)
		}
		e := event("hot:lost-commit-response", at, false)
		award := func(tx r.Group) error {
			character, _, err := tx.Character.Load(ctx, "19")
			if err != nil {
				return err
			}
			character.Stats["archive_commit_once"]++
			return r.SaveCharacterFields(ctx, tx.Character, character, r.CharacterFieldStats)
		}
		armed.Store(true)
		if _, err := activityArchiveRecord(ctx, uncertain, e, award); !errors.Is(err, errActivityArchiveLostCommit) {
			t.Fatalf("commit response was not lost: %v", err)
		}
		fresh, err := NewQualifiedMySQLGroupFromDB(ctx, db, options)
		if err != nil {
			t.Fatal(err)
		}
		if added, err := activityArchiveRecord(ctx, fresh, e, award); err != nil || added {
			t.Fatalf("committed event repeated: %v %v", added, err)
		}
		character, _, err := fresh.Character.Load(ctx, "19")
		if err != nil || character.Stats["archive_commit_once"] != 1 {
			t.Fatal("committed award lost or repeated", err)
		}
	})
}

func TestActivityArchiveQualificationAllShardsAndPrivileges(t *testing.T) {
	db, cfg, ctx := activityArchiveDB(t)
	prefix := "gate_1"
	tag := time.Now().UnixNano()
	names := []string{fmt.Sprintf("archive_gate_%d_a", tag), fmt.Sprintf("archive_gate_%d_b", tag), fmt.Sprintf("archive_gate_%d_read", tag)}
	for _, name := range names {
		activityArchiveSchema(t, db, ctx, name, prefix)
	}
	options := MySQLGroupOptions{TablePrefix: prefix, DatabasePlan: r.DatabasePlan{WriteDatabases: names[:2], ReadDatabases: names[2:]}}
	specs := []ActivityArchiveLayout{activityArchiveInstallFixture(t, db, names[0], prefix)}
	if _, err := NewQualifiedMySQLGroupFromDB(ctx, db, options); !errors.Is(err, ErrActivityArchiveSchema) {
		t.Fatalf("mixed shards accepted: %v", err)
	}
	for _, name := range names[1:] {
		specs = append(specs, activityArchiveInstallFixture(t, db, name, prefix))
	}
	if _, err := NewQualifiedMySQLGroupFromDB(ctx, db, options); err != nil {
		t.Fatal(err)
	}
	s := specs[2]
	t.Run("missing-trigger-and-partial-phase", func(t *testing.T) {
		trigger := s.Triggers[0]
		activityArchiveExec(t, db, "DROP TRIGGER "+quoteSQLIdentifier(s.Database)+"."+quoteSQLIdentifier(trigger.Name))
		if _, err := NewQualifiedMySQLGroupFromDB(ctx, db, options); !errors.Is(err, ErrActivityArchiveSchema) {
			t.Fatal("missing trigger accepted", err)
		}
		activityArchiveExec(t, db, s.TriggerDDL(trigger))
		activityArchiveExec(t, db, "UPDATE "+s.State+" SET phase='preparing'")
		component := NewComponent(SQLDBAdapter{DB: db}, ComponentOptions{DatabasePlan: options.DatabasePlan, TablePrefix: prefix, AutoCreateSchema: true, CreateDatabases: true})
		if err := component.Start(ctx); !errors.Is(err, ErrActivityArchiveSchema) {
			t.Fatal("partial layout reached startup DDL", err)
		}
		activityArchiveExec(t, db, "UPDATE "+s.State+" SET phase='active'")
	})
	t.Run("unexpected-schema-or-trigger", func(t *testing.T) {
		activityArchiveExec(t, db, "ALTER TABLE "+s.Receipts+" MODIFY payload VARBINARY(32) NOT NULL")
		if _, err := NewQualifiedMySQLGroupFromDB(ctx, db, options); !errors.Is(err, ErrActivityArchiveSchema) {
			t.Fatal("wrong receipt type accepted", err)
		}
		activityArchiveExec(t, db, "ALTER TABLE "+s.Receipts+" MODIFY payload BINARY(32) NOT NULL")
		foreign := quoteSQLIdentifier(s.Database) + ".`unrecognized_activity_insert`"
		activityArchiveExec(t, db, "CREATE TRIGGER "+foreign+" BEFORE INSERT ON "+s.Hot+" FOR EACH ROW SET @archive_foreign_probe=1")
		if _, err := NewQualifiedMySQLGroupFromDB(ctx, db, options); !errors.Is(err, ErrActivityArchiveSchema) {
			t.Fatal("foreign trigger accepted", err)
		}
		activityArchiveExec(t, db, "DROP TRIGGER "+foreign)
	})
	t.Run("schema-scoped-app-and-insufficient-metadata-permission", func(t *testing.T) {
		businessOptions := options
		businessOptions.DatabasePlan.ReadDatabases = append([]string(nil), options.DatabasePlan.WriteDatabases...)
		owner, err := NewQualifiedMySQLGroupFromDB(ctx, db, businessOptions)
		if err != nil {
			t.Fatal(err)
		}
		activityArchiveActor(t, ctx, owner, "app-account", "app-character")
		for _, withTriggers := range []bool{true, false} {
			user := fmt.Sprintf("arc_app_%d_%t", tag, withTriggers)
			activityArchiveExec(t, db, "CREATE USER '"+user+"'@'%' IDENTIFIED BY 'disposable-archive-fixture'")
			defer db.Exec("DROP USER '" + user + "'@'%'")
			for _, name := range names {
				privileges := "SELECT,INSERT,UPDATE,DELETE"
				if withTriggers {
					privileges += ",TRIGGER"
				}
				activityArchiveExec(t, db, "GRANT "+privileges+" ON "+quoteSQLIdentifier(name)+".* TO '"+user+"'@'%'")
			}
			copy := cfg.Clone()
			copy.User, copy.Passwd = user, "disposable-archive-fixture"
			app, err := sql.Open("mysql", copy.FormatDSN())
			if err != nil {
				t.Fatal(err)
			}
			_, err = NewQualifiedMySQLGroupFromDB(ctx, app, options)
			if withTriggers && err != nil {
				t.Fatalf("schema-scoped app needs global rights: %v", err)
			}
			if !withTriggers && !errors.Is(err, ErrActivityArchiveSchema) {
				t.Fatalf("invisible triggers silently accepted: %v", err)
			}
			if withTriggers {
				group, err := NewQualifiedMySQLGroupFromDB(ctx, app, businessOptions)
				if err != nil {
					t.Fatal(err)
				}
				e := r.ActivityEvent{ID: "app:trigger-definer", AccountID: "app-account", CharacterID: "app-character", Metric: r.ActivityFatigue, Amount: 1, OccurredAt: time.Now(), DungeonID: 310, Source: "dungeon_room"}
				if added, err := activityArchiveRecord(ctx, group, e, nil); err != nil || !added {
					t.Fatalf("schema-scoped trigger write failed: %v %v", added, err)
				}
			}
			app.Close()
		}
	})
}

func TestActivityArchiveFingerprintSQLCompatibility(t *testing.T) {
	db, _, ctx := activityArchiveDB(t)
	query := "SELECT " + activityFingerprintSQL("") + " FROM (SELECT ? AS account_id,? AS event_id,? AS character_id,? AS metric,? AS amount,? AS dungeon_id,? AS item_id,? AS rarity,? AS source) fingerprint_fields"
	// Historical payloads can include values not accepted as new events today.
	// The permanent encoding must still match without trimming or normalization.
	for _, e := range []r.ActivityEvent{
		{AccountID: "账号\x00A ", ID: "case:quote'", CharacterID: "角:色", Metric: r.ActivityEpic, Amount: math.MaxInt64, DungeonID: math.MaxInt64, ItemID: math.MaxUint32, Rarity: 4, Source: "dungeon_pickup"},
		{AccountID: "a", ID: "b:c", CharacterID: "d", Metric: "历史指标", Amount: -1, DungeonID: -1, Rarity: -32768, Source: "旧来源\\\x00' "},
		{AccountID: "a:b", ID: "c", CharacterID: "d", Metric: "", Amount: 0, DungeonID: 0, Rarity: 32767, Source: ""},
	} {
		var actual []byte
		if err := db.QueryRowContext(ctx, query, e.AccountID, e.ID, e.CharacterID, e.Metric, e.Amount, e.DungeonID, e.ItemID, e.Rarity, e.Source).Scan(&actual); err != nil {
			t.Fatal(err)
		}
		want := r.ActivityPayloadFingerprintV1(e)
		if string(actual) != string(want[:]) {
			t.Fatal("SQL and Go differ on historical integer or UTF-8 boundary")
		}
	}
}

// This fixture exercises the required legacy database contract, not a complete
// publisher rollback. Production maintenance must also bind identities, retain
// backups and journal each automatically committed DDL before executing it.
func TestActivityArchiveFixtureUnificationRestoresLegacyContract(t *testing.T) {
	db, _, ctx := activityArchiveDB(t)
	name := fmt.Sprintf("archive_unify_%d", time.Now().UnixNano())
	prefix := "rollback_1"
	activityArchiveSchema(t, db, ctx, name, prefix)
	options := MySQLGroupOptions{TablePrefix: prefix, DatabasePlan: r.DatabasePlan{WriteDatabases: []string{name}}}
	legacy, err := NewMySQLGroupFromDB(db, options)
	if err != nil {
		t.Fatal(err)
	}
	activityArchiveActor(t, ctx, legacy, "rollback-account", "rollback-character")
	at := time.Date(2026, 11, 16, 1, 0, 0, 0, time.UTC)
	event := func(id string) r.ActivityEvent {
		return r.ActivityEvent{ID: id, AccountID: "rollback-account", CharacterID: "rollback-character", Metric: r.ActivityFatigue, Amount: 1, OccurredAt: at, DungeonID: 310, Source: "dungeon_room"}
	}
	old, recent := event("before-cutover"), event("after-cutover")
	if added, err := activityArchiveRecord(ctx, legacy, old, nil); err != nil || !added {
		t.Fatalf("seed: %t %v", added, err)
	}
	s := activityArchiveInstallFixture(t, db, name, prefix)
	active, err := NewQualifiedMySQLGroupFromDB(ctx, db, options)
	if err != nil {
		t.Fatal(err)
	}
	if added, err := activityArchiveRecord(ctx, active, recent, nil); err != nil || !added {
		t.Fatalf("hot write: %t %v", added, err)
	}
	// Old code cannot serve a moved event until maintenance unifies history.
	if added, err := activityArchiveRecord(ctx, legacy, old, nil); err == nil || added {
		t.Fatal("unified rollback prerequisite was bypassed", err)
	}
	activityArchiveExec(t, db, "UPDATE "+s.State+" SET phase='preparing'")
	for _, trigger := range s.Triggers {
		activityArchiveExec(t, db, "DROP TRIGGER "+quoteSQLIdentifier(s.Database)+"."+quoteSQLIdentifier(trigger.Name))
	}
	if _, err := NewQualifiedMySQLGroupFromDB(ctx, db, options); !errors.Is(err, ErrActivityArchiveSchema) {
		t.Fatal("partial rollback accepted", err)
	}
	// The complete original table remains Cold; only the new hot delta is copied.
	activityArchiveExec(t, db, "INSERT INTO "+s.Cold+" SELECT h.* FROM "+s.Hot+" h WHERE NOT EXISTS (SELECT 1 FROM "+s.Cold+" c WHERE c.account_id=h.account_id AND c.event_id=h.event_id)")
	retainedHot := mysqlTable(name, prefix, "activity_retired_hot")
	activityArchiveExec(t, db, "RENAME TABLE "+s.Hot+" TO "+retainedHot+", "+s.Cold+" TO "+s.Hot)
	activityArchiveExec(t, db, "UPDATE "+s.State+" SET phase='inactive'")
	qualified, err := NewQualifiedMySQLGroupFromDB(ctx, db, options)
	if err != nil {
		t.Fatal("inactive retained receipts blocked legacy layout", err)
	}
	if qualified.Activity.(*mysqlActivityStore).router.activityArchive {
		t.Fatal("inactive receipts were reused")
	}
	for _, group := range []r.Group{legacy, qualified} {
		for _, e := range []r.ActivityEvent{old, recent} {
			e.OccurredAt = at.Add(24 * time.Hour)
			e.CharacterName = "renamed"
			if added, err := activityArchiveRecord(ctx, group, e, nil); err != nil || added {
				t.Fatalf("legacy replay: %t %v", added, err)
			}
		}
		rows, err := group.Activity.Events(ctx, r.ActivityQuery{AccountID: old.AccountID})
		if err != nil || len(rows) != 2 {
			t.Fatalf("legacy history: %d %v", len(rows), err)
		}
		summary, err := group.Activity.Summary(ctx, r.ActivityQuery{AccountID: old.AccountID})
		if err != nil || summary.Metrics[r.ActivityFatigue] != 2 {
			t.Fatal("unification changed counters", err)
		}
	}
	if added, err := activityArchiveRecord(ctx, legacy, event("after-rollback"), nil); err != nil || !added {
		t.Fatalf("legacy new write: %t %v", added, err)
	}
	var retained, receiptCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM " + retainedHot).Scan(&retained); err != nil || retained != 1 {
		t.Fatal("old hot evidence lost", err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM " + s.Receipts).Scan(&receiptCount); err != nil || receiptCount != 2 {
		t.Fatal("permanent receipt evidence lost", err)
	}
	// A stale receipt set must not become active merely by changing the marker.
	activityArchiveExec(t, db, "UPDATE "+s.State+" SET phase='active'")
	if _, err := NewQualifiedMySQLGroupFromDB(ctx, db, options); !errors.Is(err, ErrActivityArchiveSchema) {
		t.Fatal("stale receipts reactivated without complete layout", err)
	}
}

var errActivityArchiveLostCommit = errors.New("successful database commit response unavailable")

type activityArchiveLostConnector struct {
	base  driver.Connector
	armed *atomic.Bool
}

func (c *activityArchiveLostConnector) Driver() driver.Driver { return c.base.Driver() }
func (c *activityArchiveLostConnector) Connect(ctx context.Context) (driver.Conn, error) {
	base, err := c.base.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &activityArchiveLostConn{Conn: base, armed: c.armed}, nil
}

type activityArchiveLostConn struct {
	driver.Conn
	armed *atomic.Bool
}

func (c *activityArchiveLostConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	tx, err := c.Conn.(driver.ConnBeginTx).BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &activityArchiveLostTx{Tx: tx, armed: c.armed}, nil
}
func (c *activityArchiveLostConn) ExecContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	return c.Conn.(driver.ExecerContext).ExecContext(ctx, q, args)
}
func (c *activityArchiveLostConn) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	return c.Conn.(driver.QueryerContext).QueryContext(ctx, q, args)
}
func (c *activityArchiveLostConn) PrepareContext(ctx context.Context, q string) (driver.Stmt, error) {
	return c.Conn.(driver.ConnPrepareContext).PrepareContext(ctx, q)
}
func (c *activityArchiveLostConn) Ping(ctx context.Context) error {
	return c.Conn.(driver.Pinger).Ping(ctx)
}

type activityArchiveLostTx struct {
	driver.Tx
	armed *atomic.Bool
}

func (t *activityArchiveLostTx) Commit() error {
	err := t.Tx.Commit()
	if err == nil && t.armed.CompareAndSwap(true, false) {
		return errActivityArchiveLostCommit
	}
	return err
}
