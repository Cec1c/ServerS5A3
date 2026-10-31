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
	"longheng.io/server/internal/modules/dnf/repository"
)

type selectionReadsMySQLQueryCounter struct {
	SQLDBAdapter
	queries int
	rows    int
}

func (db *selectionReadsMySQLQueryCounter) QueryContext(ctx context.Context, query string, args ...any) (SQLRows, error) {
	db.queries++
	rows, err := db.SQLDBAdapter.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	return &selectionCountRows{SQLRows: rows, counter: db}, nil
}

func (db *selectionReadsMySQLQueryCounter) QueryRowContext(ctx context.Context, query string, args ...any) SQLRow {
	db.queries++
	return selectionCountRow{SQLRow: db.SQLDBAdapter.QueryRowContext(ctx, query, args...), counter: db}
}

type selectionCountRows struct {
	SQLRows
	counter *selectionReadsMySQLQueryCounter
}

func (r *selectionCountRows) Next() bool {
	ok := r.SQLRows.Next()
	if ok {
		r.counter.rows++
	}
	return ok
}

type selectionCountRow struct {
	SQLRow
	counter *selectionReadsMySQLQueryCounter
}

func (r selectionCountRow) Scan(dest ...any) error {
	err := r.SQLRow.Scan(dest...)
	if err == nil {
		r.counter.rows++
	}
	return err
}

func (db *selectionReadsMySQLQueryCounter) resetQueries() {
	db.queries = 0
	db.rows = 0
}

