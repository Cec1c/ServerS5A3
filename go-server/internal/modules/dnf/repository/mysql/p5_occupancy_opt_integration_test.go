//go:build mysql_integration

package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	driver "github.com/go-sql-driver/mysql"
	"longheng.io/server/internal/modules/dnf/onlineevent"
	r "longheng.io/server/internal/modules/dnf/repository"
)

func openP5DisposableSchema(t *testing.T, ctx context.Context) (*sql.DB, string, func()) {
	t.Helper()
	if os.Getenv("DNF_RUNTIME_DISPOSABLE_DB") != "1" {
		t.Fatal("requires explicit disposable database (DNF_RUNTIME_DISPOSABLE_DB=1)")
	}
	cfg, err := driver.ParseDSN(os.Getenv("DNFBRIDGE_PLAYER_TEST_DSN"))
	if err != nil || cfg.Net != "tcp" || cfg.DBName != "" {
		t.Fatal("requires TCP DSN without an existing schema")
	}
	host, _, err := net.SplitHostPort(cfg.Addr)
	if err != nil || host != "127.0.0.1" {
		t.Fatal("requires loopback")
	}
	config := cfg.Clone()
	config.ParseTime = true
	config.Params = map[string]string{"charset": "utf8mb4"}
	db, err := sql.Open("mysql", config.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(2)
	schema := fmt.Sprintf("p5_opt_%d", time.Now().UnixNano())
	plan := r.DatabasePlan{ReadDatabases: []string{schema}, WriteDatabases: []string{schema}}
	if err := EnsureMySQLSchema(ctx, db, SchemaOptions{AutoCreate: true, CreateDatabases: true, DatabasePlan: plan}); err != nil {
		db.Close()
		t.Fatal(err)
	}
	cleanup := func() {
		_, _ = db.ExecContext(context.Background(), "DROP DATABASE IF EXISTS `"+schema+"`")
		_ = db.Close()
	}
	return db, schema, cleanup
}

func readSessionStatusMap(t *testing.T, ctx context.Context, conn *sql.Conn) map[string]int64 {
	t.Helper()
	rows, err := conn.QueryContext(ctx, "SHOW SESSION STATUS WHERE Variable_name IN ('Created_tmp_tables','Created_tmp_disk_tables','Sort_scan','Sort_rows')")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := make(map[string]int64, 4)
	for rows.Next() {
		var name string
		var val int64
		if err := rows.Scan(&name, &val); err != nil {
			t.Fatal(err)
		}
		out[name] = val
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestP5InventoryProjectionMariaDBExplainAndSessionTempTables(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	db, schema, cleanup := openP5DisposableSchema(t, ctx)
	defer cleanup()

	plan := r.DatabasePlan{ReadDatabases: []string{schema}, WriteDatabases: []string{schema}}
	group, err := NewMySQLGroupFromDB(db, MySQLGroupOptions{DatabasePlan: plan})
	if err != nil {
		t.Fatal(err)
	}

	// Seed representative bag with 120 slots (including empty-prefix raid tickets,
	// escaped literal prefix, multi-extra rows, NULL raw/extra, and unrelated items).
	seed := r.InventoryRecord{
		CharacterID: "char-p5-inv",
		Slots:       map[string]r.ItemStack{},
		Warehouse:   map[string]r.ItemStack{},
	}
	for i := 0; i < 60; i++ {
		seed.Slots[fmt.Sprintf("slot:%03d", i)] = r.ItemStack{
			ItemID:   3330,
			Count:    int64(i + 1),
			RawEntry: []byte{1, 2, 3, byte(i)},
			Extra: map[string]string{
				"source": "raid",
				"tag":    fmt.Sprintf("v-%03d", i),
				"z_note": strings.Repeat("x", 128),
			},
		}
		seed.Slots[fmt.Sprintf("other:%03d", i)] = r.ItemStack{
			ItemID:   9999,
			Count:    1,
			RawEntry: []byte{9, 9},
		}
	}
	seed.Slots["slot:no_extra"] = r.ItemStack{ItemID: 3330, Count: 1}
	seed.Slots["lit!_%:1"] = r.ItemStack{ItemID: 3330, Count: 4, Extra: map[string]string{"k": "v"}}
	seed.Slots["litXAq:1"] = r.ItemStack{ItemID: 3330, Count: 4}
	if err := group.Inventory.Save(ctx, seed); err != nil {
		t.Fatal(err)
	}
	if err := group.Inventory.Save(ctx, r.InventoryRecord{CharacterID: "char-p5-empty", Slots: map[string]r.ItemStack{}}); err != nil {
		t.Fatal(err)
	}

	// 1. Correctness equivalence across full Load vs both projections.
	full, found, err := group.Inventory.Load(ctx, "char-p5-inv")
	if err != nil || !found {
		t.Fatalf("full Load failed: found=%v err=%v", found, err)
	}
	bagStore := group.Inventory.(*mysqlBagStore)
	gotPrefix, err := bagStore.LoadInventorySlotItemPrefix(ctx, "char-p5-inv", "slot:", 3330)
	if err != nil {
		t.Fatal(err)
	}
	wantPrefix := map[string]r.ItemStack{}
	wantAll := map[string]r.ItemStack{}
	for k, st := range full.Slots {
		if st.ItemID == 3330 {
			wantAll[k] = st
			if strings.HasPrefix(k, "slot:") {
				wantPrefix[k] = st
			}
		}
	}
	if !reflect.DeepEqual(gotPrefix, wantPrefix) {
		t.Fatalf("LoadInventorySlotItemPrefix mismatch: got=%d want=%d", len(gotPrefix), len(wantPrefix))
	}
	gotFoundEmpty, okFoundEmpty, err := bagStore.LoadInventorySlotItemPrefixFound(ctx, "char-p5-inv", "", 3330)
	if err != nil || !okFoundEmpty || !reflect.DeepEqual(gotFoundEmpty, wantAll) {
		t.Fatalf("LoadInventorySlotItemPrefixFound empty prefix mismatch: found=%v got=%d want=%d err=%v", okFoundEmpty, len(gotFoundEmpty), len(wantAll), err)
	}
	gotEsc, okEsc, err := bagStore.LoadInventorySlotItemPrefixFound(ctx, "char-p5-inv", "lit!_%:", 3330)
	if err != nil || !okEsc || len(gotEsc) != 1 || gotEsc["lit!_%:1"].Count != 4 {
		t.Fatalf("escaped prefix mismatch: got=%+v found=%v err=%v", gotEsc, okEsc, err)
	}
	gotEmptyOwner, okEmptyOwner, err := bagStore.LoadInventorySlotItemPrefixFound(ctx, "char-p5-empty", "", 3330)
	if err != nil || !okEmptyOwner || len(gotEmptyOwner) != 0 {
		t.Fatalf("present empty owner mismatch: got=%+v found=%v err=%v", gotEmptyOwner, okEmptyOwner, err)
	}
	gotAbsent, okAbsent, err := bagStore.LoadInventorySlotItemPrefixFound(ctx, "char-p5-missing", "", 3330)
	if err != nil || okAbsent || len(gotAbsent) != 0 {
		t.Fatalf("absent owner mismatch: got=%+v found=%v err=%v", gotAbsent, okAbsent, err)
	}

	// 2. EXPLAIN FORMAT=JSON and dedicated single-connection SESSION temp-table delta:
	// compare old ORDER BY SQL vs new no-ORDER-BY SQL on exact same MariaDB schema.
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	owners := "`" + schema + "`.`dnf_inventories`"
	items := "`" + schema + "`.`dnf_inventory_items`"
	extra := "`" + schema + "`.`dnf_inventory_item_extra`"
	cases := []struct {
		name     string
		newQuery string
		oldQuery string
		args     []any
	}{
		{
			name: "LoadInventorySlotItemPrefix",
			newQuery: "SELECT i.entry_key, i.item_id, i.item_count, i.bind_flag, i.expire_at, i.raw_entry, e.extra_key, e.extra_value FROM " + items + " i" +
				" INNER JOIN " + owners + " b ON b.character_id = i.character_id" +
				" LEFT JOIN " + extra + " e ON e.character_id = i.character_id AND e.collection_name = i.collection_name AND e.entry_key = i.entry_key" +
				" WHERE i.character_id = ? AND i.collection_name = ? AND i.item_id = ? AND i.entry_key LIKE ? ESCAPE '!'",
			oldQuery: "SELECT i.entry_key, i.item_id, i.item_count, i.bind_flag, i.expire_at, i.raw_entry, e.extra_key, e.extra_value FROM " + items + " i" +
				" INNER JOIN " + owners + " b ON b.character_id = i.character_id" +
				" LEFT JOIN " + extra + " e ON e.character_id = i.character_id AND e.collection_name = i.collection_name AND e.entry_key = i.entry_key" +
				" WHERE i.character_id = ? AND i.collection_name = ? AND i.item_id = ? AND i.entry_key LIKE ? ESCAPE '!' ORDER BY i.entry_key, e.extra_key",
			args: []any{"char-p5-inv", "slots", int64(3330), "slot:%"},
		},
		{
			name: "LoadInventorySlotItemPrefixFound_empty_prefix",
			newQuery: "SELECT b.character_id, i.entry_key, i.item_id, i.item_count, i.bind_flag, i.expire_at, i.raw_entry, e.extra_key, e.extra_value FROM " + owners + " b" +
				" LEFT JOIN " + items + " i ON i.character_id = b.character_id AND i.collection_name = ? AND i.item_id = ?" +
				" LEFT JOIN " + extra + " e ON e.character_id = i.character_id AND e.collection_name = i.collection_name AND e.entry_key = i.entry_key" +
				" WHERE b.character_id = ?",
			oldQuery: "SELECT b.character_id, i.entry_key, i.item_id, i.item_count, i.bind_flag, i.expire_at, i.raw_entry, e.extra_key, e.extra_value FROM " + owners + " b" +
				" LEFT JOIN " + items + " i ON i.character_id = b.character_id AND i.collection_name = ? AND i.item_id = ?" +
				" LEFT JOIN " + extra + " e ON e.character_id = i.character_id AND e.collection_name = i.collection_name AND e.entry_key = i.entry_key" +
				" WHERE b.character_id = ? ORDER BY i.entry_key, e.extra_key",
			args: []any{"slots", int64(3330), "char-p5-inv"},
		},
	}

	const repeats = 20
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var oldPlan, newPlan string
			if err := conn.QueryRowContext(ctx, "EXPLAIN FORMAT=JSON "+tc.oldQuery, tc.args...).Scan(&oldPlan); err != nil {
				t.Fatal(err)
			}
			if err := conn.QueryRowContext(ctx, "EXPLAIN FORMAT=JSON "+tc.newQuery, tc.args...).Scan(&newPlan); err != nil {
				t.Fatal(err)
			}
			t.Logf("[%s] OLD EXPLAIN=%s", tc.name, strings.ReplaceAll(oldPlan, "\n", " "))
			t.Logf("[%s] NEW EXPLAIN=%s", tc.name, strings.ReplaceAll(newPlan, "\n", " "))

			runN := func(q string) map[string]int64 {
				before := readSessionStatusMap(t, ctx, conn)
				for i := 0; i < repeats; i++ {
					rows, err := conn.QueryContext(ctx, q, tc.args...)
					if err != nil {
						t.Fatal(err)
					}
					for rows.Next() {
					}
					if err := rows.Close(); err != nil {
						t.Fatal(err)
					}
				}
				after := readSessionStatusMap(t, ctx, conn)
				delta := map[string]int64{}
				for k, v := range after {
					delta[k] = v - before[k]
				}
				return delta
			}

			oldDelta := runN(tc.oldQuery)
			newDelta := runN(tc.newQuery)
			t.Logf("[%s] %dx OLD session delta=%+v | NEW session delta=%+v", tc.name, repeats, oldDelta, newDelta)

			if newDelta["Created_tmp_disk_tables"] != 0 {
				t.Fatalf("[%s] NEW query still created disk temp tables: %+v", tc.name, newDelta)
			}
			if newDelta["Created_tmp_tables"] != 0 || newDelta["Sort_scan"] != 0 {
				t.Fatalf("[%s] NEW query still triggered tmp/sort: %+v", tc.name, newDelta)
			}
			if strings.Contains(newPlan, `"temporary_table"`) || strings.Contains(newPlan, `"filesort"`) {
				t.Fatalf("[%s] NEW EXPLAIN still contains temporary_table/filesort: %s", tc.name, newPlan)
			}
		})
	}
}

