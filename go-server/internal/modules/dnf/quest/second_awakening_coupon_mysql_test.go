package quest

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	driver "github.com/go-sql-driver/mysql"
	repo "longheng.io/server/internal/modules/dnf/repository"
	repomysql "longheng.io/server/internal/modules/dnf/repository/mysql"
)

func TestSecondAwakeningCouponMySQLAtomicReload(t *testing.T) {
	dsn := os.Getenv("DNFBRIDGE_PLAYER_TEST_DSN")
	if dsn == "" {
		t.Skip("disposable MySQL required")
	}
	cfg, e := driver.ParseDSN(dsn)
	if e != nil {
		t.Fatal("invalid test DSN")
	}
	// This destructive test creates/drops only a uniquely named scratch schema.
	// Explicitly refuse a network address that could select a production server.
	if cfg.Net != "tcp" || cfg.Addr != "127.0.0.1:34323" {
		t.Fatal("requires the local disposable MariaDB at 127.0.0.1:34323")
	}
	cfg.DBName = ""
	cfg.ParseTime = true
	db, e := sql.Open("mysql", cfg.FormatDSN())
	if e != nil {
		t.Fatal("open test pool")
	}
	defer db.Close()
	peer, e := sql.Open("mysql", cfg.FormatDSN())
	if e != nil {
		t.Fatal("open reload pool")
	}
	defer peer.Close()
	ctx := context.Background()
	name := fmt.Sprintf("second_coupon_test_%d", time.Now().UnixNano())
	plan := repo.DatabasePlan{ReadDatabases: []string{name}, WriteDatabases: []string{name}}
	if e = repomysql.EnsureMySQLSchema(ctx, db, repomysql.SchemaOptions{AutoCreate: true, CreateDatabases: true, DatabasePlan: plan}); e != nil {
		t.Fatal(e)
	}
	defer func() {
		if _, e := db.ExecContext(ctx, "DROP DATABASE `"+name+"`"); e != nil {
			t.Error(e)
		}
	}()
	group := func(pool *sql.DB) repo.Group {
		g, e := repomysql.NewMySQLGroupFromDB(pool, repomysql.MySQLGroupOptions{DatabasePlan: plan})
		if e != nil {
			t.Fatal(e)
		}
		return g
	}
	g := group(db)
	fresh := group(peer)
	seed, catalog, in := secondAwakeningCouponFixture(t)
	c, _, _ := seed.Character.Load(ctx, "77")
	sk, _, _ := seed.Skill.Load(ctx, "77")
	bag, _, _ := seed.Inventory.Load(ctx, "77")
	q, _, _ := seed.Quest.Load(ctx, "77")
	for _, e := range []error{g.Character.Save(ctx, c), g.Skill.Save(ctx, sk), g.Inventory.Save(ctx, bag), g.Quest.Save(ctx, q)} {
		if e != nil {
			t.Fatal(e)
		}
	}
	before := secondAwakeningSnapshot(t, fresh)
	// Inject failures after the earlier SQL writes in the same real transaction.
	for _, stage := range []string{"inventory_failure", "skill_failure", "quest_failure"} {
		t.Run(stage, func(t *testing.T) {
			f := g
			f.CharacterSettlement = couponFailUOW{g.CharacterSettlement, stage}
			o, _ := NewOwner(f)
			if _, e := o.UseClearCoupon(ctx, catalog, in); e == nil {
				t.Fatal("injected SQL save failure accepted")
			}
			if !reflect.DeepEqual(before, secondAwakeningSnapshot(t, fresh)) {
				t.Fatal("transaction did not roll back all state")
			}
		})
	}
	var wg sync.WaitGroup
	var applied atomic.Int32
	a, _ := NewOwner(g)
	b, _ := NewOwner(fresh)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		o := a
		if i%2 != 0 {
			o = b
		}
		go func(o *Owner) {
			defer wg.Done()
			r, e := o.UseClearCoupon(ctx, catalog, in)
			if e != nil {
				t.Error(e)
			}
			if r.Changed {
				applied.Add(1)
			}
		}(o)
	}
	wg.Wait()
	if applied.Load() != 1 {
		t.Fatalf("SQL commits=%d", applied.Load())
	}
	after := secondAwakeningSnapshot(t, group(peer))
	if after[0].(repo.CharacterRecord).Stats["grow_type"] != 0x21 || after[1].(repo.InventoryRecord).Slots["0:87"].Count != 1 || after[2].(repo.SkillRecord).Skills[4].Level != 1 || after[3].(repo.QuestRecord).States[6905].Status != "completed" {
		t.Fatal("reload lost committed state")
	}
	if after[2].(repo.SkillRecord).Points != before[2].(repo.SkillRecord).Points {
		t.Fatal("skill ledger changed")
	}
}
