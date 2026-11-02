package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/repository"
)

func characterGuildSummaryRow(id string, growType any) fakeSQLRow {
	return fakeSQLRow{values: []any{
		id, "account-" + id, "name-" + id, "job-" + id, 90, growType,
	}}
}

func characterGuildSummaryIDs(count int) []string {
	ids := make([]string, count)
	for idx := range ids {
		ids[idx] = fmt.Sprintf("character-%03d", idx)
	}
	return ids
}

func characterGuildSummaryStore(t *testing.T, database *fakeSQLDB) *mysqlCharStore {
	t.Helper()
	characters, ok := newTestMySQLGroup(t, database).Character.(*mysqlCharStore)
	if !ok {
		t.Fatal("MySQL character repository is not a mysqlCharStore")
	}
	return characters
}

func TestCharacterGuildSummaryNarrow190IDRead(t *testing.T) {
	t.Setenv(mysqlCharacterMirrorStatsAuthoritativeEnv, "")
	ids := characterGuildSummaryIDs(190)
	rows := make([]fakeSQLRow, 0, len(ids))
	for idx := len(ids) - 1; idx >= 0; idx-- {
		rows = append(rows, characterGuildSummaryRow(ids[idx], int64(33)))
	}
	database := &fakeSQLDB{rowsets: []fakeSQLRows{{rows: rows}}}
	characters := characterGuildSummaryStore(t, database)
	characters.router.tablePrefix = "dnf"
	characters.router.lockReads = true

	ctx, counts := repository.WithCharacterGuildSummaryReadCounts(context.Background())
	summaries, err := characters.LoadCharacterGuildSummaries(ctx, ids)
	if err != nil {
		t.Fatal(err)
	}
	if counts.Queries != 1 || counts.FullLoads != 0 {
		t.Fatalf("summary counts = queries:%d full_loads:%d, want 1/0", counts.Queries, counts.FullLoads)
	}
	if len(summaries) != len(ids) || summaries["character-000"].GrowType != 33 {
		t.Fatalf("summary count/first = %d/%+v", len(summaries), summaries["character-000"])
	}
	call := requireOneExec(t, database)
	wantPrefix := "SELECT character_id, account_id, name, job, level, grow_type FROM `dnf_s1_r1`.`dnf_characters` WHERE character_id IN ("
	if !strings.HasPrefix(call.Query, wantPrefix) || !strings.HasSuffix(call.Query, ") ORDER BY character_id ASC FOR UPDATE") {
		t.Fatalf("query = %q", call.Query)
	}
	if got := strings.Count(call.Query, "?"); got != len(ids) {
		t.Fatalf("placeholder count = %d, want %d", got, len(ids))
	}
	if strings.Contains(call.Query, "character_stats") || strings.Contains(call.Query, "JOIN") ||
		strings.Contains(call.Query, "delete_flag") || !reflect.DeepEqual(call.Args, stringsToAny(ids)) {
		t.Fatalf("wide or reordered read: query=%q args=%#v", call.Query, call.Args)
	}
}

func TestCharacterGuildSummaryDefaultIgnoresGrowTypeShadow(t *testing.T) {
	t.Setenv(mysqlCharacterMirrorStatsAuthoritativeEnv, "")
	database := &fakeSQLDB{rowsets: []fakeSQLRows{{rows: []fakeSQLRow{
		characterGuildSummaryRow("character-1", int64(33)),
	}}}}
	characters := characterGuildSummaryStore(t, database)
	ctx, counts := repository.WithCharacterGuildSummaryReadCounts(context.Background())

	summaries, err := characters.LoadCharacterGuildSummaries(ctx, []string{"character-1"})
	if err != nil {
		t.Fatal(err)
	}
	if summaries["character-1"].GrowType != 33 || counts.Queries != 1 || counts.FullLoads != 0 {
		t.Fatalf("summary=%+v counts=%+v", summaries["character-1"], counts)
	}
	call := requireOneExec(t, database)
	if strings.Contains(call.Query, "character_stats") || strings.Contains(call.Query, "JOIN") {
		t.Fatalf("default query unexpectedly consulted shadow stats: %q", call.Query)
	}
	if !reflect.DeepEqual(call.Args, []any{"character-1"}) {
		t.Fatalf("default args = %#v", call.Args)
	}
}

