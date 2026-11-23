package mysql

import (
	"context"
	"testing"

	"longheng.io/server/internal/modules/dnf/repository"
)

func TestMySQLInventoryLoadFreshRecordIsolatedFromTrackedSnapshot(t *testing.T) {
	raw := []byte{1, 2}
	database := &fakeSQLDB{
		rows: []fakeSQLRow{{values: []any{"char-1", nil}}},
		rowsets: []fakeSQLRows{
			{rows: []fakeSQLRow{{values: []any{"slot-0", int64(100), int64(2), false, nil, raw}}}},
			{rows: []fakeSQLRow{{values: []any{"slot-0", "marker", "before"}}}},
			{rows: []fakeSQLRow{{values: []any{"warehouse-0", int64(200), int64(1), false, nil, []byte{3}}}}},
			{rows: []fakeSQLRow{}},
		},
	}
	store := &mysqlBagStore{
		mysqlStoreBase:   mysqlStoreBase{router: mysqlRouter{db: database, readDBs: []string{"dnf"}, writeDBs: []string{"dnf"}, tablePrefix: "dnf"}},
		trackSlotChanges: true,
	}
	record, found, err := store.Load(context.Background(), "char-1")
	if err != nil || !found {
		t.Fatalf("Load() found=%t err=%v", found, err)
	}
	if store.loadedSnapshot == nil {
		t.Fatal("tracked snapshot missing")
	}
	item := record.Slots["slot-0"]
	item.Extra["marker"] = "after-return"
	item.RawEntry[0] = 0xff
	record.Slots["slot-0"] = item
	record.Slots["new-slot"] = repository.ItemStack{ItemID: 999}
	if got := store.loadedSnapshot.Slots["slot-0"]; got.Extra["marker"] != "before" || got.RawEntry[0] != 1 {
		t.Fatalf("tracked slot aliased caller: %+v", got)
	}
	if _, ok := store.loadedSnapshot.Slots["new-slot"]; ok {
		t.Fatal("tracked slot map aliased caller")
	}
	if raw[0] != 1 {
		t.Fatal("returned raw entry aliased the SQL row buffer")
	}
}

func TestMySQLEquipmentLoadFreshRecordIsolatedFromTrackedSnapshot(t *testing.T) {
	raw := []byte{7, 8}
	database := &fakeSQLDB{
		rows: []fakeSQLRow{{values: []any{"char-1", nil}}},
		rowsets: []fakeSQLRows{
			{rows: []fakeSQLRow{{values: []any{"weapon", int64(1), int64(1000), false, nil, raw}}}},
			{rows: []fakeSQLRow{{values: []any{"weapon", "marker", "before"}}}},
		},
	}
	store := &mysqlEquipmentStore{
		mysqlStoreBase:    mysqlStoreBase{router: mysqlRouter{db: database, readDBs: []string{"dnf"}, writeDBs: []string{"dnf"}, tablePrefix: "dnf"}},
		trackEntryChanges: true,
	}
	record, found, err := store.Load(context.Background(), "char-1")
	if err != nil || !found {
		t.Fatalf("Load() found=%t err=%v", found, err)
	}
	if store.loadedSnapshot == nil {
		t.Fatal("tracked snapshot missing")
	}
	entry := record.Entries["weapon"]
	entry.Extra["marker"] = "after-return"
	entry.RawEntry[0] = 0xff
	record.Entries["weapon"] = entry
	record.Entries["new-entry"] = repository.EquipmentEntry{ItemID: 999}
	if got := store.loadedSnapshot.Entries["weapon"]; got.Extra["marker"] != "before" || got.RawEntry[0] != 7 {
		t.Fatalf("tracked entry aliased caller: %+v", got)
	}
	if _, ok := store.loadedSnapshot.Entries["new-entry"]; ok {
		t.Fatal("tracked entry map aliased caller")
	}
	if raw[0] != 7 {
		t.Fatal("returned raw entry aliased the SQL row buffer")
	}
}

