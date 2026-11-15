package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/repository"
)

type experimentalPeakVersionedFakeSQLDB struct {
	*fakeSQLDB
	writeVersion atomic.Uint64
}

func (db *experimentalPeakVersionedFakeSQLDB) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	db.writeVersion.Add(1)
	return db.fakeSQLDB.ExecContext(ctx, query, args...)
}

func (db *experimentalPeakVersionedFakeSQLDB) transactionWriteVersion() (uint64, bool) {
	return db.writeVersion.Load(), true
}

type experimentalPeakDisabledVersionFakeSQLDB struct {
	*fakeSQLDB
}

func (*experimentalPeakDisabledVersionFakeSQLDB) transactionWriteVersion() (uint64, bool) {
	return 0, false
}

type experimentalPeakFixedVersionFakeSQLDB struct {
	*fakeSQLDB
}

func (*experimentalPeakFixedVersionFakeSQLDB) transactionWriteVersion() (uint64, bool) {
	return 1, true
}

type experimentalPeakFailNthExecSQLDB struct {
	*fakeSQLDB
	failAt  int
	attempt int
	err     error
}

func (db *experimentalPeakFailNthExecSQLDB) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	db.attempt++
	result, err := db.fakeSQLDB.ExecContext(ctx, query, args...)
	if db.attempt == db.failAt {
		return nil, db.err
	}
	return result, err
}

func experimentalPeakRowValues(characterID, accountID string, level int, overrides map[string]int64) []any {
	values := []any{characterID, accountID, 0, "character-" + characterID, "1", level}
	for _, column := range mysqlCharacterStatColumns {
		value := column.fallback
		if override, ok := overrides[column.key]; ok {
			value = override
		}
		values = append(values, value)
	}
	return append(values, nil, nil)
}

func newExperimentalPeakGroupForDB(db SQLDB) (repository.Group, error) {
	return NewMySQLGroup(db, MySQLGroupOptions{
		DatabasePlan: repository.DatabasePlan{
			WriteDatabases: []string{"dnf_s1_w1"},
			ReadDatabases:  []string{"dnf_s1_r1"},
		},
		Now: func() time.Time {
			return time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)
		},
	})
}

func newExperimentalPeakStore(t *testing.T, db SQLDB) (*mysqlCharStore, repository.Group) {
	t.Helper()
	group, err := newExperimentalPeakGroupForDB(db)
	if err != nil {
		t.Fatalf("NewMySQLGroup() error = %v", err)
	}
	store, ok := group.Character.(*mysqlCharStore)
	if !ok {
		t.Fatalf("character store type = %T", group.Character)
	}
	store.router.lockReads = true
	return store, group
}

func queueExperimentalPeakCharacterRow(db *fakeSQLDB, characterID, accountID string, level int, overrides map[string]int64) {
	db.rows = []fakeSQLRow{{values: experimentalPeakRowValues(characterID, accountID, level, overrides)}}
}

