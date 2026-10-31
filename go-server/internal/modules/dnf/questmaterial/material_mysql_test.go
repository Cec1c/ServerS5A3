package questmaterial

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	driver "github.com/go-sql-driver/mysql"
	r "longheng.io/server/internal/modules/dnf/repository"
	m "longheng.io/server/internal/modules/dnf/repository/mysql"
)

func TestPersonalMaterialMariaDBTransactions(t *testing.T) {
	dsn := os.Getenv("DNFBRIDGE_PLAYER_TEST_DSN")
	if dsn == "" {
		t.Skip("requires disposable MariaDB")
	}
	cfg, err := driver.ParseDSN(dsn)
	if err != nil {
		t.Fatal("invalid isolated DSN")
	}
	if cfg.Net != "tcp" || cfg.Addr != "127.0.0.1:34323" {
		t.Fatal("refuse non-local disposable endpoint")
	}
	cfg.DBName = ""
	cfg.ParseTime = true
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal("open isolated DB")
	}
	defer db.Close()
	db.SetMaxOpenConns(12)
	name := fmt.Sprintf("party_material_test_%d", time.Now().UnixNano())
	var version string
	if err := db.QueryRow("SELECT VERSION()").Scan(&version); err != nil {
		t.Fatal(err)
	}
	t.Logf("MariaDB=%s scratch_schema=%s", version, name)
	plan := r.DatabasePlan{ReadDatabases: []string{name}, WriteDatabases: []string{name}}
	if err = m.EnsureMySQLSchema(context.Background(), db, m.SchemaOptions{AutoCreate: true, CreateDatabases: true, DatabasePlan: plan}); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, e := db.Exec("DROP DATABASE `" + name + "`"); e != nil {
			t.Error(e)
		}
	}()
	g, err := m.NewMySQLGroupFromDB(db, m.MySQLGroupOptions{DatabasePlan: plan})
	if err != nil {
		t.Fatal(err)
	}
	transactionCases(t, g)
	ctx := context.Background()
	if err := Prepare(ctx, g, "sql-extra", "miss", []string{"101"}, func(context.Context, string, r.QuestRecord) ([]Attempt, error) {
		return []Attempt{{QuestID: 84001, ItemID: 600001, Count: 0, MaxStack: 5}}, nil
	}); err != nil {
		t.Fatal(err)
	}
	recoverOne(t, g, "101")
	if held(t, g, "101") != 4 {
		t.Fatal("SQL miss granted an item")
	}
	if err := Prepare(ctx, g, "sql-extra", "full", []string{"101"}, planOne); err != nil {
		t.Fatal(err)
	}
	if v, err := Recover(ctx, g, "owner-101", "101", func(*r.InventoryRecord, Attempt) ([]int16, error) { return nil, ErrBagFull }); err != nil || !v.Pending {
		t.Fatalf("SQL full inbox: %v %+v", err, v)
	}
	if held(t, g, "101") != 4 {
		t.Fatal("SQL full bag changed inventory")
	}

	// A fresh pool/repository has no runtime caches or connection identity.
	peer, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal("open reload pool")
	}
	defer peer.Close()
	fresh, err := m.NewMySQLGroupFromDB(peer, m.MySQLGroupOptions{DatabasePlan: plan})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"101", "102", "103", "104"} {
		recoverOne(t, fresh, id)
		want := int64(4)
		if id == "101" {
			want = 5
		}
		if held(t, fresh, id) != want {
			t.Fatal("fresh-pool replay changed delivery")
		}
		var j journal
		if err := loadJSON(context.Background(), fresh.Settings, scope(id, "room-b"), &j); err != nil {
			t.Fatal(err)
		}
		if !j.Events["one-boss"].Done {
			t.Fatal("missing persistent role receipt")
		}
	}
}