func TestP5CharacterExtraStatsMariaDBEquivalenceAndRowReduction(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	db, schema, cleanup := openP5DisposableSchema(t, ctx)
	defer cleanup()

	plan := r.DatabasePlan{ReadDatabases: []string{schema}, WriteDatabases: []string{schema}}
	group, err := NewMySQLGroupFromDB(db, MySQLGroupOptions{DatabasePlan: plan})
	if err != nil {
		t.Fatal(err)
	}

	starts := time.Date(2026, 11, 20, 6, 0, 0, 0, onlineevent.ChinaCalendar)
	campaign := onlineevent.EpicCampaign{Starts: starts, Ends: starts.Add(28 * 24 * time.Hour)}
	now := starts.Add(10 * 24 * time.Hour)
	semPrefix := onlineevent.SemesterJournalKey(campaign)

	// Seed character with 80 unrelated stats + semester & epic stats.
	stats := map[string]int64{
		semPrefix + ":day_bitmap":       0xFF, // 8 days qualified
		semPrefix + ":qualified_days":   8,
		semPrefix + ":version":          1,
		semPrefix + ":claim_bitmap":     1,
		semPrefix + ":claim_observed":   now.Unix() - 60,
		campaign.Key() + ":invitations": 9500,
		campaign.Key() + ":claimed":     1,
		campaign.Key() + ":observed":    now.Unix() - 30,
	}
	for i := 0; i < 80; i++ {
		stats[fmt.Sprintf("unrelated_stat_%03d", i)] = int64(i + 1)
	}
	if err := group.Character.Save(ctx, r.CharacterRecord{
		CharacterID: "501",
		AccountID:   "acc-501",
		Slot:        0,
		Name:        "SemesterHero",
		Job:         "slayer",
		Level:       90,
		Stats:       stats,
		CreatedAt:   starts,
		UpdatedAt:   now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := group.Character.Save(ctx, r.CharacterRecord{
		CharacterID: "502",
		AccountID:   "acc-502",
		Level:       16,
		Stats:       map[string]int64{},
		CreatedAt:   starts,
		UpdatedAt:   now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := group.Character.Save(ctx, r.CharacterRecord{
		CharacterID: "503",
		AccountID:   "acc-503",
		Level:       17,
		Stats:       map[string]int64{},
		CreatedAt:   starts,
		UpdatedAt:   now,
	}); err != nil {
		t.Fatal(err)
	}

	obs := &SQLObservation{}
	observed := &ObservedSQLDB{SQLDB: SQLDBAdapter{DB: db}, Observation: obs}
	obsGroup, err := NewMySQLGroup(observed, MySQLGroupOptions{DatabasePlan: plan})
	if err != nil {
		t.Fatal(err)
	}
	counts := func() (calls, rows int64) {
		for path := range obs.series {
			for _, op := range []int{sqlObserveQuery, sqlObserveRow} {
				for j := range obs.series[path][op].buckets {
					calls += obs.series[path][op].buckets[j].Load()
				}
				rows += obs.series[path][op].rows.Load()
			}
		}
		return calls, rows
	}

	// Compare full Load vs LoadCharacterExtraStats for semester & epic snapshots.
	for _, cid := range []string{"501", "502", "503", "999"} {
		fullRec, fullFound, fullErr := obsGroup.Character.Load(ctx, cid)
		cBefore, rBefore := counts()
		narrowRec, narrowFound, narrowErr := r.LoadCharacterExtraStats(ctx, obsGroup.Character, cid, onlineevent.SemesterCharacterStatKeys(campaign)...)
		cAfter, rAfter := counts()
		if (fullErr != nil) != (narrowErr != nil) || fullFound != narrowFound {
			t.Fatalf("cid=%s found/err mismatch: full=(%v,%v) narrow=(%v,%v)", cid, fullFound, fullErr, narrowFound, narrowErr)
		}
		if cAfter-cBefore != 1 {
			t.Fatalf("cid=%s expected 1 narrow SQL query, got %d", cid, cAfter-cBefore)
		}
		if cid == "501" {
			t.Logf("cid=501 narrow semester query read %d rows (vs ~80+ unrelated stats)", rAfter-rBefore)
			if rAfter-rBefore > 5 {
				t.Fatalf("cid=501 narrow read returned too many rows: %d", rAfter-rBefore)
			}
		}
		if fullFound {
			narrowAsRec := r.CharacterRecord{CharacterID: narrowRec.CharacterID, AccountID: narrowRec.AccountID, Level: narrowRec.Level, Stats: narrowRec.Stats}
			sFull, errFull := onlineevent.SemesterCharacterSnapshot(fullRec, campaign, now)
			sNarrow, errNarrow := onlineevent.SemesterCharacterSnapshot(narrowAsRec, campaign, now)
			if (errFull != nil) != (errNarrow != nil) || !reflect.DeepEqual(sFull, sNarrow) {
				t.Fatalf("cid=%s semester snapshot mismatch: full=(%+v,%v) narrow=(%+v,%v)", cid, sFull, errFull, sNarrow, errNarrow)
			}
			// 05:59:59 vs 06:00 boundary check
			beforeSix := time.Date(2026, 11, 29, 5, 59, 59, 0, onlineevent.ChinaCalendar)
			atSix := time.Date(2026, 11, 29, 6, 0, 0, 0, onlineevent.ChinaCalendar)
			for _, ts := range []time.Time{beforeSix, atSix} {
				a, ea := onlineevent.SemesterCharacterSnapshot(fullRec, campaign, ts)
				b, eb := onlineevent.SemesterCharacterSnapshot(narrowAsRec, campaign, ts)
				if (ea != nil) != (eb != nil) || !reflect.DeepEqual(a, b) {
					t.Fatalf("cid=%s boundary %v mismatch: full=(%+v,%v) narrow=(%+v,%v)", cid, ts, a, ea, b, eb)
				}
			}

			// Epic snapshot equivalence
			eProj, eFound, eErr := r.LoadCharacterExtraStats(ctx, obsGroup.Character, cid, onlineevent.EpicCharacterStatKeys(campaign)...)
			if eErr != nil || !eFound {
				t.Fatalf("cid=%s epic narrow failed: found=%v err=%v", cid, eFound, eErr)
			}
			eAsRec := r.CharacterRecord{CharacterID: eProj.CharacterID, AccountID: eProj.AccountID, Level: eProj.Level, Stats: eProj.Stats}
			eFull, eFullErr := onlineevent.EpicCharacterSnapshot(fullRec, campaign, now)
			eNarrow, eNarrowErr := onlineevent.EpicCharacterSnapshot(eAsRec, campaign, now)
			if (eFullErr != nil) != (eNarrowErr != nil) || !reflect.DeepEqual(eFull, eNarrow) {
				t.Fatalf("cid=%s epic snapshot mismatch: full=(%+v,%v) narrow=(%+v,%v)", cid, eFull, eFullErr, eNarrow, eNarrowErr)
			}
		}
	}

	// Verify case-insensitive DB key collision does NOT leak wrong-case key into Go map.
	statsTable := "`" + schema + "`.`dnf_character_stats`"
	if _, err := db.ExecContext(ctx, "INSERT INTO "+statsTable+" (character_id, stat_key, stat_value) VALUES (?, ?, ?)", "503", strings.ToUpper(semPrefix+":day_bitmap"), 7); err != nil {
		t.Fatal(err)
	}
	got503, found503, err := r.LoadCharacterExtraStats(ctx, obsGroup.Character, "503", onlineevent.SemesterCharacterStatKeys(campaign)...)
	if err != nil || !found503 {
		t.Fatalf("cid=503 failed: found=%v err=%v", found503, err)
	}
	if len(got503.Stats) != 0 {
		t.Fatalf("utf8mb4_unicode_ci uppercase row leaked into case-sensitive Go map: %+v", got503.Stats)
	}
}
