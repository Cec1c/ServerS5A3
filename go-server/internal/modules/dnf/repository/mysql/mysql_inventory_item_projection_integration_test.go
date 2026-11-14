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
	r "longheng.io/server/internal/modules/dnf/repository"
)

func TestInventoryItemProjectionDisposableMariaDB(t *testing.T) {
	if os.Getenv("DNF_RUNTIME_DISPOSABLE_DB") != "1" {
		t.Fatal("requires explicit disposable database")
	}
	cfg, err := driver.ParseDSN(os.Getenv("DNFBRIDGE_PLAYER_TEST_DSN"))
	if err != nil || cfg.Net != "tcp" || cfg.DBName != "" {
		t.Fatal("requires TCP DSN without an existing schema")
	}
	host, _, err := net.SplitHostPort(cfg.Addr)
	if err != nil || host != "127.0.0.1" {
		t.Fatal("requires loopback")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	for _, interpolate := range []bool{false, true} {
		t.Run(fmt.Sprintf("interpolate=%t", interpolate), func(t *testing.T) {
			config := cfg.Clone()
			config.InterpolateParams, config.ParseTime = interpolate, true
			config.Params = map[string]string{"charset": "utf8mb4"}
			db, err := sql.Open("mysql", config.FormatDSN())
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			db.SetMaxOpenConns(2)
			schema := fmt.Sprintf("followup_%d", time.Now().UnixNano())
			defer func() {
				if _, err := db.ExecContext(ctx, "DROP DATABASE IF EXISTS `"+schema+"`"); err != nil {
					t.Error(err)
				}
			}()
			plan := r.DatabasePlan{ReadDatabases: []string{schema}, WriteDatabases: []string{schema}}
			if err := EnsureMySQLSchema(ctx, db, SchemaOptions{AutoCreate: true, CreateDatabases: true, DatabasePlan: plan}); err != nil {
				t.Fatal(err)
			}
			group, err := NewMySQLGroupFromDB(db, MySQLGroupOptions{DatabasePlan: plan})
			if err != nil {
				t.Fatal(err)
			}
			seed := r.InventoryRecord{CharacterID: "system:auction:escrow", Slots: map[string]r.ItemStack{}, Warehouse: map[string]r.ItemStack{}}
			for i := 0; i < 400; i++ {
				stack := r.ItemStack{ItemID: 7000, Count: 2, RawEntry: []byte{0, 39, 255}, Extra: map[string]string{
					"listing_id": fmt.Sprint(i + 1), "seller_id": "seller", "buyer_id": "buyer", "total_gold": "200", "fee_gold": "2", "sold_at": "1800000000000000000", "bidding": "false",
				}}
				if i%10 == 0 {
					stack.ItemID = 6001
				}
				seed.Slots[fmt.Sprintf("auction:history:%d:0", i)] = stack
				stack.ItemID = 6001
				seed.Slots[fmt.Sprintf("auction:listing:%d", i)] = stack
				seed.Slots[fmt.Sprintf("auction:outbox:%d", i)] = stack
				seed.Warehouse[fmt.Sprint(i)] = stack
			}
			seed.Slots["auction:history:no-extra"] = r.ItemStack{ItemID: 6001, Count: 1}
			seed.Slots["literal!_%:1"] = r.ItemStack{ItemID: 6001, Count: 1, Extra: map[string]string{"值": "中文'?\\\x00"}}
			seed.Slots["literalXAq:1"] = r.ItemStack{ItemID: 6001, Count: 1}
			if err := group.Inventory.Save(ctx, seed); err != nil {
				t.Fatal(err)
			}
			observation := &SQLObservation{}
			observed := &ObservedSQLDB{SQLDB: SQLDBAdapter{DB: db}, Observation: observation}
			store := &mysqlBagStore{mysqlStoreBase: mysqlStoreBase{router: mysqlRouter{db: observed, readDBs: []string{schema}, tablePrefix: "dnf"}}, trackSlotChanges: true}
			readRows := func() int64 {
				return observation.series[0][sqlObserveQuery].rows.Load() + observation.series[0][sqlObserveRow].rows.Load()
			}
			before := readRows()
			full, found, err := store.Load(ctx, seed.CharacterID)
			if err != nil || !found {
				t.Fatal(found, err)
			}
			fullRows := readRows() - before
			snapshot := store.loadedSnapshot
			before = readRows()
			prefix, err := store.LoadInventorySlotPrefix(ctx, seed.CharacterID, "auction:history:")
			if err != nil {
				t.Fatal(err)
			}
			prefixRows := readRows() - before
			before = readRows()
			got, err := store.LoadInventorySlotItemPrefix(ctx, seed.CharacterID, "auction:history:", 6001)
			if err != nil {
				t.Fatal(err)
			}
			itemRows := readRows() - before
			want := map[string]r.ItemStack{}
			for key, stack := range full.Slots {
				if strings.HasPrefix(key, "auction:history:") && stack.ItemID == 6001 {
					want[key] = stack
				}
			}
			if len(prefix) != 401 || len(got) != 41 || !reflect.DeepEqual(got, want) || store.loadedSnapshot != snapshot {
				t.Fatalf("projection mismatch: prefix=%d item=%d snapshot=%v", len(prefix), len(got), store.loadedSnapshot == snapshot)
			}
			if itemRows >= prefixRows || prefixRows >= fullRows {
				t.Fatalf("returned row reduction absent: full=%d prefix=%d item=%d", fullRows, prefixRows, itemRows)
			}
			t.Logf("actual SQL returned rows full=%d history-prefix=%d one-item=%d; complete slot values equal", fullRows, prefixRows, itemRows)
			literal, err := store.LoadInventorySlotItemPrefix(ctx, seed.CharacterID, "literal!_%:", 6001)
			if err != nil || len(literal) != 1 || !reflect.DeepEqual(literal["literal!_%:1"], full.Slots["literal!_%:1"]) {
				t.Fatal("literal prefix broadened", literal, err)
			}
			if empty, err := store.LoadInventorySlotItemPrefix(ctx, "absent", "auction:history:", 6001); err != nil || len(empty) != 0 {
				t.Fatal("absent owner returned inventory", err)
			}
			// Reads must not rewrite items, their attributes, or the warehouse.
			after, found, err := group.Inventory.Load(ctx, seed.CharacterID)
			if err != nil || !found || !reflect.DeepEqual(after, full) {
				t.Fatal("read-only projection changed persisted inventory", err)
			}
		})
	}
}