func TestExperimentalPeakCharacterCacheRequiresVersionedLockedTransaction(t *testing.T) {
	t.Run("environment-off", func(t *testing.T) {
		t.Setenv("DNFBRIDGE_EXPERIMENTAL_PEAK", "off")
		db := &fakeSQLDB{}
		store, _ := newExperimentalPeakStore(t, db)
		queueExperimentalPeakCharacterRow(db, "character-off", "account-off", 11, nil)
		first, ok, err := store.Load(context.Background(), "character-off")
		if err != nil || !ok || first.Level != 11 {
			t.Fatalf("first Load() record=%+v ok=%v err=%v", first, ok, err)
		}
		callsAfterFirst := len(db.execs)
		queueExperimentalPeakCharacterRow(db, "character-off", "account-off", 22, nil)
		second, ok, err := store.Load(context.Background(), "character-off")
		if err != nil || !ok || second.Level != 22 {
			t.Fatalf("second Load() record=%+v ok=%v err=%v", second, ok, err)
		}
		if len(db.execs) <= callsAfterFirst {
			t.Fatalf("environment-off performed no second SQL read: before=%d after=%d", callsAfterFirst, len(db.execs))
		}
	})

	t.Setenv("DNFBRIDGE_EXPERIMENTAL_PEAK", "on")
	for _, test := range []struct {
		name string
		db   func(*fakeSQLDB) SQLDB
	}{
		{name: "no-transaction-write-version", db: func(db *fakeSQLDB) SQLDB { return db }},
		{name: "transaction-write-version-disabled", db: func(db *fakeSQLDB) SQLDB {
			return &experimentalPeakDisabledVersionFakeSQLDB{fakeSQLDB: db}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fakeDB := &fakeSQLDB{}
			store, _ := newExperimentalPeakStore(t, test.db(fakeDB))
			queueExperimentalPeakCharacterRow(fakeDB, "character-unversioned", "account-unversioned", 31, nil)
			first, ok, err := store.Load(context.Background(), "character-unversioned")
			if err != nil || !ok || first.Level != 31 {
				t.Fatalf("first Load() record=%+v ok=%v err=%v", first, ok, err)
			}
			callsAfterFirst := len(fakeDB.execs)
			queueExperimentalPeakCharacterRow(fakeDB, "character-unversioned", "account-unversioned", 32, nil)
			second, ok, err := store.Load(context.Background(), "character-unversioned")
			if err != nil || !ok || second.Level != 32 {
				t.Fatalf("second Load() record=%+v ok=%v err=%v", second, ok, err)
			}
			if len(fakeDB.execs) <= callsAfterFirst {
				t.Fatalf("unversioned fake was incorrectly cached: before=%d after=%d", callsAfterFirst, len(fakeDB.execs))
			}
		})
	}
}

func TestExperimentalPeakCharacterCacheHitsAndReturnsDeepCopies(t *testing.T) {
	t.Setenv("DNFBRIDGE_EXPERIMENTAL_PEAK", "on")
	statKey := mysqlCharacterStatColumns[0].key
	db := &experimentalPeakVersionedFakeSQLDB{fakeSQLDB: &fakeSQLDB{}}
	store, _ := newExperimentalPeakStore(t, db)
	rosterArgs := characterRosterArgs("character-copy", repository.CharacterRoster{
		Entry: repository.CharacterRosterEntry{Flags: []int64{0, 0}},
	})
	db.rows = []fakeSQLRow{
		{values: experimentalPeakRowValues("character-copy", "account-copy", 41, map[string]int64{statKey: 42})},
		{values: []any{0, int64(0), int64(0), ""}},
		{values: rosterArgs[1:]},
	}
	db.rowsets = []fakeSQLRows{
		{},
		{},
		{rows: []fakeSQLRow{
			{values: []any{"flags", int64(1)}},
			{values: []any{"flags", int64(2)}},
		}},
	}

	first, ok, err := store.Load(context.Background(), "character-copy")
	if err != nil || !ok {
		t.Fatalf("first Load() ok=%v err=%v", ok, err)
	}
	if first.Level != 41 || first.Stats[statKey] != 42 {
		t.Fatalf("first Load() record=%+v", first)
	}
	if len(first.Roster.Entry.Flags) != 2 || first.Roster.Entry.Flags[0] != 1 || first.Roster.Entry.Flags[1] != 2 {
		t.Fatalf("first Load() roster flags=%v", first.Roster.Entry.Flags)
	}
	callsAfterFirst := len(db.execs)
	db.rows = nil
	db.rowsets = nil
	first.Stats[statKey] = 9001
	first.Roster.Entry.Flags[0] = 9003

	second, ok, err := store.Load(context.Background(), "character-copy")
	if err != nil || !ok {
		t.Fatalf("cached Load() ok=%v err=%v", ok, err)
	}
	if second.Level != 41 || second.Stats[statKey] != 42 {
		t.Fatalf("cached record shared caller mutation: %+v", second)
	}
	if len(second.Roster.Entry.Flags) != 2 || second.Roster.Entry.Flags[0] != 1 || second.Roster.Entry.Flags[1] != 2 {
		t.Fatalf("cached roster shared caller mutation: %v", second.Roster.Entry.Flags)
	}
	if len(db.execs) != callsAfterFirst {
		t.Fatalf("cached Load() issued SQL: before=%d after=%d", callsAfterFirst, len(db.execs))
	}

	second.Stats[statKey] = 9002
	second.Roster.Entry.Flags[1] = 9004
	third, ok, err := store.Load(context.Background(), "character-copy")
	if err != nil || !ok || third.Stats[statKey] != 42 || third.Level != 41 {
		t.Fatalf("second cached clone record=%+v ok=%v err=%v", third, ok, err)
	}
	if len(third.Roster.Entry.Flags) != 2 || third.Roster.Entry.Flags[0] != 1 || third.Roster.Entry.Flags[1] != 2 {
		t.Fatalf("second cached roster shared caller mutation: %v", third.Roster.Entry.Flags)
	}
	if len(db.execs) != callsAfterFirst {
		t.Fatalf("second cached Load() issued SQL: before=%d after=%d", callsAfterFirst, len(db.execs))
	}
}