func TestCharacterGuildSummaryCompatibilityOffUsesGrowTypeShadow(t *testing.T) {
	t.Setenv(mysqlCharacterMirrorStatsAuthoritativeEnv, "off")
	for _, test := range []struct {
		name     string
		selected int64
	}{
		{name: "shadow zero overrides mirror", selected: 0},
		{name: "no shadow falls back to mirror", selected: 33},
	} {
		t.Run(test.name, func(t *testing.T) {
			database := &fakeSQLDB{rowsets: []fakeSQLRows{{rows: []fakeSQLRow{
				characterGuildSummaryRow("character-1", test.selected),
			}}}}
			characters := characterGuildSummaryStore(t, database)
			ctx, counts := repository.WithCharacterGuildSummaryReadCounts(context.Background())

			summaries, err := characters.LoadCharacterGuildSummaries(ctx, []string{"character-1"})
			if err != nil {
				t.Fatal(err)
			}
			if summaries["character-1"].GrowType != test.selected || counts.Queries != 1 || counts.FullLoads != 0 {
				t.Fatalf("summary=%+v counts=%+v", summaries["character-1"], counts)
			}
			call := requireOneExec(t, database)
			for _, part := range []string{
				"`dnf_s1_r1`.`dnf_characters` AS c",
				"LEFT JOIN `dnf_s1_r1`.`dnf_character_stats` AS s",
				"s.character_id = c.character_id AND s.stat_key = ?",
				"COALESCE(s.stat_value, c.grow_type) AS grow_type",
			} {
				if !strings.Contains(call.Query, part) {
					t.Fatalf("off query missing %q: %s", part, call.Query)
				}
			}
			if !reflect.DeepEqual(call.Args, []any{repository.CharacterGrowTypeStatKey, "character-1"}) {
				t.Fatalf("off args = %#v", call.Args)
			}
		})
	}
}

func TestCharacterGuildSummaryCompatibilityOffJoinsSameReadRoute(t *testing.T) {
	t.Setenv(mysqlCharacterMirrorStatsAuthoritativeEnv, "off")
	database := &fakeSQLDB{rowsets: []fakeSQLRows{{rows: []fakeSQLRow{
		characterGuildSummaryRow("route-a", int64(0)),
	}}}}
	characters := characterGuildSummaryStore(t, database)
	characters.router.readDBs = []string{"guild_read_b", "guild_read_a"}

	for idx := 0; idx < 1000; idx++ {
		id := fmt.Sprintf("route-character-%d", idx)
		if characters.router.readDBs[pickDatabase(id, len(characters.router.readDBs))] == "guild_read_a" {
			database.rowsets[0].rows[0] = characterGuildSummaryRow(id, int64(0))
			if _, err := characters.LoadCharacterGuildSummaries(context.Background(), []string{id}); err != nil {
				t.Fatal(err)
			}
			call := requireOneExec(t, database)
			if !strings.Contains(call.Query, "`guild_read_a`.`dnf_characters` AS c") ||
				!strings.Contains(call.Query, "`guild_read_a`.`dnf_character_stats` AS s") {
				t.Fatalf("off join did not stay on the character read route: %s", call.Query)
			}
			return
		}
	}
	t.Fatal("failed to find a route assigned to guild_read_a")
}

func TestCharacterGuildSummaryChunksAbove256(t *testing.T) {
	ids := characterGuildSummaryIDs(257)
	database := &fakeSQLDB{rowsets: []fakeSQLRows{
		{rows: []fakeSQLRow{
			characterGuildSummaryRow(ids[0], int64(1)),
			characterGuildSummaryRow(ids[256], int64(2)),
		}},
		{},
	}}
	characters := characterGuildSummaryStore(t, database)

	summaries, err := characters.LoadCharacterGuildSummaries(context.Background(), ids)
	if err != nil {
		t.Fatal(err)
	}
	if len(summaries) != 1 || summaries[ids[0]].GrowType != 1 || len(database.execs) != 2 {
		t.Fatalf("summaries=%+v queries=%d, want only first-chunk row and 2 queries", summaries, len(database.execs))
	}
	if _, ok := summaries[ids[256]]; ok {
		t.Fatalf("first chunk accepted an ID from the second chunk: %q", ids[256])
	}
	for idx, want := range []int{256, 1} {
		call := database.execs[idx]
		if got := strings.Count(call.Query, "?"); got != want {
			t.Fatalf("query %d placeholders = %d, want %d", idx, got, want)
		}
		if len(call.Args) != want {
			t.Fatalf("query %d args = %d, want %d", idx, len(call.Args), want)
		}
	}
}

