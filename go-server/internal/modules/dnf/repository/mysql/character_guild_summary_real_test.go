package mysql

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"reflect"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	driver "github.com/go-sql-driver/mysql"
	"longheng.io/server/internal/modules/dnf/repository"
)

const (
	guildSummaryRealDSNEnv          = "DNFBRIDGE_PLAYER_TEST_DSN"
	guildSummaryRealTablePrefix     = "summary"
	guildSummaryRealMeasurementMark = "GUILD_SUMMARY_MEASUREMENT"
)

var _ repository.CharacterGuildSummaryReader = (*mysqlCharStore)(nil)

type guildSummaryRealSQLCounts struct {
	QueryContexts    int
	QueryRowContexts int
	Execs            int
	FullLoads        int
	Queries          []string
}

// guildSummaryRealSQLDB records every SQLDB entry point used by production
// repositories. Full-load calls are identified from the wide character SELECT,
// independently of which Go method issued them.
type guildSummaryRealSQLDB struct {
	SQLDBAdapter

	mu               sync.Mutex
	queryContexts    int
	queryRowContexts int
	execs            int
	fullLoads        int
	captureQueries   bool
	queries          []string
}

func (db *guildSummaryRealSQLDB) QueryContext(ctx context.Context, query string, args ...any) (SQLRows, error) {
	db.recordQuery(query)
	db.mu.Lock()
	db.queryContexts++
	db.mu.Unlock()
	return db.SQLDBAdapter.QueryContext(ctx, query, args...)
}

func (db *guildSummaryRealSQLDB) QueryRowContext(ctx context.Context, query string, args ...any) SQLRow {
	db.recordQuery(query)
	db.mu.Lock()
	db.queryRowContexts++
	db.mu.Unlock()
	return db.SQLDBAdapter.QueryRowContext(ctx, query, args...)
}

func (db *guildSummaryRealSQLDB) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	db.mu.Lock()
	db.execs++
	db.mu.Unlock()
	return db.SQLDBAdapter.ExecContext(ctx, query, args...)
}

func (db *guildSummaryRealSQLDB) recordQuery(query string) {
	db.mu.Lock()
	defer db.mu.Unlock()
	if guildSummaryRealIsFullLoadQuery(query) {
		db.fullLoads++
	}
	if db.captureQueries {
		db.queries = append(db.queries, query)
	}
}

func (db *guildSummaryRealSQLDB) reset(captureQueries bool) {
	db.mu.Lock()
	defer db.mu.Unlock()
	db.queryContexts = 0
	db.queryRowContexts = 0
	db.execs = 0
	db.fullLoads = 0
	db.captureQueries = captureQueries
	db.queries = nil
}

func (db *guildSummaryRealSQLDB) snapshot() guildSummaryRealSQLCounts {
	db.mu.Lock()
	defer db.mu.Unlock()
	return guildSummaryRealSQLCounts{
		QueryContexts:    db.queryContexts,
		QueryRowContexts: db.queryRowContexts,
		Execs:            db.execs,
		FullLoads:        db.fullLoads,
		Queries:          append([]string(nil), db.queries...),
	}
}

func guildSummaryRealIsFullLoadQuery(query string) bool {
	return strings.Contains(query, "SELECT character_id, account_id, slot, name, job, level, ") &&
		strings.Contains(query, "created_at, updated_at FROM ") &&
		strings.Contains(query, " WHERE character_id = ?")
}

func guildSummaryRealAssertReadOnly(t *testing.T, counts guildSummaryRealSQLCounts) {
	t.Helper()
	if counts.Execs != 0 {
		t.Fatalf("read path executed %d SQL writes", counts.Execs)
	}
}

// Embedding CharacterRepository intentionally hides the concrete MySQL type's
// LoadCharacterGuildSummaries method so callers exercise the production legacy
// one-full-Load-per-ID fallback.
type guildSummaryRealLegacyCharacters struct {
	repository.CharacterRepository
}

func guildSummaryRealAssertSixFieldContract(t *testing.T) {
	t.Helper()
	typ := reflect.TypeOf(repository.CharacterGuildSummary{})
	want := []struct {
		name string
		typ  reflect.Type
	}{
		{name: "CharacterID", typ: reflect.TypeOf("")},
		{name: "AccountID", typ: reflect.TypeOf("")},
		{name: "Name", typ: reflect.TypeOf("")},
		{name: "Job", typ: reflect.TypeOf("")},
		{name: "Level", typ: reflect.TypeOf(int(0))},
		{name: "GrowType", typ: reflect.TypeOf(int64(0))},
	}
	if typ.NumField() != len(want) {
		t.Fatalf("CharacterGuildSummary has %d fields, want %d", typ.NumField(), len(want))
	}
	for index, expected := range want {
		field := typ.Field(index)
		if field.Name != expected.name || field.Type != expected.typ {
			t.Fatalf("CharacterGuildSummary field %d = %s %s, want %s %s",
				index, field.Name, field.Type, expected.name, expected.typ)
		}
	}
}