func TestExperimentalPeakCharacterCacheSeparatesOwnersAndCapsAtEight(t *testing.T) {
	t.Setenv("DNFBRIDGE_EXPERIMENTAL_PEAK", "on")
	db := &experimentalPeakVersionedFakeSQLDB{fakeSQLDB: &fakeSQLDB{}}
	store, _ := newExperimentalPeakStore(t, db)

	loadNew := func(id string, level int) repository.CharacterRecord {
		t.Helper()
		queueExperimentalPeakCharacterRow(db.fakeSQLDB, id, "account-"+id, level, nil)
		record, ok, err := store.Load(context.Background(), id)
		if err != nil || !ok {
			t.Fatalf("Load(%q) ok=%v err=%v", id, ok, err)
		}
		return record
	}

	first := loadNew("owner-0", 1)
	second := loadNew("owner-1", 2)
	if first.CharacterID != "owner-0" || second.CharacterID != "owner-1" || first.Level != 1 || second.Level != 2 {
		t.Fatalf("owner records first=%+v second=%+v", first, second)
	}
	callsBeforeOwnerSwitch := len(db.execs)
	db.rows = nil
	ownerZero, ok, err := store.Load(context.Background(), "owner-0")
	if err != nil || !ok || ownerZero.CharacterID != "owner-0" || ownerZero.Level != 1 {
		t.Fatalf("owner switch returned record=%+v ok=%v err=%v", ownerZero, ok, err)
	}
	if len(db.execs) != callsBeforeOwnerSwitch {
		t.Fatalf("owner switch issued SQL: before=%d after=%d", callsBeforeOwnerSwitch, len(db.execs))
	}

	for index := 2; index < 8; index++ {
		loadNew(fmt.Sprintf("owner-%d", index), index+1)
	}

	ninthFirst := loadNew("owner-8", 9)
	if ninthFirst.Level != 9 {
		t.Fatalf("ninth owner first Load() = %+v", ninthFirst)
	}
	callsAfterNinth := len(db.execs)
	queueExperimentalPeakCharacterRow(db.fakeSQLDB, "owner-8", "account-owner-8", 109, nil)
	ninthSecond, ok, err := store.Load(context.Background(), "owner-8")
	if err != nil || !ok || ninthSecond.Level != 109 {
		t.Fatalf("ninth owner was cached record=%+v ok=%v err=%v", ninthSecond, ok, err)
	}
	if len(db.execs) <= callsAfterNinth {
		t.Fatalf("ninth owner repeat did not query SQL: before=%d after=%d", callsAfterNinth, len(db.execs))
	}
}