func TestCharacterGuildSummaryGroupsByActualReadRoute(t *testing.T) {
	database := &fakeSQLDB{}
	characters := characterGuildSummaryStore(t, database)
	characters.router.readDBs = []string{"guild_read_b", "guild_read_a"}
	characters.router.tablePrefix = "dnf"

	var idA, idB string
	for idx := 0; idA == "" || idB == ""; idx++ {
		id := fmt.Sprintf("route-character-%d", idx)
		switch characters.router.readDBs[pickDatabase(id, len(characters.router.readDBs))] {
		case "guild_read_a":
			if idA == "" {
				idA = id
			}
		case "guild_read_b":
			if idB == "" {
				idB = id
			}
		}
	}
	database.rowsets = []fakeSQLRows{
		{rows: []fakeSQLRow{characterGuildSummaryRow(idB, int64(2)), characterGuildSummaryRow(idA, nil)}},
		{},
	}

	summaries, err := characters.LoadCharacterGuildSummaries(context.Background(), []string{idB, idA})
	if err != nil {
		t.Fatal(err)
	}
	if len(summaries) != 1 || summaries[idA].GrowType != 0 {
		t.Fatalf("summaries = %+v, want only correctly attributed %q with null grow=0", summaries, idA)
	}
	if len(database.execs) != 2 || !strings.Contains(database.execs[0].Query, "`guild_read_a`.`dnf_characters`") ||
		!strings.Contains(database.execs[1].Query, "`guild_read_b`.`dnf_characters`") {
		t.Fatalf("route order/queries = %v", execQueries(database))
	}
}

func TestCharacterGuildSummaryEmptyAndDuplicateInputs(t *testing.T) {
	for name, ids := range map[string][]string{
		"nil":       nil,
		"empty":     {},
		"blank":     {"", " \t\r\n "},
		"duplicate": {"7", " 7 "},
	} {
		t.Run(name, func(t *testing.T) {
			database := &fakeSQLDB{}
			characters := characterGuildSummaryStore(t, database)
			if name == "duplicate" {
				database.rowsets = []fakeSQLRows{{rows: []fakeSQLRow{characterGuildSummaryRow("7", int64(17))}}}
			}
			summaries, err := characters.LoadCharacterGuildSummaries(context.Background(), ids)
			if err != nil {
				t.Fatal(err)
			}
			wantQueries := 0
			if name == "duplicate" {
				wantQueries = 1
			}
			if len(database.execs) != wantQueries {
				t.Fatalf("queries = %d, want %d", len(database.execs), wantQueries)
			}
			if name == "duplicate" {
				if len(database.execs[0].Args) != 1 || !reflect.DeepEqual(database.execs[0].Args, []any{"7"}) || summaries["7"].GrowType != 17 {
					t.Fatalf("duplicate read args=%#v summaries=%+v", database.execs[0].Args, summaries)
				}
			} else if summaries == nil || len(summaries) != 0 {
				t.Fatalf("empty summaries = %#v, want non-nil empty map", summaries)
			}
		})
	}
}

func TestCharacterGuildSummaryCancellationAndMissingRows(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	database := &fakeSQLDB{}
	characters := characterGuildSummaryStore(t, database)
	got, err := characters.LoadCharacterGuildSummaries(ctx, []string{"a"})
	if !errors.Is(err, context.Canceled) || got != nil || len(database.execs) != 0 {
		t.Fatalf("canceled summary=%v err=%v queries=%d", got, err, len(database.execs))
	}

	database = &fakeSQLDB{rowsets: []fakeSQLRows{{}}}
	characters = characterGuildSummaryStore(t, database)
	got, err = characters.LoadCharacterGuildSummaries(context.Background(), []string{"missing"})
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("missing summary=%v err=%v, want non-nil empty map", got, err)
	}
}

type guildSummaryCloseRows struct {
	fakeSQLRows
	closed   bool
	closeErr error
}

func (r *guildSummaryCloseRows) Close() error {
	r.closed = true
	return r.closeErr
}

type guildSummaryCancelRows struct {
	guildSummaryCloseRows
	cancel context.CancelFunc
}

func (r *guildSummaryCancelRows) Next() bool {
	if r.idx == 0 {
		r.cancel()
	}
	return r.guildSummaryCloseRows.Next()
}

type guildSummaryErrorDB struct {
	queryErr error
	rows     SQLRows
	calls    []fakeSQLCall
}

func (d *guildSummaryErrorDB) ExecContext(context.Context, string, ...any) (sql.Result, error) {
	return nil, errors.New("unexpected write")
}

func (d *guildSummaryErrorDB) QueryContext(_ context.Context, query string, args ...any) (SQLRows, error) {
	d.calls = append(d.calls, fakeSQLCall{Query: query, Args: append([]any(nil), args...)})
	if d.queryErr != nil {
		return d.rows, d.queryErr
	}
	if d.rows != nil {
		return d.rows, nil
	}
	return &fakeSQLRows{}, nil
}

func (d *guildSummaryErrorDB) QueryRowContext(context.Context, string, ...any) SQLRow {
	return fakeSQLRow{err: errors.New("unexpected single-row query")}
}

func (d *guildSummaryErrorDB) PingContext(context.Context) error { return nil }