func TestMySQLAccountLoadFreshRecordIsolatedFromTrackedSnapshot(t *testing.T) {
	database := &fakeSQLDB{
		rows:    []fakeSQLRow{{values: []any{"acc-1", "active", int64(0), nil, nil, nil}}},
		rowsets: []fakeSQLRows{{rows: []fakeSQLRow{{values: []any{"key", "before"}}}}},
	}
	store := &mysqlAccountStore{
		mysqlStoreBase:       mysqlStoreBase{router: mysqlRouter{db: database, readDBs: []string{"dnf"}, writeDBs: []string{"dnf"}, tablePrefix: "dnf"}},
		trackMetadataChanges: true,
	}
	record, found, err := store.Load(context.Background(), "acc-1")
	if err != nil || !found {
		t.Fatalf("Load() found=%t err=%v", found, err)
	}
	if store.loadedSnapshot == nil {
		t.Fatal("tracked snapshot missing")
	}
	record.Metadata["key"] = "after-return"
	record.Metadata["new-key"] = "new"
	if store.loadedSnapshot.Metadata["key"] != "before" {
		t.Fatal("tracked metadata aliased caller")
	}
	if _, ok := store.loadedSnapshot.Metadata["new-key"]; ok {
		t.Fatal("tracked metadata map aliased caller")
	}
}

func TestMySQLSettingsLockedCacheReturnsIsolatedRecords(t *testing.T) {
	database := &fakeSQLDB{
		rows:    []fakeSQLRow{{values: []any{"scope-1", nil}}},
		rowsets: []fakeSQLRows{{rows: []fakeSQLRow{{values: []any{"key", "before"}}}}},
	}
	store := &mysqlSettingsStore{
		mysqlStoreBase:   mysqlStoreBase{router: mysqlRouter{db: database, readDBs: []string{"dnf"}, writeDBs: []string{"dnf"}, tablePrefix: "dnf", lockReads: true}},
		cacheLockedReads: true,
	}
	first, found, err := store.Load(context.Background(), "scope-1")
	if err != nil || !found {
		t.Fatalf("first Load() found=%t err=%v", found, err)
	}
	first.Values["key"] = "after-first"
	first.Values["new-key"] = "new"
	second, found, err := store.Load(context.Background(), "scope-1")
	if err != nil || !found {
		t.Fatalf("second Load() found=%t err=%v", found, err)
	}
	if second.Values["key"] != "before" {
		t.Fatal("locked cache leaked first caller mutation")
	}
	if _, ok := second.Values["new-key"]; ok {
		t.Fatal("locked cache leaked first caller map insertion")
	}
	second.Values["key"] = "after-second"
	third, found, err := store.Load(context.Background(), "scope-1")
	if err != nil || !found || third.Values["key"] != "before" {
		t.Fatalf("third Load()=%+v found=%t err=%v", third, found, err)
	}
	if len(database.execs) != 2 {
		t.Fatalf("settings SQL calls=%d, want first load only", len(database.execs))
	}
}

func TestMySQLCharacterLoadFreshRecordIsolatedFromTrackedSnapshot(t *testing.T) {
	database := &fakeSQLDB{}
	database.rows, database.rowsets = ownershipCharacterResult("char-1")
	store := &mysqlCharStore{
		mysqlStoreBase: mysqlStoreBase{router: mysqlRouter{
			db:          database,
			readDBs:     []string{"dnf"},
			writeDBs:    []string{"dnf"},
			tablePrefix: "dnf",
			lockReads:   true,
		}},
		trackStatChanges: true,
	}
	record, found, err := store.Load(context.Background(), "char-1")
	if err != nil || !found {
		t.Fatalf("Load() found=%t err=%v", found, err)
	}
	if store.loadedSnapshot == nil {
		t.Fatal("tracked snapshot missing")
	}
	record.Stats["honor_expert_level"] = 99
	record.Roster.Entry.Flags[0] = 99
	record.Roster.Entry.EquipSummary[0].RawEntry[0] = 0xff
	if store.loadedSnapshot.Stats["honor_expert_level"] != 7 {
		t.Fatal("tracked stats aliased caller")
	}
	if store.loadedSnapshot.Roster.Entry.Flags[0] != 42 {
		t.Fatal("tracked roster flags aliased caller")
	}
	if store.loadedSnapshot.Roster.Entry.EquipSummary[0].RawEntry[0] != 9 {
		t.Fatal("tracked roster raw entry aliased caller")
	}
}