func TestExperimentalPeakCharacterCacheDoesNotHideReadFailures(t *testing.T) {
	t.Setenv("DNFBRIDGE_EXPERIMENTAL_PEAK", "on")
	readErr := errors.New("read failed")
	db := &experimentalPeakVersionedFakeSQLDB{fakeSQLDB: &fakeSQLDB{}}
	store, _ := newExperimentalPeakStore(t, db)

	db.rows = []fakeSQLRow{{err: readErr}}
	if _, ok, err := store.Load(context.Background(), "character-error"); !errors.Is(err, readErr) || ok {
		t.Fatalf("Load() ok=%v err=%v, want %v", ok, err, readErr)
	}
	queueExperimentalPeakCharacterRow(db.fakeSQLDB, "character-error", "account-error", 51, nil)
	record, ok, err := store.Load(context.Background(), "character-error")
	if err != nil || !ok || record.Level != 51 {
		t.Fatalf("retry Load() record=%+v ok=%v err=%v", record, ok, err)
	}
	callsBeforeCancel := len(db.execs)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, ok, err := store.Load(ctx, "character-error"); !errors.Is(err, context.Canceled) || ok {
		t.Fatalf("canceled Load() ok=%v err=%v", ok, err)
	}
	if len(db.execs) != callsBeforeCancel {
		t.Fatalf("canceled Load() issued SQL: before=%d after=%d", callsBeforeCancel, len(db.execs))
	}
}

func TestExperimentalPeakCharacterCacheRejectsShadowAndIneligibleStats(t *testing.T) {
	t.Setenv("DNFBRIDGE_EXPERIMENTAL_PEAK", "on")
	tests := []struct {
		name string
		key  string
	}{
		{name: "shadow-mirror-stat", key: mysqlCharacterStatColumns[0].key},
		{name: "invalid-level-up-event", key: repository.LevelUpEventStatPrefix + "invalid"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db := &experimentalPeakFixedVersionFakeSQLDB{fakeSQLDB: &fakeSQLDB{}}
			store, _ := newExperimentalPeakStore(t, db)
			queueExperimentalPeakCharacterRow(db.fakeSQLDB, "character-ineligible", "account-ineligible", 61, nil)
			db.rowsets = []fakeSQLRows{{rows: []fakeSQLRow{{values: []any{test.key, int64(7)}}}}}
			if record, ok, err := store.Load(context.Background(), "character-ineligible"); err != nil || !ok || record.Level != 61 {
				t.Fatalf("first Load() record=%+v ok=%v err=%v", record, ok, err)
			}
			callsAfterFirst := len(db.execs)
			queueExperimentalPeakCharacterRow(db.fakeSQLDB, "character-ineligible", "account-ineligible", 62, nil)
			db.rowsets = []fakeSQLRows{{rows: []fakeSQLRow{{values: []any{test.key, int64(8)}}}}}
			record, ok, err := store.Load(context.Background(), "character-ineligible")
			if err != nil || !ok || record.Level != 62 {
				t.Fatalf("ineligible snapshot was cached record=%+v ok=%v err=%v", record, ok, err)
			}
			if len(db.execs) <= callsAfterFirst {
				t.Fatalf("ineligible snapshot did not re-read SQL: before=%d after=%d", callsAfterFirst, len(db.execs))
			}
		})
	}
}

func TestExperimentalPeakCharacterCacheInvalidatedByCrossRepositoryWrite(t *testing.T) {
	t.Setenv("DNFBRIDGE_EXPERIMENTAL_PEAK", "on")
	db := &experimentalPeakVersionedFakeSQLDB{fakeSQLDB: &fakeSQLDB{}}
	store, group := newExperimentalPeakStore(t, db)
	queueExperimentalPeakCharacterRow(db.fakeSQLDB, "character-shared-version", "account-shared-version", 71, nil)
	if record, ok, err := store.Load(context.Background(), "character-shared-version"); err != nil || !ok || record.Level != 71 {
		t.Fatalf("prime Load() record=%+v ok=%v err=%v", record, ok, err)
	}

	if err := group.Account.Save(context.Background(), repository.AccountRecord{AccountID: "account-cross-repo", State: "open"}); err != nil {
		t.Fatalf("cross-repository Save() error = %v", err)
	}
	if db.writeVersion.Load() == 0 {
		t.Fatal("cross-repository ExecContext did not advance the shared version")
	}

	queueExperimentalPeakCharacterRow(db.fakeSQLDB, "character-shared-version", "account-shared-version", 72, nil)
	fresh, ok, err := store.Load(context.Background(), "character-shared-version")
	if err != nil || !ok || fresh.Level != 72 {
		t.Fatalf("post-write Load() record=%+v ok=%v err=%v", fresh, ok, err)
	}
	callsAfterFresh := len(db.execs)
	db.rows = nil
	refreshed, ok, err := store.Load(context.Background(), "character-shared-version")
	if err != nil || !ok || refreshed.Level != 72 {
		t.Fatalf("post-write cached Load() record=%+v ok=%v err=%v", refreshed, ok, err)
	}
	if len(db.execs) != callsAfterFresh {
		t.Fatalf("post-write snapshot was not reusable: before=%d after=%d", callsAfterFresh, len(db.execs))
	}
}