func TestSelectionReadsMySQLIntegration(t *testing.T) {
	if os.Getenv("DNF_RUNTIME_DISPOSABLE_DB") != "1" {
		t.Fatal("requires disposable database")
	}
	cfg, err := driver.ParseDSN(os.Getenv("DNFBRIDGE_PLAYER_TEST_DSN"))
	if err != nil || cfg.Net != "tcp" {
		t.Fatal("invalid isolated DSN")
	}
	host, _, err := net.SplitHostPort(cfg.Addr)
	if err != nil || host != "127.0.0.1" || cfg.DBName != "" {
		t.Fatal("requires loopback without existing schema")
	}
	cfg.ParseTime = true
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	schema := fmt.Sprintf("selection_reads_%d", time.Now().UnixNano())
	defer func() {
		if _, err := db.Exec("DROP DATABASE IF EXISTS `" + schema + "`"); err != nil {
			t.Error(err)
		}
	}()

	plan := repository.DatabasePlan{ReadDatabases: []string{schema}, WriteDatabases: []string{schema}}
	if err := EnsureMySQLSchema(ctx, db, SchemaOptions{AutoCreate: true, CreateDatabases: true, DatabasePlan: plan}); err != nil {
		t.Fatal(err)
	}
	queryCounter := &selectionReadsMySQLQueryCounter{SQLDBAdapter: SQLDBAdapter{DB: db}}
	g, err := NewMySQLGroup(queryCounter, MySQLGroupOptions{DatabasePlan: plan})
	if err != nil {
		t.Fatal(err)
	}
	identities, ok := g.Character.(repository.CharacterSelectionIdentityReader)
	if !ok {
		t.Fatal("MySQL character repository does not expose ListCharacterIdentities")
	}

	t.Run("identity counts and deleted filter", func(t *testing.T) {
		for _, count := range []int{1, 16, 17, 32} {
			count := count
			t.Run(fmt.Sprintf("%d-characters", count), func(t *testing.T) {
				account := fmt.Sprintf("dnf:selection:count:%d", count)
				want, deletedID := seedSelectionReadsCharacters(
					t, ctx, g.Character, db, schema, fmt.Sprintf("count-%d", count), account, count,
				)

				got, err := identities.ListCharacterIdentities(ctx, account, count)
				if err != nil {
					t.Fatal(err)
				}
				if len(got) != count {
					t.Fatalf("identity rows = %d, want %d", len(got), count)
				}
				for index := range want {
					if got[index].CharacterID != want[index].CharacterID ||
						got[index].AccountID != account ||
						got[index].Slot != want[index].Slot {
						t.Fatalf("identity row %d = %+v, want id=%q account=%q slot=%d",
							index, got[index], want[index].CharacterID, account, want[index].Slot)
					}
				}
				for _, record := range got {
					if record.CharacterID == deletedID {
						t.Fatalf("deleted character %q was returned", deletedID)
					}
				}
			})
		}
	})

	t.Run("empty parent records exist and missing records do not", func(t *testing.T) {
		presentCharacterID := "selection_presence_character"
		presentScope := "selection_presence_scope"
		if err := g.Inventory.Save(ctx, repository.InventoryRecord{CharacterID: presentCharacterID}); err != nil {
			t.Fatal(err)
		}
		if err := g.Equipment.Save(ctx, repository.EquipmentRecord{CharacterID: presentCharacterID}); err != nil {
			t.Fatal(err)
		}
		if err := g.Skill.Save(ctx, repository.SkillRecord{CharacterID: presentCharacterID}); err != nil {
			t.Fatal(err)
		}
		if err := g.Settings.Save(ctx, repository.SettingsRecord{Scope: presentScope}); err != nil {
			t.Fatal(err)
		}

		inventoryPresence, ok := g.Inventory.(repository.RecordPresenceReader)
		if !ok {
			t.Fatal("MySQL inventory repository does not expose Exists")
		}
		equipmentPresence, ok := g.Equipment.(repository.RecordPresenceReader)
		if !ok {
			t.Fatal("MySQL equipment repository does not expose Exists")
		}
		skillPresence, ok := g.Skill.(repository.RecordPresenceReader)
		if !ok {
			t.Fatal("MySQL skill repository does not expose Exists")
		}
		settingsPresence, ok := g.Settings.(repository.RecordPresenceReader)
		if !ok {
			t.Fatal("MySQL settings repository does not expose Exists")
		}

		checkPresence := func(name string, reader repository.RecordPresenceReader, key string, want bool) {
			t.Helper()
			got, err := reader.Exists(ctx, key)
			if err != nil {
				t.Fatalf("%s Exists(%q): %v", name, key, err)
			}
			if got != want {
				t.Fatalf("%s Exists(%q) = %t, want %t", name, key, got, want)
			}
		}
		checkPresence("inventory", inventoryPresence, presentCharacterID, true)
		checkPresence("equipment", equipmentPresence, presentCharacterID, true)
		checkPresence("skill", skillPresence, presentCharacterID, true)
		checkPresence("settings", settingsPresence, presentScope, true)

		missingCharacterID := "selection_presence_missing"
		missingScope := "selection_presence_scope_missing"
		checkPresence("inventory", inventoryPresence, missingCharacterID, false)
		checkPresence("equipment", equipmentPresence, missingCharacterID, false)
		checkPresence("skill", skillPresence, missingCharacterID, false)
		checkPresence("settings", settingsPresence, missingScope, false)
	})

	t.Run("quest status prefers states and falls back to progress", func(t *testing.T) {
		characterID := "selection_quests_character"
		if err := repository.CreateCharacter(ctx, g.Character, repository.CharacterRecord{
			CharacterID: characterID,
			AccountID:   "dnf:selection:quests",
			Slot:        0,
			Name:        "SelectionQuests",
		}); err != nil {
			t.Fatal(err)
		}
		if err := g.Quest.Save(ctx, repository.QuestRecord{
			CharacterID: characterID,
			States: map[int64]repository.QuestState{
				2670: {Status: ""},
				2672: {Status: "states-only"},
			},
			Progress: map[int64]repository.QuestState{
				2670: {Status: "progress-shadowed"},
				2671: {Status: "progress-only"},
			},
			UpdatedAt: time.Now().UTC(),
		}); err != nil {
			t.Fatal(err)
		}
		statuses, ok := g.Quest.(repository.QuestStatusReader)
		if !ok {
			t.Fatal("MySQL quest repository does not expose LoadQuestStatus")
		}

		cases := []struct {
			name       string
			character  string
			questID    int64
			wantStatus string
			wantFound  bool
		}{
			{name: "empty states status wins", character: characterID, questID: 2670, wantStatus: "", wantFound: true},
			{name: "progress fallback", character: characterID, questID: 2671, wantStatus: "progress-only", wantFound: true},
			{name: "nonempty states status", character: characterID, questID: 2672, wantStatus: "states-only", wantFound: true},
			{name: "missing quest", character: characterID, questID: 999999, wantFound: false},
			{name: "missing quest parent", character: "selection_quests_missing", questID: 2670, wantFound: false},
		}
		for _, test := range cases {
			t.Run(test.name, func(t *testing.T) {
				status, found, err := statuses.LoadQuestStatus(ctx, test.character, test.questID)
				if err != nil {
					t.Fatal(err)
				}
				if status != test.wantStatus || found != test.wantFound {
					t.Fatalf("LoadQuestStatus() = (%q, %t), want (%q, %t)",
						status, found, test.wantStatus, test.wantFound)
				}
			})
		}
	})

	t.Run("full load remains complete after narrow reads", func(t *testing.T) {
		full := repository.CharacterRecord{
			CharacterID: "selection_full_character",
			AccountID:   "dnf:selection:full",
			Slot:        7,
			Name:        "SelectionFull",
			Job:         "synthetic-job",
			Level:       89,
			Stats: map[string]int64{
				"exp":                    123456789,
				"selection_probe_marker": 424242,
			},
			Location: repository.CharacterLocation{
				ChannelID: 7,
				TownID:    38,
				DungeonID: 91234,
				RoomID:    "selection-room",
			},
			Roster: repository.CharacterRoster{
				Header: repository.CharacterRosterHeader{
					TotalOrSlotLimit: 32,
					UsedOrRemain:     1,
					SelectedOrPage:   3,
					RosterState:      2,
				},
				Entry: repository.CharacterRosterEntry{
					PackedJobGrow: 0x24,
					ByteC:         56,
					ObjectID:      17,
				},
			},
		}
		if err := repository.CreateCharacter(ctx, g.Character, full); err != nil {
			t.Fatal(err)
		}
		before, err := g.Character.ListByAccount(ctx, full.AccountID, 1)
		if err != nil {
			t.Fatal(err)
		}
		if len(before) != 1 {
			t.Fatalf("full records = %d, want 1", len(before))
		}
		assertSelectionReadsFullCharacter(t, before[0])

		narrow, err := identities.ListCharacterIdentities(ctx, full.AccountID, 1)
		if err != nil {
			t.Fatal(err)
		}
		if len(narrow) != 1 || narrow[0].CharacterID != full.CharacterID ||
			narrow[0].AccountID != full.AccountID || narrow[0].Slot != full.Slot {
			t.Fatalf("narrow identity = %+v", narrow)
		}
		loaded, found, err := g.Character.Load(ctx, full.CharacterID)
		if err != nil {
			t.Fatal(err)
		}
		if !found {
			t.Fatal("full character disappeared")
		}
		assertSelectionReadsFullCharacter(t, loaded)

		after, err := g.Character.ListByAccount(ctx, full.AccountID, 1)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(after, before) {
			t.Fatalf("full load changed after narrow reads:\nbefore=%+v\nafter=%+v", before, after)
		}
	})

	t.Run("same-scope SQL count", func(t *testing.T) {
		for _, count := range []int{1, 16, 17, 32} {
			account := fmt.Sprintf("dnf:selection:sql-count:%d", count)
			seedSelectionReadsCharacters(
				t, ctx, g.Character, db, schema, fmt.Sprintf("sql-count-%d", count), account, count,
			)

			queryCounter.resetQueries()
			start := time.Now()
			full, err := g.Character.ListByAccount(ctx, account, count)
			if err != nil {
				t.Fatal(err)
			}
			fullQueries := queryCounter.queries
			fullRows, fullTime := queryCounter.rows, time.Since(start)
			if len(full) != count {
				t.Fatalf("full records = %d, want %d", len(full), count)
			}

			queryCounter.resetQueries()
			start = time.Now()
			narrow, err := identities.ListCharacterIdentities(ctx, account, count)
			if err != nil {
				t.Fatal(err)
			}
			if len(narrow) != count {
				t.Fatalf("identity rows = %d, want %d", len(narrow), count)
			}
			selected, found, err := g.Character.Load(ctx, narrow[len(narrow)-1].CharacterID)
			if err != nil {
				t.Fatal(err)
			}
			narrowQueries := queryCounter.queries
			if !found {
				t.Fatal("selected character missing")
			}
			if narrowQueries > fullQueries || (count > 1 && narrowQueries == fullQueries) {
				t.Fatalf("identity plus one load used %d queries, full list used %d", narrowQueries, fullQueries)
			}
			if !reflect.DeepEqual(selected, full[len(full)-1]) {
				t.Fatalf("selected full record differs from ListByAccount result")
			}
			t.Logf("characters=%d full_queries=%d full_rows=%d full_us=%d narrow_queries=%d narrow_rows=%d narrow_us=%d", count, fullQueries, fullRows, fullTime.Microseconds(), narrowQueries, queryCounter.rows, time.Since(start).Microseconds())
		}
	})
}