func TestMySQLCharacterLockedReadCacheReturnsIsolatedRecords(t *testing.T) {
	t.Setenv(mysqlExperimentalPeakEnv, "on")
	database := &fakeSQLDB{}
	database.rows, database.rowsets = ownershipCharacterResult("char-cache")
	versioned := &experimentalPeakVersionedFakeSQLDB{fakeSQLDB: database}
	store := &mysqlCharStore{
		mysqlStoreBase: mysqlStoreBase{router: mysqlRouter{
			db:          versioned,
			readDBs:     []string{"dnf"},
			writeDBs:    []string{"dnf"},
			tablePrefix: "dnf",
			lockReads:   true,
		}},
		trackStatChanges: true,
	}
	first, found, err := store.Load(context.Background(), "char-cache")
	if err != nil || !found || store.loadedSnapshot == nil {
		t.Fatalf("first Load() found=%t err=%v snapshot=%v", found, err, store.loadedSnapshot != nil)
	}
	first.Stats["honor_expert_level"] = 99
	first.Roster.Entry.Flags[0] = 99
	first.Roster.Entry.EquipSummary[0].RawEntry[0] = 0xff
	second, found, err := store.Load(context.Background(), "char-cache")
	if err != nil || !found {
		t.Fatalf("second Load() found=%t err=%v", found, err)
	}
	if second.Stats["honor_expert_level"] != 7 || second.Roster.Entry.Flags[0] != 42 || second.Roster.Entry.EquipSummary[0].RawEntry[0] != 9 {
		t.Fatal("locked read cache leaked caller mutation")
	}
	second.Stats["honor_expert_level"] = 77
	third, found, err := store.Load(context.Background(), "char-cache")
	if err != nil || !found || third.Stats["honor_expert_level"] != 7 {
		t.Fatalf("third Load() found=%t err=%v stats=%v", found, err, third.Stats)
	}
	if len(database.execs) != 6 {
		t.Fatalf("character SQL calls=%d, want only first load to execute SQL", len(database.execs))
	}
}

func TestMySQLStoreEnvironmentCapturedAtRouterConstruction(t *testing.T) {
	t.Setenv(mysqlCharacterMirrorStatsAuthoritativeEnv, "off")
	t.Setenv(mysqlExperimentalPeakEnv, "off")
	router, err := newMySQLRouter(&fakeSQLDB{}, MySQLGroupOptions{DatabasePlan: repository.DatabasePlan{WriteDatabases: []string{"dnf"}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(mysqlCharacterMirrorStatsAuthoritativeEnv, "on")
	t.Setenv(mysqlExperimentalPeakEnv, "on")
	env := router.environment()
	if env.mirrorStatsAuthoritative || env.experimentalPeak {
		t.Fatalf("router reread environment after construction: %+v", env)
	}
	// Directly constructed stores are a test seam and keep the historical
	// per-call environment semantics used by compatibility test cases.
	direct := mysqlRouter{}
	directEnv := direct.environment()
	if !directEnv.mirrorStatsAuthoritative || !directEnv.experimentalPeak {
		t.Fatalf("direct test router did not retain per-call env semantics: %+v", directEnv)
	}
}

func ownershipCharacterResult(characterID string) ([]fakeSQLRow, []fakeSQLRows) {
	values := []any{characterID, "account-" + characterID, 1, "name", "1", 90}
	for _, column := range mysqlCharacterStatColumns {
		values = append(values, column.fallback)
	}
	values = append(values, nil, nil)
	rosterArgs := characterRosterArgs(characterID, repository.CharacterRoster{})
	rows := []fakeSQLRow{
		{values: values},
		{values: []any{1, 2, 3, "room"}},
		{values: rosterArgs[1:]},
	}
	rowsets := []fakeSQLRows{
		{rows: []fakeSQLRow{{values: []any{"honor_expert_level", int64(7)}}}},
		{rows: []fakeSQLRow{{values: []any{0, 1001, []byte{9, 8}, 1, 0, 0, 0}}}},
		{rows: []fakeSQLRow{{values: []any{"flags", int64(42)}}}},
	}
	return rows, rowsets
}