func TestExperimentalPeakCharacterWritesInvalidateCachedLoad(t *testing.T) {
	t.Setenv("DNFBRIDGE_EXPERIMENTAL_PEAK", "on")
	tests := []struct {
		name   string
		action func(*testing.T, *mysqlCharStore, string)
	}{
		{
			name: "SaveFields",
			action: func(t *testing.T, store *mysqlCharStore, id string) {
				t.Helper()
				record := repository.CharacterRecord{CharacterID: id, AccountID: "account-save", Level: 82}
				if err := store.SaveFields(context.Background(), record, repository.CharacterFieldBase); err != nil {
					t.Fatalf("SaveFields() error = %v", err)
				}
			},
		},
		{
			name: "AdvanceStoryDigest",
			action: func(t *testing.T, store *mysqlCharStore, id string) {
				t.Helper()
				if err := store.AdvanceStoryDigest(context.Background(), id, 82, 1); err != nil {
					t.Fatalf("AdvanceStoryDigest() error = %v", err)
				}
			},
		},
		{
			name: "SaveProgressionCharacter",
			action: func(t *testing.T, store *mysqlCharStore, id string) {
				t.Helper()
				before := repository.CharacterRecord{CharacterID: id, AccountID: "account-save", Level: 81}
				after := before
				after.Level = 82
				if err := store.SaveProgressionCharacter(context.Background(), before, after); err != nil {
					t.Fatalf("SaveProgressionCharacter() error = %v", err)
				}
			},
		},
		{
			name: "SaveExpertJobExperience",
			action: func(t *testing.T, store *mysqlCharStore, id string) {
				t.Helper()
				record := repository.CharacterRecord{CharacterID: id, Stats: map[string]int64{"expert_job_exp": 82}}
				if err := store.SaveExpertJobExperience(context.Background(), record); err != nil {
					t.Fatalf("SaveExpertJobExperience() error = %v", err)
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db := &experimentalPeakFixedVersionFakeSQLDB{fakeSQLDB: &fakeSQLDB{}}
			store, _ := newExperimentalPeakStore(t, db)
			id := "character-save-" + strings.ReplaceAll(test.name, " ", "-")
			queueExperimentalPeakCharacterRow(db.fakeSQLDB, id, "account-save", 81, nil)
			if record, ok, err := store.Load(context.Background(), id); err != nil || !ok || record.Level != 81 {
				t.Fatalf("prime Load() record=%+v ok=%v err=%v", record, ok, err)
			}
			db.rows = nil
			if record, ok, err := store.Load(context.Background(), id); err != nil || !ok || record.Level != 81 {
				t.Fatalf("prime cache Load() record=%+v ok=%v err=%v", record, ok, err)
			}

			test.action(t, store, id)
			queueExperimentalPeakCharacterRow(db.fakeSQLDB, id, "account-save", 82, nil)
			fresh, ok, err := store.Load(context.Background(), id)
			if err != nil || !ok || fresh.Level != 82 {
				t.Fatalf("post-save Load() record=%+v ok=%v err=%v", fresh, ok, err)
			}
		})
	}
}

func TestExperimentalPeakCharacterCacheIsIsolatedAcrossTransactionStores(t *testing.T) {
	t.Setenv("DNFBRIDGE_EXPERIMENTAL_PEAK", "on")
	statKey := mysqlCharacterStatColumns[0].key
	const workers = 70
	errs := make(chan error, workers)
	var wait sync.WaitGroup
	for index := 0; index < workers; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			db := &experimentalPeakVersionedFakeSQLDB{fakeSQLDB: &fakeSQLDB{}}
			queueExperimentalPeakCharacterRow(db.fakeSQLDB, "shared-character-key", "shared-account-key", index+1, map[string]int64{statKey: 42})
			group, err := newExperimentalPeakGroupForDB(db)
			if err != nil {
				errs <- fmt.Errorf("worker %d NewMySQLGroup: %w", index, err)
				return
			}
			store, ok := group.Character.(*mysqlCharStore)
			if !ok {
				errs <- fmt.Errorf("worker %d character store type = %T", index, group.Character)
				return
			}
			store.router.lockReads = true
			first, ok, err := store.Load(context.Background(), "shared-character-key")
			if err != nil || !ok {
				errs <- fmt.Errorf("worker %d first Load ok=%v err=%v", index, ok, err)
				return
			}
			first.Stats[statKey] = int64(index + 1000)
			db.rows = nil
			second, ok, err := store.Load(context.Background(), "shared-character-key")
			if err != nil || !ok {
				errs <- fmt.Errorf("worker %d cached Load ok=%v err=%v", index, ok, err)
				return
			}
			if second.Level != index+1 || second.Stats[statKey] != 42 {
				errs <- fmt.Errorf("worker %d received foreign snapshot %+v", index, second)
			}
		}(index)
	}
	wait.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