func seedSelectionReadsCharacters(
	t *testing.T,
	ctx context.Context,
	characters repository.CharacterRepository,
	db *sql.DB,
	schema, namespace, account string,
	count int,
) ([]repository.CharacterRecord, string) {
	t.Helper()
	records := make([]repository.CharacterRecord, 0, count)
	for slot := 0; slot < count; slot++ {
		record := repository.CharacterRecord{
			CharacterID: fmt.Sprintf("selection_%s_slot_%02d", namespace, slot),
			AccountID:   account,
			Slot:        slot,
			Name:        fmt.Sprintf("Selection_%s_%02d", namespace, slot),
			Job:         "synthetic-job",
			Level:       70,
		}
		if err := repository.CreateCharacter(ctx, characters, record); err != nil {
			t.Fatalf("create character %q: %v", record.CharacterID, err)
		}
		records = append(records, record)
	}

	deletedID := fmt.Sprintf("selection_%s_deleted", namespace)
	query := "INSERT INTO " + mysqlTable(schema, defaultTablePrefix, mysqlCharTable) +
		" (character_id, account_id, slot, delete_flag) VALUES (?, ?, ?, 1)"
	if _, err := db.ExecContext(ctx, query, deletedID, account, -1); err != nil {
		t.Fatalf("insert deleted character %q: %v", deletedID, err)
	}
	return records, deletedID
}

