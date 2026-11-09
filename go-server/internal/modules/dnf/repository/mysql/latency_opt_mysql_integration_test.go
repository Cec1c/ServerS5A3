//go:build mysql_integration

package mysql

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	driver "github.com/go-sql-driver/mysql"
	"longheng.io/server/internal/modules/dnf/adventuregroup"
	"longheng.io/server/internal/modules/dnf/guild"
	r "longheng.io/server/internal/modules/dnf/repository"
)

type latencyLegacyCharacters struct{ r.CharacterRepository }
type latencyAdventureText struct{}

func (latencyAdventureText) ReadText(path string) (string, error) {
	return "[point bonus]\n1 90 10\n[/point bonus]\n[manage level point]\n10 1000 2000\n[/manage level point]\n[manage level max]\n3\n[/manage level max]\n[exp bonus]\n1 0\n[/exp bonus]\n[gold bonus]\n1 0\n[/gold bonus]\n[manage option]\n1 0\n[/manage option]", nil
}

func TestLatencyOptimizationDisposableMySQL(t *testing.T) {
	if os.Getenv("DNF_RUNTIME_DISPOSABLE_DB") != "1" {
		t.Fatal("requires explicit disposable database")
	}
	cfg, err := driver.ParseDSN(os.Getenv("DNFBRIDGE_PLAYER_TEST_DSN"))
	if err != nil || cfg.Net != "tcp" || cfg.DBName != "" {
		t.Fatal("invalid disposable DSN")
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
	db.SetMaxOpenConns(8)
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	schema := fmt.Sprintf("latency_opt_%d", time.Now().UnixNano())
	defer func() {
		if _, err := db.Exec("DROP DATABASE IF EXISTS `" + schema + "`"); err != nil {
			t.Error(err)
		}
	}()
	plan := r.DatabasePlan{ReadDatabases: []string{schema}, WriteDatabases: []string{schema}}
	if err = EnsureMySQLSchema(ctx, db, SchemaOptions{AutoCreate: true, CreateDatabases: true, DatabasePlan: plan}); err != nil {
		t.Fatal(err)
	}
	prepared := NewPreparedSQLDB(db, 64)
	defer prepared.Close()
	group, err := NewMySQLGroup(prepared, MySQLGroupOptions{DatabasePlan: plan})
	if err != nil {
		t.Fatal(err)
	}
	table := mysqlTable(schema, defaultTablePrefix, mysqlCharTable)
	statsTable := mysqlTable(schema, defaultTablePrefix, mysqlCharacterStatsTable)
	reg := guild.NewRegistry()
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	var g *guild.Guild
	owners := map[r.GuildOwner]bool{}
	accountSet := map[string]bool{}
	for start := 1; start <= 555; start += 100 {
		var values []string
		var args []any
		for i := start; i <= 555 && i < start+100; i++ {
			id, account := fmt.Sprint(i), fmt.Sprintf("a%03d", i%209)
			accountSet[account] = true
			c := r.CharacterRecord{CharacterID: id, AccountID: account, Name: fmt.Sprintf("M%04d", i), Slot: i / 209, Level: 90, Stats: map[string]int64{"server_group_id": 1, "pvp_grade": 8}}
			if i == 1 {
				g, err = reg.Create(c, "Latency", now)
				if err != nil {
					t.Fatal(err)
				}
				g.MemberLimit = 1000
			} else {
				if _, err = reg.JoinDirect(g.ID, c, now); err != nil {
					t.Fatal(err)
				}
			}
			deleted := 0
			if i == 555 {
				deleted = 1
			}
			owners[r.GuildOwner{CharacterID: id, AccountID: account}] = true
			values = append(values, "(?, ?, ?, ?, ?, ?, ?)")
			args = append(args, id, account, c.Name, c.Slot, c.Level, 8, deleted)
		}
		if _, err = db.ExecContext(ctx, "INSERT INTO "+table+" (character_id, account_id, name, slot, level, pvp_grade, delete_flag) VALUES "+strings.Join(values, ","), args...); err != nil {
			t.Fatal(err)
		}
	}
	g.Benefits = map[byte]guild.Benefit{1: {Type: 1, Value: 10, ExpiresAt: now.Add(time.Hour)}}
	if err = guild.Save(ctx, group.Settings, reg, now); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, "INSERT INTO "+statsTable+" (character_id, stat_key, stat_value) VALUES (?, ?, ?)", "1", "raid_clear_v1_1_01234567890123456789012345678901", now.Unix()); err != nil {
		t.Fatal(err)
	}
	management, err := adventuregroup.Load(ctx, latencyAdventureText{})
	if err != nil {
		t.Fatal(err)
	}
	rules := &guild.RankingRules{Members: guild.RankingRate{Maximum: 100, Rate: .1}, WeeklyActive: guild.RankingRate{Maximum: 90, Rate: .1}, Adventure: []guild.RankingRange{{Min: 1, Max: 30, Points: 3}}, PVP: []guild.RankingRange{{Min: 0, Max: 21, Points: 1}}, RaidClears: map[string]guild.RankingPoints{"anton": {Points: 1}, "luke": {Points: 2}, "fiendwar": {Points: 2}}}
	status := func() map[string]int64 {
		rows, e := db.QueryContext(ctx, "SHOW GLOBAL STATUS WHERE Variable_name IN ('Questions','Bytes_sent','Innodb_buffer_pool_reads','Innodb_row_lock_waits')")
		if e != nil {
			t.Fatal(e)
		}
		defer rows.Close()
		out := map[string]int64{}
		for rows.Next() {
			var k string
			var n int64
			if e = rows.Scan(&k, &n); e != nil {
				t.Fatal(e)
			}
			out[k] = n
		}
		if e = rows.Err(); e != nil {
			t.Fatal(e)
		}
		return out
	}
	measurement := func(label string, fn func()) map[string]any {
		before := status()
		var allocationBefore, allocationAfter runtime.MemStats
		runtime.ReadMemStats(&allocationBefore)
		start := time.Now()
		fn()
		elapsed := time.Since(start)
		runtime.ReadMemStats(&allocationAfter)
		after := status()
		delta := map[string]int64{}
		for k, n := range after {
			delta[k] = n - before[k]
		}
		return map[string]any{"label": label, "ms": float64(elapsed) / float64(time.Millisecond), "status": delta, "allocatedBytes": allocationAfter.TotalAlloc - allocationBefore.TotalAlloc, "mallocs": allocationAfter.Mallocs - allocationBefore.Mallocs}
	}
	var original, optimized map[string]guild.RankingMemberFacts
	var measurements []map[string]any
	for round := 0; round < 3; round++ {
		runLegacy := func() {
			facts, e := guild.SampleRankingMembers(ctx, latencyLegacyCharacters{group.Character}, reg, management, 32, owners, nil)
			if e != nil {
				t.Fatal(e)
			}
			original = facts
		}
		runNew := func() {
			facts, e := guild.SampleRankingMembers(ctx, group.Character, reg, management, 32, owners, nil)
			if e != nil {
				t.Fatal(e)
			}
			optimized = facts
		}
		if round%2 == 0 {
			measurements = append(measurements, measurement("ranking-legacy", runLegacy), measurement("ranking-batch", runNew))
		} else {
			measurements = append(measurements, measurement("ranking-batch", runNew), measurement("ranking-legacy", runLegacy))
		}
		oldFactors, e := guild.RankingMemberFactors(g, original, rules, now)
		if e != nil {
			t.Fatal(e)
		}
		newFactors, e := guild.RankingMemberFactors(g, optimized, rules, now)
		if e != nil || !reflect.DeepEqual(oldFactors, newFactors) {
			t.Fatalf("ranking factors changed: %+v %+v %v", oldFactors, newFactors, e)
		}
		if optimized["555"].Character.Stats["delete_flag"] != 1 || optimized["555"].Online {
			t.Fatal("deleted proof changed")
		}
	}
	owner := r.GuildOwner{CharacterID: "1", AccountID: fmt.Sprintf("a%03d", 1%209)}
	measurements = append(measurements, measurement("entitlement-legacy-100", func() {
		for i := 0; i < 100; i++ {
			loaded, e := guild.Load(ctx, group.Settings)
			if e != nil {
				t.Fatal(e)
			}
			if _, _, ok := loaded.GuildForOwner(owner); !ok {
				t.Fatal("legacy membership lost")
			}
		}
	}))
	measurements = append(measurements, measurement("entitlement-snapshot-100", func() {
		for i := 0; i < 100; i++ {
			v, ok, e := guild.ReadGuildEntitlement(ctx, group.Settings, owner)
			if e != nil || !ok || v.ID != 1 {
				t.Fatal(v, ok, e)
			}
		}
	}))
	t.Run("direct-writer-same-timestamp", func(t *testing.T) {
		member := g.Members["1"]
		member.AccountID = "foreign"
		g.Members["1"] = member
		encoded, e := json.Marshal(reg)
		if e != nil {
			t.Fatal(e)
		}
		values := mysqlTable(schema, defaultTablePrefix, mysqlSettingValuesTable)
		if _, e = db.ExecContext(ctx, "UPDATE "+values+" SET entry_value=? WHERE scope=? AND entry_key='state'", string(encoded), guild.RegistryScope); e != nil {
			t.Fatal(e)
		}
		if _, ok, e := guild.ReadGuildEntitlement(ctx, group.Settings, owner); e != nil || ok {
			t.Fatal("same timestamp hid GM-style update", ok, e)
		}
	})
	t.Run("batched-stats-and-rollback", func(t *testing.T) {
		t.Setenv(currentMysqlInsertBatchEnv, "200")
		stats := map[string]int64{}
		for i := 0; i < 501; i++ {
			stats[fmt.Sprintf("latency_%04d", i)] = int64(i)
		}
		stats[r.LevelUpEventKey(90)] = 1
		stats[r.LevelUpRewardStatPrefix+"90"] = 1
		tx, e := db.BeginTx(ctx, nil)
		if e != nil {
			t.Fatal(e)
		}
		if e = saveChangedCharacterStats(ctx, sqlTransactionDB{tx: tx}, statsTable, "1", nil, stats); e != nil {
			tx.Rollback()
			t.Fatal(e)
		}
		if e = tx.Commit(); e != nil {
			t.Fatal(e)
		}
		tx, e = db.BeginTx(ctx, nil)
		if e != nil {
			t.Fatal(e)
		}
		next := map[string]int64{}
		for k, n := range stats {
			next[k] = n
		}
		next["latency_0000"] = 999
		next[r.LevelUpEventKey(90)] = 0
		if e = saveChangedCharacterStats(ctx, sqlTransactionDB{tx: tx}, statsTable, "1", stats, next); e != nil {
			tx.Rollback()
			t.Fatal(e)
		}
		if e = tx.Rollback(); e != nil {
			t.Fatal(e)
		}
		var n int64
		if e = db.QueryRowContext(ctx, "SELECT stat_value FROM "+statsTable+" WHERE character_id='1' AND stat_key='latency_0000'").Scan(&n); e != nil || n != 0 {
			t.Fatal("rollback lost", n, e)
		}
	})
	t.Run("pet-scoped-slot-and-entry-deltas", func(t *testing.T) {
		t.Setenv(r.InventorySaveScopeEnvName, r.SaveScopeSlot)
		t.Setenv(r.EquipmentSaveScopeEnvName, r.SaveScopeEntry)
		initial := r.InventoryRecord{CharacterID: "1", Slots: map[string]r.ItemStack{"0:1": {ItemID: 1, Count: 1, Extra: map[string]string{"keep": "old"}}, "0:2": {ItemID: 2, Count: 8, Extra: map[string]string{"keep": "unchanged"}}}}
		if e := group.Inventory.Save(ctx, initial); e != nil {
			t.Fatal(e)
		}
		wear := r.EquipmentRecord{CharacterID: "1", Entries: map[string]r.EquipmentEntry{"1": {SlotIndex: 1, ItemID: 3, RawEntry: []byte{1, 2}, Extra: map[string]string{"keep": "old"}}, "2": {SlotIndex: 2, ItemID: 4, RawEntry: []byte{3, 4}, Extra: map[string]string{"keep": "unchanged"}}}}
		if e := group.Equipment.Save(ctx, wear); e != nil {
			t.Fatal(e)
		}
		nextCount := int64(7)
		apply := func(inv r.InventoryRepository, equip r.EquipmentRepository, pets r.PetRepository) error {
			record, _, e := inv.Load(ctx, "1")
			if e != nil {
				return e
			}
			record.Slots["0:1"] = r.ItemStack{ItemID: 5, Count: nextCount}
			if e = r.SaveInventoryFieldsScoped(ctx, inv, record, []r.InventoryField{r.InventoryFieldSlots}, r.InventorySlotDeltaKeys{"slots": []string{"0:1"}}); e != nil {
				return e
			}
			equipped, _, e := equip.Load(ctx, "1")
			if e != nil {
				return e
			}
			delete(equipped.Entries, "1")
			return r.SaveEquipmentFieldsScoped(ctx, equip, equipped, []string{"1"})
		}
		if e := group.CharacterPets.WithinCharacterPets(ctx, "1", apply); e != nil {
			t.Fatal(e)
		}
		actual, _, e := group.Inventory.Load(ctx, "1")
		if e != nil || actual.Slots["0:1"].Count != 7 || !reflect.DeepEqual(actual.Slots["0:2"], initial.Slots["0:2"]) {
			t.Fatal("slot delta mismatch", actual, e)
		}
		actualWear, _, e := group.Equipment.Load(ctx, "1")
		if e != nil || len(actualWear.Entries) != 1 || !reflect.DeepEqual(actualWear.Entries["2"], wear.Entries["2"]) {
			t.Fatal("equipment delta mismatch", actualWear, e)
		}
		nextCount = 99
		if e := group.CharacterPets.WithinCharacterPets(ctx, "1", func(inv r.InventoryRepository, equip r.EquipmentRepository, pets r.PetRepository) error {
			if e := apply(inv, equip, pets); e != nil {
				return e
			}
			return fmt.Errorf("injected rollback")
		}); e == nil {
			t.Fatal("injected rollback lost")
		}
		after, _, e := group.Inventory.Load(ctx, "1")
		if e != nil || !reflect.DeepEqual(after.Slots, actual.Slots) {
			t.Fatal("rollback changed slots", after, e)
		}
	})
	var accounts []string
	for id := range accountSet {
		accounts = append(accounts, id)
	}
	sort.Strings(accounts)
	encoded, _ := json.Marshal(reg)
	result := map[string]any{"members": 555, "accounts": len(accounts), "registryBytes": len(encoded), "measurements": measurements, "actualDatabase": true, "syntheticData": true, "fullGameSessions": false, "production": false}
	if path := os.Getenv("LATENCY_OPT_MEASUREMENTS"); path != "" {
		b, _ := json.MarshalIndent(result, "", "  ")
		if err = os.WriteFile(path, b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("measurements=%v", measurements)
}