func newExperimentalPeakProgressionRecords() (repository.CharacterRecord, repository.CharacterRecord) {
	beforeStats := make(map[string]int64, 508)
	afterStats := make(map[string]int64, 508)
	for index := 0; index < 501; index++ {
		key := fmt.Sprintf("delta_%04d", index)
		beforeStats[key] = int64(index)
		afterStats[key] = int64(index + 1000)
	}
	beforeStats["unchanged_stat"] = 7
	afterStats["unchanged_stat"] = 7
	beforeStats["removed_regular"] = 8
	beforeStats[repository.LevelUpEventStatPrefix+"89"] = 1
	beforeStats[repository.LevelUpRewardStatPrefix+"89"] = 1
	eventKey := repository.LevelUpEventKey(90)
	rewardKey := repository.LevelUpRewardStatPrefix + "90"
	beforeStats[eventKey] = 1
	afterStats[eventKey] = 2
	beforeStats[rewardKey] = 1
	afterStats[rewardKey] = 2
	return repository.CharacterRecord{
			CharacterID: "character-progression",
			AccountID:   "account-progression",
			Level:       89,
			Stats:       beforeStats,
		}, repository.CharacterRecord{
			CharacterID: "character-progression",
			AccountID:   "account-progression",
			Level:       90,
			Stats:       afterStats,
		}
}

