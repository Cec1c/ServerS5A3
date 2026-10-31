package quest

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	driver "github.com/go-sql-driver/mysql"
	r "longheng.io/server/internal/modules/dnf/repository"
	repomysql "longheng.io/server/internal/modules/dnf/repository/mysql"
)

func TestCarnelianMySQLRewardAtomicReload(t *testing.T) {
	dsn := os.Getenv("DNFBRIDGE_PLAYER_TEST_DSN")
	if dsn == "" {
		t.Skip("disposable MySQL required")
	}
	cfg, err := driver.ParseDSN(dsn)
	if err != nil {
		t.Fatal("invalid test DSN")
	}
	if cfg.Net != "tcp" || cfg.Addr != "127.0.0.1:34323" {
		t.Fatal("requires disposable local MariaDB at 127.0.0.1:34323")
	}
	cfg.DBName = ""
	cfg.ParseTime = true
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal("open test pool")
	}
	defer db.Close()
	peer, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal("open reload pool")
	}
	defer peer.Close()
	ctx := context.Background()
	name := fmt.Sprintf("carnelian_test_%d", time.Now().UnixNano())
	plan := r.DatabasePlan{ReadDatabases: []string{name}, WriteDatabases: []string{name}}
	if err := repomysql.EnsureMySQLSchema(ctx, db, repomysql.SchemaOptions{AutoCreate: true, CreateDatabases: true, DatabasePlan: plan}); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := db.ExecContext(ctx, "DROP DATABASE `"+name+"`"); err != nil {
			t.Error(err)
		}
	}()
	group := func(pool *sql.DB) r.Group {
		g, err := repomysql.NewMySQLGroupFromDB(pool, repomysql.MySQLGroupOptions{DatabasePlan: plan})
		if err != nil {
			t.Fatal(err)
		}
		return g
	}
	g, fresh := group(db), group(peer)
	seed, c, e := carnelianLifecycleFixture(t)
	ch, _, _ := seed.Character.Load(ctx, "19")
	sk, _, _ := seed.Skill.Load(ctx, "19")
	bag, _, _ := seed.Inventory.Load(ctx, "19")
	q, _, _ := seed.Quest.Load(ctx, "19")
	eq, _, _ := seed.Equipment.Load(ctx, "19")
	for _, err := range []error{g.Character.Save(ctx, ch), g.Skill.Save(ctx, sk), g.Inventory.Save(ctx, bag), g.Quest.Save(ctx, q), g.Equipment.Save(ctx, eq)} {
		if err != nil {
			t.Fatal(err)
		}
	}
	day := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	for n := 0; n < 2; n++ {
		at := day.Add(time.Duration(n) * 24 * time.Hour)
		carnelianAcceptAndHunt(t, g, c, e, at)
		in := carnelianFinishInput(t, at)
		beforeQ := dailyLifecycleLoadQuest(t, ctx, fresh)
		beforeBag, _, _ := fresh.Inventory.Load(ctx, "19")
		for _, stage := range []string{"inventory_failure", "skill_failure", "quest_failure"} {
			f := g
			f.CharacterSettlement = couponFailUOW{g.CharacterSettlement, stage}
			o, _ := NewOwner(f)
			if _, err := o.ApplyFinishSettlement(ctx, c, in); err == nil {
				t.Fatalf("accepted injected SQL %s", stage)
			}
			afterBag, _, _ := fresh.Inventory.Load(ctx, "19")
			if !reflect.DeepEqual(beforeQ, dailyLifecycleLoadQuest(t, ctx, fresh)) || !reflect.DeepEqual(beforeBag, afterBag) {
				t.Fatalf("SQL %s did not roll back", stage)
			}
		}
		var wg sync.WaitGroup
		var commits atomic.Int32
		a, _ := NewOwner(g)
		b, _ := NewOwner(fresh)
		for i := 0; i < 2; i++ {
			wg.Add(1)
			o := a
			if i%2 != 0 {
				o = b
			}
			go func(o *Owner) {
				defer wg.Done()
				result, err := o.ApplyFinishSettlement(ctx, c, in)
				if err != nil {
					var conflict *driver.MySQLError
					if !errors.As(err, &conflict) || conflict.Number != 1020 {
						t.Error(err)
					}
					// MariaDB may abort the losing snapshot with ER_CHECKREAD.
					// The retry below must read the winner's durable receipt.
					return
				}
				if !result.Replayed {
					commits.Add(1)
				}
			}(o)
		}
		wg.Wait()
		if commits.Load() != 1 {
			t.Fatalf("fresh SQL reward commits=%d", commits.Load())
		}
		reloaded := group(peer)
		bag, _, err := reloaded.Inventory.Load(ctx, "19")
		if err != nil {
			t.Fatal(err)
		}
		if got := bag.Slots["0:65"]; got.ItemID != 10163959 || got.Count != int64((n+1)*15) {
			t.Fatalf("reloaded reward=%+v", got)
		}
		o, _ := NewOwner(reloaded)
		if got, err := o.ApplyFinishSettlement(ctx, c, in); err != nil || !got.Replayed {
			t.Fatalf("reopened replay=%t err=%v", got.Replayed, err)
		}
	}
}