func guildSummaryRealIDsForDatabase(router mysqlRouter, database, namespace string, count int) ([]string, error) {
	wantTable := mysqlTable(database, guildSummaryRealTablePrefix, mysqlCharTable)
	ids := make([]string, 0, count)
	for suffix := 0; len(ids) < count; suffix++ {
		id := fmt.Sprintf("guild-summary-%s-%04d", namespace, suffix)
		table, err := router.readTable(mysqlCharTable, id)
		if err != nil {
			return nil, err
		}
		if table == wantTable {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

func guildSummaryRealRecord(namespace string, index int, rich bool, now time.Time) repository.CharacterRecord {
	growType := int64(index%4 + 1)
	if rich {
		growType = 0x21
	}
	stats := map[string]int64{
		repository.CharacterGrowTypeStatKey: growType,
		"exp":                               int64(1000 + index),
		"gold":                              int64(2000 + index),
	}
	if rich {
		for extra := 0; extra < 20; extra++ {
			stats[fmt.Sprintf("guild_summary_extra_%02d", extra)] = int64(10_000 + extra)
		}
	}

	entry := repository.CharacterRosterEntry{
		PackedJobGrow: int64(0x20 + index%4),
		ByteC:         int64(index%17 + 1),
		ObjectID:      int64(50_000 + index),
	}
	if rich {
		entry.EquipSummary = []repository.CharacterRosterEquipSummary{
			{
				Slot:               1,
				ItemIDOrIcon:       10101,
				RawEntry:           []byte{0x10, 0x11, 0x12, 0x13},
				PackedFlags:        0x01020304,
				OptionalIDOrExpire: 20261102,
				AuxValue:           71,
				AuxFlag:            1,
			},
			{
				Slot:               2,
				ItemIDOrIcon:       20202,
				RawEntry:           []byte{0x20, 0x21, 0x22, 0x23, 0x24},
				PackedFlags:        0x05060708,
				OptionalIDOrExpire: 20261103,
				AuxValue:           72,
				AuxFlag:            2,
			},
		}
		entry.LinkedIDBlock = []int64{9001, 9002}
		entry.Flags = []int64{7, 9}
	}

	return repository.CharacterRecord{
		CharacterID: fmt.Sprintf("guild-summary-%s-%04d", namespace, index),
		AccountID:   fmt.Sprintf("guild-summary-account-%s-%04d", namespace, index),
		Slot:        index % 32,
		Name:        fmt.Sprintf("Summary%s%03d", namespace, index),
		Job:         fmt.Sprintf("job-%d", index%8),
		Level:       70 + index%20,
		Stats:       stats,
		Location: repository.CharacterLocation{
			ChannelID: index%8 + 1,
			TownID:    38,
			DungeonID: int64(10_000 + index),
			RoomID:    fmt.Sprintf("room-%04d", index),
		},
		Roster: repository.CharacterRoster{
			Header: repository.CharacterRosterHeader{
				TotalOrSlotLimit: 32,
				UsedOrRemain:     2,
				SelectedOrPage:   int64(index%3 + 1),
				RosterState:      2,
			},
			Entry: entry,
		},
		CreatedAt: now.Add(time.Duration(index) * time.Microsecond),
		UpdatedAt: now.Add(time.Duration(index)*time.Microsecond + time.Minute),
	}
}

func guildSummaryRealSeed(
	t *testing.T,
	ctx context.Context,
	characters repository.CharacterRepository,
	ids []string,
	namespace string,
	richFirst bool,
) map[string]repository.CharacterRecord {
	t.Helper()
	records := make(map[string]repository.CharacterRecord, len(ids))
	now := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	for index, id := range ids {
		record := guildSummaryRealRecord(namespace, index, richFirst && index == 0, now)
		record.CharacterID = id
		if err := characters.Save(ctx, record); err != nil {
			t.Fatalf("seed character %q: %v", id, err)
		}
		records[id] = record
	}
	return records
}

func guildSummaryRealExpected(ids []string, records map[string]repository.CharacterRecord) map[string]repository.CharacterGuildSummary {
	want := make(map[string]repository.CharacterGuildSummary, len(ids))
	for _, id := range ids {
		record, ok := records[id]
		if !ok {
			continue
		}
		want[id] = repository.CharacterGuildSummary{
			CharacterID: record.CharacterID,
			AccountID:   record.AccountID,
			Name:        record.Name,
			Job:         record.Job,
			Level:       record.Level,
			GrowType:    record.Stats[repository.CharacterGrowTypeStatKey],
		}
	}
	return want
}

func guildSummaryRealFirstDifference(
	got, want map[string]repository.CharacterGuildSummary,
) string {
	if len(got) != len(want) {
		return fmt.Sprintf("summary count got=%d want=%d", len(got), len(want))
	}
	ids := make([]string, 0, len(want))
	for id := range want {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if !reflect.DeepEqual(got[id], want[id]) {
			return fmt.Sprintf("summary[%q] got=%+v want=%+v", id, got[id], want[id])
		}
	}
	return ""
}

func guildSummaryRealBatchCounts(characters *mysqlCharStore, ids []string) (map[string]int, error) {
	counts := make(map[string]int)
	for _, id := range ids {
		table, err := characters.router.readTable(mysqlCharTable, id)
		if err != nil {
			return nil, err
		}
		counts[table]++
	}
	for table, count := range counts {
		counts[table] = (count + characterGuildSummaryBatchSize - 1) / characterGuildSummaryBatchSize
	}
	return counts, nil
}

func guildSummaryRealQueryCounts(queries []string, tables map[string]int) map[string]int {
	counts := make(map[string]int, len(tables))
	for _, query := range queries {
		for table := range tables {
			if strings.Contains(query, " FROM "+table+" WHERE character_id IN (") {
				counts[table]++
			}
		}
	}
	return counts
}

type guildSummaryRealMeasurementRun struct {
	Kind                 string `json:"kind"`
	Group                string `json:"group"`
	SequencePosition     int    `json:"sequence_position"`
	Iteration            int    `json:"iteration"`
	FixedIDCount         int    `json:"fixed_id_count"`
	ElapsedNS            int64  `json:"elapsed_ns"`
	TotalAllocDeltaBytes uint64 `json:"total_alloc_delta_bytes"`
	QueryContexts        int    `json:"query_contexts"`
	QueryRowContexts     int    `json:"query_row_contexts"`
	Execs                int    `json:"execs"`
	FullLoads            int    `json:"full_loads"`
	SummaryQueries       int64  `json:"summary_queries"`
	SummaryFullLoads     int64  `json:"summary_full_loads"`
}

type guildSummaryRealMeasurementTotals struct {
	Runs                 int   `json:"runs"`
	ElapsedNS            int64 `json:"elapsed_ns"`
	TotalAllocDeltaBytes int64 `json:"total_alloc_delta_bytes"`
	QueryContexts        int   `json:"query_contexts"`
	QueryRowContexts     int   `json:"query_row_contexts"`
	Execs                int   `json:"execs"`
	FullLoads            int   `json:"full_loads"`
	SummaryQueries       int64 `json:"summary_queries"`
	SummaryFullLoads     int64 `json:"summary_full_loads"`
}

func guildSummaryRealMeasureOnce(
	ctx context.Context,
	characters repository.CharacterRepository,
	legacy repository.CharacterRepository,
	counter *guildSummaryRealSQLDB,
	ids []string,
	useLegacy bool,
) (guildSummaryRealMeasurementRun, error) {
	readCtx, readCounts := repository.WithCharacterGuildSummaryReadCounts(ctx)
	counter.reset(false)
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	started := time.Now()
	var summaries map[string]repository.CharacterGuildSummary
	var err error
	if useLegacy {
		summaries, err = repository.LoadCharacterGuildSummaries(readCtx, legacy, ids)
	} else {
		summaries, err = repository.LoadCharacterGuildSummaries(readCtx, characters, ids)
	}
	elapsed := time.Since(started)
	runtime.ReadMemStats(&after)
	sqlCounts := counter.snapshot()
	if err != nil {
		return guildSummaryRealMeasurementRun{}, err
	}
	if len(summaries) != len(ids) {
		return guildSummaryRealMeasurementRun{}, fmt.Errorf("measurement summaries=%d want=%d", len(summaries), len(ids))
	}
	if sqlCounts.Execs != 0 {
		return guildSummaryRealMeasurementRun{}, fmt.Errorf("measurement performed %d writes", sqlCounts.Execs)
	}
	return guildSummaryRealMeasurementRun{
		ElapsedNS:            elapsed.Nanoseconds(),
		TotalAllocDeltaBytes: after.TotalAlloc - before.TotalAlloc,
		QueryContexts:        sqlCounts.QueryContexts,
		QueryRowContexts:     sqlCounts.QueryRowContexts,
		Execs:                sqlCounts.Execs,
		FullLoads:            sqlCounts.FullLoads,
		SummaryQueries:       readCounts.Queries,
		SummaryFullLoads:     readCounts.FullLoads,
	}, nil
}

func guildSummaryRealAddMeasurement(total *guildSummaryRealMeasurementTotals, run guildSummaryRealMeasurementRun) {
	total.Runs++
	total.ElapsedNS += run.ElapsedNS
	total.TotalAllocDeltaBytes += int64(run.TotalAllocDeltaBytes)
	total.QueryContexts += run.QueryContexts
	total.QueryRowContexts += run.QueryRowContexts
	total.Execs += run.Execs
	total.FullLoads += run.FullLoads
	total.SummaryQueries += run.SummaryQueries
	total.SummaryFullLoads += run.SummaryFullLoads
}

func guildSummaryRealLogMeasurement(t *testing.T, value any) {
	t.Helper()
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal guild summary measurement: %v", err)
	}
	t.Logf("%s %s", guildSummaryRealMeasurementMark, payload)
}

// TestCharacterGuildSummaryRealMySQL exercises the narrow guild summary path
// against an independently owned MariaDB/MySQL server. It is skipped unless the
// controller supplies DNFBRIDGE_PLAYER_TEST_DSN. This test does not start a
// server, use application credentials, or inspect any database other than the
// two uniquely named databases created below.
func TestCharacterGuildSummaryRealMySQL(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv(guildSummaryRealDSNEnv))
	if dsn == "" {
		t.Skip(guildSummaryRealDSNEnv + " is not configured")
	}
	cfg, err := driver.ParseDSN(dsn)
	if err != nil {
		// Never include the DSN or parser error: parser failures can quote input.
		t.Fatal("invalid DNFBRIDGE_PLAYER_TEST_DSN")
	}
	if cfg.Net == "tcp" {
		host, _, splitErr := net.SplitHostPort(cfg.Addr)
		if splitErr != nil || (host != "localhost" && !net.ParseIP(host).IsLoopback()) {
			t.Fatal("DNFBRIDGE_PLAYER_TEST_DSN must target a loopback MySQL server")
		}
	}
	cfg.DBName = ""
	cfg.ParseTime = true
	controlDB, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal("open disposable MySQL server")
	}
	defer controlDB.Close()

	pingCtx, pingCancel := context.WithTimeout(context.Background(), 15*time.Second)
	err = controlDB.PingContext(pingCtx)
	pingCancel()
	if err != nil {
		t.Fatalf("ping disposable MySQL server: %v", err)
	}

	stamp := time.Now().UTC().UnixNano()
	databaseA := fmt.Sprintf("guild_summary_test_%d_a", stamp)
	databaseB := fmt.Sprintf("guild_summary_test_%d_b", stamp)
	ownedDatabases := []string{databaseA, databaseB}
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		for _, database := range ownedDatabases {
			if _, dropErr := controlDB.ExecContext(cleanupCtx, "DROP DATABASE IF EXISTS "+quoteSQLIdentifier(database)); dropErr != nil {
				t.Errorf("drop disposable database %s: %v", database, dropErr)
			}
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	plan := repository.DatabasePlan{
		ReadDatabases:  append([]string(nil), ownedDatabases...),
		WriteDatabases: append([]string(nil), ownedDatabases...),
	}
	if err := EnsureMySQLSchema(ctx, controlDB, SchemaOptions{
		AutoCreate:      true,
		CreateDatabases: true,
		TablePrefix:     guildSummaryRealTablePrefix,
		DatabasePlan:    plan,
	}); err != nil {
		t.Fatalf("ensure disposable schema: %v", err)
	}

	counter := &guildSummaryRealSQLDB{SQLDBAdapter: SQLDBAdapter{DB: controlDB}}
	group, err := NewMySQLGroup(counter, MySQLGroupOptions{
		DatabasePlan: plan,
		TablePrefix:  guildSummaryRealTablePrefix,
	})
	if err != nil {
		t.Fatal(err)
	}
	characters, ok := group.Character.(*mysqlCharStore)
	if !ok {
		t.Fatal("MySQL character repository is not the real mysqlCharStore")
	}

	namespace := strconv.FormatInt(stamp, 10)
	primaryIDs, err := guildSummaryRealIDsForDatabase(characters.router, databaseA, namespace+"-a", 257)
	if err != nil {
		t.Fatal(err)
	}
	secondaryIDs, err := guildSummaryRealIDsForDatabase(characters.router, databaseB, namespace+"-b", 10)
	if err != nil {
		t.Fatal(err)
	}
	primaryRecords := guildSummaryRealSeed(t, ctx, group.Character, primaryIDs, namespace+"-a", true)
	secondaryRecords := guildSummaryRealSeed(t, ctx, group.Character, secondaryIDs, namespace+"-b", false)
	allRecords := make(map[string]repository.CharacterRecord, len(primaryRecords)+len(secondaryRecords))
	for id, record := range primaryRecords {
		allRecords[id] = record
	}
	for id, record := range secondaryRecords {
		allRecords[id] = record
	}
	counter.reset(false)
	t.Logf("disposable_schema=%s/%s table_prefix=%s primary_route=%s secondary_route=%s primary=%d secondary=%d schema_and_seed_read_counts_excluded=true",
		databaseA, databaseB, guildSummaryRealTablePrefix, databaseA, databaseB, len(primaryIDs), len(secondaryIDs))

	t.Run("six_field_summary_contract", func(t *testing.T) {
		guildSummaryRealAssertSixFieldContract(t)
	})

	richID := primaryIDs[0]
	richRecord := primaryRecords[richID]
	t.Run("saved_full_record_has_six_selects", func(t *testing.T) {
		t.Setenv(mysqlCharacterMirrorStatsAuthoritativeEnv, "")
		counter.reset(false)
		loaded, found, err := group.Character.Load(ctx, richID)
		if err != nil || !found {
			t.Fatalf("full Load found=%t err=%v", found, err)
		}
		counts := counter.snapshot()
		if counts.QueryContexts != 3 || counts.QueryRowContexts != 3 || counts.FullLoads != 1 {
			t.Fatalf("full Load SQL = query_contexts:%d query_row_contexts:%d total:%d full_loads:%d; nonempty Roster should produce 3+3=6 SELECTs",
				counts.QueryContexts, counts.QueryRowContexts, counts.QueryContexts+counts.QueryRowContexts, counts.FullLoads)
		}
		guildSummaryRealAssertReadOnly(t, counts)
		if loaded.CharacterID != richRecord.CharacterID || loaded.AccountID != richRecord.AccountID ||
			loaded.Slot != richRecord.Slot || loaded.Name != richRecord.Name || loaded.Job != richRecord.Job || loaded.Level != richRecord.Level {
			t.Fatalf("identity/base fields changed: got=%+v want=%+v", loaded, richRecord)
		}
		if loaded.Stats[repository.CharacterGrowTypeStatKey] != 0x21 {
			t.Fatalf("main grow_type=%d, want 0x21", loaded.Stats[repository.CharacterGrowTypeStatKey])
		}
		for extra := 0; extra < 20; extra++ {
			key := fmt.Sprintf("guild_summary_extra_%02d", extra)
			if loaded.Stats[key] != int64(10_000+extra) {
				t.Fatalf("extra stat %q=%d, want %d", key, loaded.Stats[key], 10_000+extra)
			}
		}
		if loaded.Location != richRecord.Location {
			t.Fatalf("Location=%+v, want %+v", loaded.Location, richRecord.Location)
		}
		if !reflect.DeepEqual(loaded.Roster, richRecord.Roster) {
			t.Fatalf("nonempty Roster changed: got=%+v want=%+v", loaded.Roster, richRecord.Roster)
		}
		t.Logf("full_load_sql=6 query_contexts=3 query_row_contexts=3 full_loads=1 execs=0 extra_stats=20 equipment=%d lists=%d",
			len(loaded.Roster.Entry.EquipSummary), len(loaded.Roster.Entry.LinkedIDBlock)+len(loaded.Roster.Entry.Flags))
	})

	t.Run("default_on_summary_matches_full_load_190", func(t *testing.T) {
		t.Setenv(mysqlCharacterMirrorStatsAuthoritativeEnv, "")
		ids := primaryIDs[:190]
		readCtx, readCounts := repository.WithCharacterGuildSummaryReadCounts(ctx)
		counter.reset(false)
		summaries, err := repository.LoadCharacterGuildSummaries(readCtx, group.Character, ids)
		if err != nil {
			t.Fatal(err)
		}
		counts := counter.snapshot()
		guildSummaryRealAssertReadOnly(t, counts)
		if len(summaries) != len(ids) {
			t.Fatalf("narrow summaries=%d, want %d", len(summaries), len(ids))
		}
		if readCounts.Queries != 1 || readCounts.FullLoads != 0 ||
			counts.QueryContexts != 1 || counts.QueryRowContexts != 0 || counts.FullLoads != 0 {
			t.Fatalf("narrow 190 counts = repo(queries:%d full_loads:%d) SQL(query_contexts:%d query_row_contexts:%d full_loads:%d), want 1/0 and 1/0/0",
				readCounts.Queries, readCounts.FullLoads, counts.QueryContexts, counts.QueryRowContexts, counts.FullLoads)
		}
		want := guildSummaryRealExpected(ids, primaryRecords)
		if difference := guildSummaryRealFirstDifference(summaries, want); difference != "" {
			t.Fatal(difference)
		}

		counter.reset(false)
		fullProjection := make(map[string]repository.CharacterGuildSummary, len(ids))
		for _, id := range ids {
			full, found, loadErr := group.Character.Load(ctx, id)
			if loadErr != nil || !found {
				t.Fatalf("full Load %q found=%t err=%v", id, found, loadErr)
			}
			fullProjection[id] = repository.CharacterGuildSummary{
				CharacterID: full.CharacterID,
				AccountID:   full.AccountID,
				Name:        full.Name,
				Job:         full.Job,
				Level:       full.Level,
				GrowType:    full.Stats[repository.CharacterGrowTypeStatKey],
			}
		}
		fullCounts := counter.snapshot()
		guildSummaryRealAssertReadOnly(t, fullCounts)
		if difference := guildSummaryRealFirstDifference(summaries, fullProjection); difference != "" {
			t.Fatalf("default narrow summary differs from full Load: %s", difference)
		}
		t.Logf("default_on_190 narrow_queries=1 full_loads=0 full_comparison_selects=%d field_match=true", fullCounts.QueryContexts+fullCounts.QueryRowContexts)
	})

	t.Run("grow_type_default_on_and_compatibility_off", func(t *testing.T) {
		statsTable, err := characters.router.readTable(mysqlCharacterStatsTable, richID)
		if err != nil {
			t.Fatal(err)
		}
		characterTable, err := characters.router.readTable(mysqlCharTable, richID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := controlDB.ExecContext(ctx,
			"INSERT INTO "+statsTable+" (character_id, stat_key, stat_value) VALUES (?, ?, ?) ON DUPLICATE KEY UPDATE stat_value = VALUES(stat_value)",
			richID, repository.CharacterGrowTypeStatKey, 0,
		); err != nil {
			t.Fatalf("insert grow_type shadow stat: %v", err)
		}
		var mainGrowType int64
		if err := controlDB.QueryRowContext(ctx,
			"SELECT grow_type FROM "+characterTable+" WHERE character_id = ?", richID,
		).Scan(&mainGrowType); err != nil {
			t.Fatalf("read main grow_type column: %v", err)
		}
		if mainGrowType != 0x21 {
			t.Fatalf("main grow_type column=%#x, want 0x21", mainGrowType)
		}

		t.Setenv(mysqlCharacterMirrorStatsAuthoritativeEnv, "")
		counter.reset(false)
		defaultSummaries, err := characters.LoadCharacterGuildSummaries(ctx, []string{richID})
		if err != nil {
			t.Fatal(err)
		}
		defaultCounts := counter.snapshot()
		guildSummaryRealAssertReadOnly(t, defaultCounts)
		if defaultSummaries[richID].GrowType != 0x21 {
			t.Fatalf("default-on summary grow_type=%#x, want authoritative main column 0x21", defaultSummaries[richID].GrowType)
		}
		if defaultCounts.QueryContexts != 1 || defaultCounts.QueryRowContexts != 0 || defaultCounts.FullLoads != 0 {
			t.Fatalf("default-on grow_type SQL query_contexts=%d query_row_contexts=%d full_loads=%d, want 1/0/0",
				defaultCounts.QueryContexts, defaultCounts.QueryRowContexts, defaultCounts.FullLoads)
		}

		t.Setenv(mysqlCharacterMirrorStatsAuthoritativeEnv, "off")
		counter.reset(false)
		offIDs := []string{richID, primaryIDs[1]}
		offSummaries, err := characters.LoadCharacterGuildSummaries(ctx, offIDs)
		if err != nil {
			t.Fatal(err)
		}
		offCounts := counter.snapshot()
		guildSummaryRealAssertReadOnly(t, offCounts)
		if offSummaries[richID].GrowType != 0 {
			t.Fatalf("off compatibility summary grow_type=%#x, want shadow row 0", offSummaries[richID].GrowType)
		}
		withoutShadow := primaryRecords[primaryIDs[1]]
		if got, want := offSummaries[primaryIDs[1]].GrowType, withoutShadow.Stats[repository.CharacterGrowTypeStatKey]; got != want {
			t.Fatalf("off compatibility without shadow grow_type=%d, want main-column fallback %d", got, want)
		}
		if offCounts.QueryContexts != 1 || offCounts.QueryRowContexts != 0 || offCounts.FullLoads != 0 {
			t.Fatalf("off grow_type SQL query_contexts=%d query_row_contexts=%d full_loads=%d, want 1/0/0",
				offCounts.QueryContexts, offCounts.QueryRowContexts, offCounts.FullLoads)
		}
		t.Logf("grow_type main_column=%#x shadow=0 default_on=%#x compatibility_off=%#x no_shadow_off=%d narrow_queries=1 full_loads=0",
			mainGrowType, defaultSummaries[richID].GrowType, offSummaries[richID].GrowType, offSummaries[primaryIDs[1]].GrowType)
	})

	t.Run("legacy_190_uses_one_full_load_per_id", func(t *testing.T) {
		t.Setenv(mysqlCharacterMirrorStatsAuthoritativeEnv, "")
		ids := primaryIDs[:190]
		readCtx, readCounts := repository.WithCharacterGuildSummaryReadCounts(ctx)
		counter.reset(false)
		summaries, err := repository.LoadCharacterGuildSummaries(
			readCtx,
			guildSummaryRealLegacyCharacters{CharacterRepository: group.Character},
			ids,
		)
		if err != nil {
			t.Fatal(err)
		}
		counts := counter.snapshot()
		guildSummaryRealAssertReadOnly(t, counts)
		totalSelects := counts.QueryContexts + counts.QueryRowContexts
		if readCounts.FullLoads != 190 || readCounts.Queries != 0 || counts.FullLoads != 190 || totalSelects != 190*6 {
			t.Fatalf("legacy 190 counts = repo(queries:%d full_loads:%d) SQL(query_contexts:%d query_row_contexts:%d total:%d full_loads:%d), want repo 0/190 and SQL 570/570/1140/190; every seeded Roster is nonempty, so each full Load should execute 6 SELECTs",
				readCounts.Queries, readCounts.FullLoads, counts.QueryContexts, counts.QueryRowContexts, totalSelects, counts.FullLoads)
		}
		want := guildSummaryRealExpected(ids, primaryRecords)
		if difference := guildSummaryRealFirstDifference(summaries, want); difference != "" {
			t.Fatal(difference)
		}
		t.Logf("legacy_190 actual query_contexts=%d query_row_contexts=%d total_selects=%d full_loads=%d execs=%d",
			counts.QueryContexts, counts.QueryRowContexts, totalSelects, counts.FullLoads, counts.Execs)
	})

	t.Run("empty_duplicate_missing", func(t *testing.T) {
		t.Setenv(mysqlCharacterMirrorStatsAuthoritativeEnv, "")
		counter.reset(false)
		empty, err := characters.LoadCharacterGuildSummaries(ctx, []string{"", " \t\r\n "})
		if err != nil {
			t.Fatal(err)
		}
		emptyCounts := counter.snapshot()
		guildSummaryRealAssertReadOnly(t, emptyCounts)
		if empty == nil || len(empty) != 0 || emptyCounts.QueryContexts != 0 || emptyCounts.QueryRowContexts != 0 {
			t.Fatalf("empty summaries=%v counts=%+v", empty, emptyCounts)
		}

		counter.reset(false)
		duplicate, err := characters.LoadCharacterGuildSummaries(ctx,
			[]string{richID, " " + richID + " ", ""},
		)
		if err != nil {
			t.Fatal(err)
		}
		duplicateCounts := counter.snapshot()
		guildSummaryRealAssertReadOnly(t, duplicateCounts)
		if len(duplicate) != 1 || duplicateCounts.QueryContexts != 1 || duplicateCounts.FullLoads != 0 {
			t.Fatalf("duplicate summaries=%d counts=%+v, want one query/one row", len(duplicate), duplicateCounts)
		}

		missingID := namespace + "-missing"
		expectedBatches, err := guildSummaryRealBatchCounts(characters, []string{richID, missingID})
		if err != nil {
			t.Fatal(err)
		}
		counter.reset(false)
		withMissing, err := characters.LoadCharacterGuildSummaries(ctx, []string{richID, missingID})
		if err != nil {
			t.Fatal(err)
		}
		missingCounts := counter.snapshot()
		guildSummaryRealAssertReadOnly(t, missingCounts)
		if len(withMissing) != 1 || missingCounts.QueryContexts != len(expectedBatches) || missingCounts.FullLoads != 0 {
			t.Fatalf("missing summaries=%d counts=%+v, want one query per route and no fabrication", len(withMissing), missingCounts)
		}
		if _, ok := withMissing[missingID]; ok {
			t.Fatalf("missing character %q was fabricated", missingID)
		}
	})

	t.Run("account_values_follow_rows", func(t *testing.T) {
		t.Setenv(mysqlCharacterMirrorStatsAuthoritativeEnv, "")
		ids := []string{primaryIDs[1], secondaryIDs[0]}
		counter.reset(false)
		got, err := characters.LoadCharacterGuildSummaries(ctx, ids)
		if err != nil {
			t.Fatal(err)
		}
		counts := counter.snapshot()
		guildSummaryRealAssertReadOnly(t, counts)
		if counts.QueryContexts != 2 || counts.QueryRowContexts != 0 || counts.FullLoads != 0 {
			t.Fatalf("two-route account read counts=%+v, want two route queries and no full Load", counts)
		}
		first := primaryRecords[ids[0]]
		second := secondaryRecords[ids[1]]
		if got[first.CharacterID].AccountID != first.AccountID || got[second.CharacterID].AccountID != second.AccountID ||
			first.AccountID == second.AccountID {
			t.Fatalf("account values crossed rows: first=%+v second=%+v", got[first.CharacterID], got[second.CharacterID])
		}
	})

	t.Run("256_and_257_batching", func(t *testing.T) {
		t.Setenv(mysqlCharacterMirrorStatsAuthoritativeEnv, "")
		for _, test := range []struct {
			count        int
			wantQueries  int
			wantParts    []int
			expectRoutes int
		}{
			{count: 256, wantQueries: 1, wantParts: []int{256}, expectRoutes: 1},
			{count: 257, wantQueries: 2, wantParts: []int{1, 256}, expectRoutes: 1},
		} {
			t.Run(strconv.Itoa(test.count), func(t *testing.T) {
				ids := primaryIDs[:test.count]
				counter.reset(true)
				summaries, err := characters.LoadCharacterGuildSummaries(ctx, ids)
				if err != nil {
					t.Fatal(err)
				}
				counts := counter.snapshot()
				guildSummaryRealAssertReadOnly(t, counts)
				if len(summaries) != test.count || counts.QueryContexts != test.wantQueries ||
					counts.QueryRowContexts != 0 || counts.FullLoads != 0 {
					t.Fatalf("count=%d summaries=%d query_contexts=%d query_row_contexts=%d full_loads=%d, want %d/%d/0/0",
						test.count, len(summaries), counts.QueryContexts, counts.QueryRowContexts, counts.FullLoads, test.count, test.wantQueries)
				}
				parts := make([]int, 0, len(counts.Queries))
				routes := make(map[string]struct{})
				for _, query := range counts.Queries {
					parts = append(parts, strings.Count(query, "?"))
					for _, database := range ownedDatabases {
						if strings.Contains(query, " FROM "+mysqlTable(database, guildSummaryRealTablePrefix, mysqlCharTable)+" WHERE ") {
							routes[database] = struct{}{}
						}
					}
				}
				sort.Ints(parts)
				if !reflect.DeepEqual(parts, test.wantParts) || len(routes) != test.expectRoutes {
					t.Fatalf("count=%d batch parts=%v routes=%v, want parts=%v routes=%d", test.count, parts, routes, test.wantParts, test.expectRoutes)
				}
			})
		}
	})

	t.Run("multi_route_batches_follow_actual_ids", func(t *testing.T) {
		t.Setenv(mysqlCharacterMirrorStatsAuthoritativeEnv, "")
		ids := append(append([]string(nil), primaryIDs...), secondaryIDs...)
		expectedBatches, err := guildSummaryRealBatchCounts(characters, ids)
		if err != nil {
			t.Fatal(err)
		}
		if len(expectedBatches) != 2 {
			t.Fatalf("generated IDs hit %d read routes, want both disposable databases", len(expectedBatches))
		}
		counter.reset(true)
		summaries, err := characters.LoadCharacterGuildSummaries(ctx, ids)
		if err != nil {
			t.Fatal(err)
		}
		counts := counter.snapshot()
		guildSummaryRealAssertReadOnly(t, counts)
		actualBatches := guildSummaryRealQueryCounts(counts.Queries, expectedBatches)
		if !reflect.DeepEqual(actualBatches, expectedBatches) {
			t.Fatalf("per-route batches=%v want=%v", actualBatches, expectedBatches)
		}
		totalBatches := 0
		for _, count := range expectedBatches {
			totalBatches += count
		}
		if counts.QueryContexts != totalBatches || counts.QueryRowContexts != 0 || counts.FullLoads != 0 || len(summaries) != len(ids) {
			t.Fatalf("multi-route summaries=%d query_contexts=%d want_batches=%d query_row_contexts=%d full_loads=%d",
				len(summaries), counts.QueryContexts, totalBatches, counts.QueryRowContexts, counts.FullLoads)
		}
		t.Logf("multi_route ids=%d expected_batches=%v actual_batches=%v full_loads=0", len(ids), expectedBatches, actualBatches)
	})

	t.Run("cancel_does_not_fallback", func(t *testing.T) {
		t.Setenv(mysqlCharacterMirrorStatsAuthoritativeEnv, "")
		canceledCtx, canceled := context.WithCancel(ctx)
		canceled()
		readCtx, readCounts := repository.WithCharacterGuildSummaryReadCounts(canceledCtx)
		counter.reset(false)
		summaries, err := repository.LoadCharacterGuildSummaries(readCtx, group.Character, []string{richID})
		counts := counter.snapshot()
		guildSummaryRealAssertReadOnly(t, counts)
		if !errors.Is(err, context.Canceled) || summaries != nil ||
			counts.QueryContexts != 0 || counts.QueryRowContexts != 0 || counts.FullLoads != 0 ||
			readCounts.Queries != 0 || readCounts.FullLoads != 0 {
			t.Fatalf("cancel summaries=%v err=%v SQL=%+v repo=%+v", summaries, err, counts, readCounts)
		}
	})

	t.Run("fixed_190_abba_measurement", func(t *testing.T) {
		t.Setenv(mysqlCharacterMirrorStatsAuthoritativeEnv, "")
		ids := primaryIDs[:190]
		legacy := guildSummaryRealLegacyCharacters{CharacterRepository: group.Character}
		groups := []struct {
			label  string
			legacy bool
		}{
			{label: "before_1", legacy: true},
			{label: "after_1", legacy: false},
			{label: "after_2", legacy: false},
			{label: "before_2", legacy: true},
		}
		const runsPerGroup = 5
		var beforeTotal, afterTotal guildSummaryRealMeasurementTotals
		for sequencePosition, measurementGroup := range groups {
			for iteration := 0; iteration < runsPerGroup; iteration++ {
				run, err := guildSummaryRealMeasureOnce(ctx, group.Character, legacy, counter, ids, measurementGroup.legacy)
				if err != nil {
					t.Fatalf("measurement %s/%d: %v", measurementGroup.label, iteration+1, err)
				}
				run.Kind = "run"
				run.Group = measurementGroup.label
				run.SequencePosition = sequencePosition + 1
				run.Iteration = iteration + 1
				run.FixedIDCount = len(ids)
				if measurementGroup.legacy {
					if run.FullLoads != len(ids) || run.QueryContexts != len(ids)*3 || run.QueryRowContexts != len(ids)*3 {
						t.Fatalf("before measurement SQL=%+v, want 190 full Loads and 570/570 SELECT methods", run)
					}
					guildSummaryRealAddMeasurement(&beforeTotal, run)
				} else {
					if run.FullLoads != 0 || run.QueryContexts != 1 || run.QueryRowContexts != 0 {
						t.Fatalf("after measurement SQL=%+v, want one narrow SELECT and no full Load", run)
					}
					guildSummaryRealAddMeasurement(&afterTotal, run)
				}
				guildSummaryRealLogMeasurement(t, run)
			}
		}
		delta := map[string]any{
			"elapsed_ns":              afterTotal.ElapsedNS - beforeTotal.ElapsedNS,
			"total_alloc_delta_bytes": afterTotal.TotalAllocDeltaBytes - beforeTotal.TotalAllocDeltaBytes,
			"query_contexts":          afterTotal.QueryContexts - beforeTotal.QueryContexts,
			"query_row_contexts":      afterTotal.QueryRowContexts - beforeTotal.QueryRowContexts,
			"full_loads":              afterTotal.FullLoads - beforeTotal.FullLoads,
			"summary_queries":         afterTotal.SummaryQueries - beforeTotal.SummaryQueries,
			"summary_full_loads":      afterTotal.SummaryFullLoads - beforeTotal.SummaryFullLoads,
		}
		guildSummaryRealLogMeasurement(t, map[string]any{
			"kind":               "aggregate",
			"fixed_id_count":     len(ids),
			"sequence":           []string{"before_1", "after_1", "after_2", "before_2"},
			"runs_per_group":     runsPerGroup,
			"before":             beforeTotal,
			"after":              afterTotal,
			"after_minus_before": delta,
			"go_version":         runtime.Version(),
			"goos":               runtime.GOOS,
			"goarch":             runtime.GOARCH,
			"gomaxprocs":         runtime.GOMAXPROCS(0),
		})
	})

	t.Run("missing_table_does_not_fallback", func(t *testing.T) {
		t.Setenv(mysqlCharacterMirrorStatsAuthoritativeEnv, "")
		characterTable, err := characters.router.readTable(mysqlCharTable, richID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := controlDB.ExecContext(ctx, "DROP TABLE IF EXISTS "+characterTable); err != nil {
			t.Fatalf("drop test character table: %v", err)
		}
		readCtx, readCounts := repository.WithCharacterGuildSummaryReadCounts(ctx)
		counter.reset(false)
		summaries, err := repository.LoadCharacterGuildSummaries(readCtx, group.Character, []string{richID})
		counts := counter.snapshot()
		guildSummaryRealAssertReadOnly(t, counts)
		var mysqlErr *driver.MySQLError
		if err == nil || summaries != nil || !errors.As(err, &mysqlErr) || mysqlErr.Number != 1146 {
			t.Fatalf("missing table summaries=%v err=%v, want MySQL 1146 without fallback", summaries, err)
		}
		if readCounts.Queries != 1 || readCounts.FullLoads != 0 ||
			counts.QueryContexts != 1 || counts.QueryRowContexts != 0 || counts.FullLoads != 0 {
			t.Fatalf("missing table SQL counts=%+v repo=%+v; error must propagate without a full-Load fallback", counts, readCounts)
		}
	})
}