func assertSelectionReadsFullCharacter(t *testing.T, record repository.CharacterRecord) {
	t.Helper()
	if record.CharacterID != "selection_full_character" ||
		record.AccountID != "dnf:selection:full" ||
		record.Slot != 7 ||
		record.Name != "SelectionFull" ||
		record.Job != "synthetic-job" ||
		record.Level != 89 {
		t.Fatalf("character identity/base fields incomplete: %+v", record)
	}
	if record.Stats["exp"] != 123456789 || record.Stats["selection_probe_marker"] != 424242 {
		t.Fatalf("character Stats incomplete: %+v", record.Stats)
	}
	if record.Location != (repository.CharacterLocation{
		ChannelID: 7,
		TownID:    38,
		DungeonID: 91234,
		RoomID:    "selection-room",
	}) {
		t.Fatalf("character Location incomplete: %+v", record.Location)
	}
	if record.Roster.Header.TotalOrSlotLimit != 32 ||
		record.Roster.Header.UsedOrRemain != 1 ||
		record.Roster.Header.SelectedOrPage != 3 ||
		record.Roster.Header.RosterState != 2 ||
		record.Roster.Entry.PackedJobGrow != 0x24 ||
		record.Roster.Entry.ByteC != 56 ||
		record.Roster.Entry.ObjectID != 17 {
		t.Fatalf("character Roster incomplete: %+v", record.Roster)
	}
}