func TestExperimentalPeakProgressionBatches501ChangedStatsAndPreservesAuditRules(t *testing.T) {
	t.Setenv("DNFBRIDGE_EXPERIMENTAL_PEAK", "on")
	t.Setenv(currentMysqlInsertBatchEnv, "200")
	db := &fakeSQLDB{}
	store, _ := newExperimentalPeakStore(t, db)
	before, after := newExperimentalPeakProgressionRecords()
	if err := store.SaveProgressionCharacter(context.Background(), before, after); err != nil {
		t.Fatalf("SaveProgressionCharacter() error = %v", err)
	}
	if len(db.execs) != 7 {
		t.Fatalf("batched statements = %d, want 7; calls=%v", len(db.execs), execQueries(db))
	}

	var insertedRows, deleteCalls int
	var eventRule, rewardRule, regularDelete bool
	for _, call := range db.execs {
		if strings.Contains(call.Query, "DELETE FROM") {
			deleteCalls++
			if len(call.Args) < 2 {
				t.Fatalf("DELETE args = %#v", call.Args)
			}
			key, _ := call.Args[1].(string)
			if key == "removed_regular" {
				regularDelete = true
			}
			if strings.HasPrefix(key, repository.LevelUpEventStatPrefix) || strings.HasPrefix(key, repository.LevelUpRewardStatPrefix) {
				t.Fatalf("permanent audit fact was deleted: %q", key)
			}
			continue
		}
		if !strings.Contains(call.Query, "INSERT INTO") || !strings.Contains(call.Query, mysqlCharacterStatsTable) {
			continue
		}
		insertedRows += len(call.Args) / 3
		for index := 1; index < len(call.Args); index += 3 {
			key, _ := call.Args[index].(string)
			if key == "unchanged_stat" || key == "removed_regular" ||
				key == repository.LevelUpEventStatPrefix+"89" || key == repository.LevelUpRewardStatPrefix+"89" {
				t.Fatalf("unchanged or absent key was written: %q", key)
			}
		}
		eventRule = eventRule || strings.Contains(call.Query, "stat_value < 0 OR VALUES(stat_value) >= 0")
		rewardRule = rewardRule || strings.Contains(call.Query, "IF(stat_value = 0, VALUES(stat_value), stat_value)")
	}
	if insertedRows != 503 {
		t.Fatalf("inserted stat rows = %d, want 503", insertedRows)
	}
	if deleteCalls != 1 || !regularDelete {
		t.Fatalf("delete calls=%d regularDelete=%v", deleteCalls, regularDelete)
	}
	if !eventRule || !rewardRule {
		t.Fatalf("permanent merge rules event=%v reward=%v", eventRule, rewardRule)
	}
}

func TestExperimentalPeakProgressionOffWritesChangedStatsRowByRow(t *testing.T) {
	t.Setenv("DNFBRIDGE_EXPERIMENTAL_PEAK", "off")
	t.Setenv(currentMysqlInsertBatchEnv, "200")
	db := &fakeSQLDB{}
	store, _ := newExperimentalPeakStore(t, db)
	before, after := newExperimentalPeakProgressionRecords()
	if err := store.SaveProgressionCharacter(context.Background(), before, after); err != nil {
		t.Fatalf("SaveProgressionCharacter() error = %v", err)
	}
	if len(db.execs) != 505 {
		t.Fatalf("row-by-row statements = %d, want 505", len(db.execs))
	}

	var statInserts, deleteCalls int
	for _, call := range db.execs {
		if strings.Contains(call.Query, "DELETE FROM") {
			deleteCalls++
			continue
		}
		if !strings.Contains(call.Query, "INSERT INTO") || !strings.Contains(call.Query, mysqlCharacterStatsTable) {
			continue
		}
		statInserts++
		if got := strings.Count(call.Query, "(?, ?, ?)"); got != 1 {
			t.Fatalf("row-by-row INSERT placeholders = %d in %s", got, call.Query)
		}
		if len(call.Args) != 3 {
			t.Fatalf("row-by-row INSERT args = %#v", call.Args)
		}
	}
	if statInserts != 503 || deleteCalls != 1 {
		t.Fatalf("stat inserts=%d deletes=%d", statInserts, deleteCalls)
	}
}

func TestExperimentalPeakProgressionBatchedWriteFailurePropagates(t *testing.T) {
	t.Setenv("DNFBRIDGE_EXPERIMENTAL_PEAK", "on")
	t.Setenv(currentMysqlInsertBatchEnv, "200")
	writeErr := errors.New("batched stat write failed")
	db := &experimentalPeakFailNthExecSQLDB{
		fakeSQLDB: &fakeSQLDB{},
		failAt:    3,
		err:       writeErr,
	}
	store, _ := newExperimentalPeakStore(t, db)
	before, after := newExperimentalPeakProgressionRecords()
	err := store.SaveProgressionCharacter(context.Background(), before, after)
	if !errors.Is(err, writeErr) {
		t.Fatalf("SaveProgressionCharacter() error = %v, want %v", err, writeErr)
	}
	if len(db.execs) != 3 {
		t.Fatalf("statements attempted = %d, want base + first batch + failed batch", len(db.execs))
	}
}