func guildSummaryErrorStore(database SQLDB) *mysqlCharStore {
	return &mysqlCharStore{mysqlStoreBase: mysqlStoreBase{router: mysqlRouter{
		db: database, readDBs: []string{"guild_read"}, writeDBs: []string{"guild_write"}, tablePrefix: "dnf",
	}}}
}

func TestCharacterGuildSummaryPropagatesQueryScanRowsAndCloseErrors(t *testing.T) {
	wantErr := errors.New("summary query failed")
	t.Run("query", func(t *testing.T) {
		database := &guildSummaryErrorDB{queryErr: wantErr}
		got, err := guildSummaryErrorStore(database).LoadCharacterGuildSummaries(context.Background(), []string{"a"})
		if !errors.Is(err, wantErr) || got != nil {
			t.Fatalf("summary=%v err=%v", got, err)
		}
	})

	t.Run("query_typed_nil_rows", func(t *testing.T) {
		database := &guildSummaryErrorDB{queryErr: wantErr, rows: (*sql.Rows)(nil)}
		got, err := guildSummaryErrorStore(database).LoadCharacterGuildSummaries(context.Background(), []string{"a"})
		if !errors.Is(err, wantErr) || got != nil {
			t.Fatalf("summary=%v err=%v", got, err)
		}
	})

	t.Run("scan", func(t *testing.T) {
		rows := &guildSummaryCloseRows{fakeSQLRows: fakeSQLRows{rows: []fakeSQLRow{
			characterGuildSummaryRow("a", int64(1)), {err: wantErr},
		}}}
		database := &guildSummaryErrorDB{rows: rows}
		got, err := guildSummaryErrorStore(database).LoadCharacterGuildSummaries(context.Background(), []string{"a", "b"})
		if !errors.Is(err, wantErr) || got != nil || !rows.closed {
			t.Fatalf("summary=%v err=%v closed=%t", got, err, rows.closed)
		}
	})

	t.Run("rows", func(t *testing.T) {
		rows := &guildSummaryCloseRows{fakeSQLRows: fakeSQLRows{
			rows: []fakeSQLRow{characterGuildSummaryRow("a", int64(1))}, err: wantErr,
		}}
		database := &guildSummaryErrorDB{rows: rows}
		got, err := guildSummaryErrorStore(database).LoadCharacterGuildSummaries(context.Background(), []string{"a"})
		if !errors.Is(err, wantErr) || got != nil || !rows.closed {
			t.Fatalf("summary=%v err=%v closed=%t", got, err, rows.closed)
		}
	})

	t.Run("close", func(t *testing.T) {
		rows := &guildSummaryCloseRows{closeErr: wantErr}
		database := &guildSummaryErrorDB{rows: rows}
		got, err := guildSummaryErrorStore(database).LoadCharacterGuildSummaries(context.Background(), []string{"a"})
		if !errors.Is(err, wantErr) || got != nil || !rows.closed {
			t.Fatalf("summary=%v err=%v closed=%t", got, err, rows.closed)
		}
	})

	t.Run("cancel during rows", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		rows := &guildSummaryCancelRows{
			guildSummaryCloseRows: guildSummaryCloseRows{fakeSQLRows: fakeSQLRows{rows: []fakeSQLRow{characterGuildSummaryRow("a", int64(1))}}},
			cancel:                cancel,
		}
		database := &guildSummaryErrorDB{rows: rows}
		got, err := guildSummaryErrorStore(database).LoadCharacterGuildSummaries(ctx, []string{"a"})
		if !errors.Is(err, context.Canceled) || got != nil || !rows.closed {
			t.Fatalf("summary=%v err=%v closed=%t", got, err, rows.closed)
		}
	})
}

func TestCharacterGuildSummaryQueryOrderIsStable(t *testing.T) {
	ids := []string{"z", "a", "m"}
	database := &fakeSQLDB{rowsets: []fakeSQLRows{{rows: []fakeSQLRow{
		characterGuildSummaryRow("a", int64(0)),
		characterGuildSummaryRow("m", int64(0)),
		characterGuildSummaryRow("z", int64(0)),
	}}}}
	characters := characterGuildSummaryStore(t, database)
	if _, err := characters.LoadCharacterGuildSummaries(context.Background(), ids); err != nil {
		t.Fatal(err)
	}
	want := []any{"a", "m", "z"}
	if !reflect.DeepEqual(database.execs[0].Args, want) {
		t.Fatalf("sorted args = %#v, want %#v", database.execs[0].Args, want)
	}
	sortedIDs := append([]string(nil), ids...)
	sort.Strings(sortedIDs)
	if !reflect.DeepEqual(stringsToAny(sortedIDs), want) {
		t.Fatal("test expectation is not sorted")
	}
}
