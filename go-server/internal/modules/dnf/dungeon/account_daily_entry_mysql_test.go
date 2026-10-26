package dungeon

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	driver "github.com/go-sql-driver/mysql"
	r "longheng.io/server/internal/modules/dnf/repository"
	repomysql "longheng.io/server/internal/modules/dnf/repository/mysql"
)

func TestEventAdmissionMySQLAccountLimitTalismanRollbackAndReconnect(t *testing.T) {
	dsn := os.Getenv("DNFBRIDGE_PLAYER_TEST_DSN")
	if dsn == "" {
		t.Skip("disposable MySQL DSN not configured")
	}
	cfg, err := driver.ParseDSN(dsn)
	if err != nil {
		t.Fatal("invalid disposable DSN")
	}
	cfg.DBName = ""
	cfg.ParseTime = true
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal("open disposable database")
	}
	defer db.Close()
	ctx := context.Background()
	name := fmt.Sprintf("event_admission_test_%d", time.Now().UnixNano())
	plan := r.DatabasePlan{ReadDatabases: []string{name}, WriteDatabases: []string{name}}
	if err = repomysql.EnsureMySQLSchema(ctx, db, repomysql.SchemaOptions{AutoCreate: true, CreateDatabases: true, DatabasePlan: plan}); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := db.ExecContext(ctx, "DROP DATABASE `"+name+"`"); err != nil {
			t.Error(err)
		}
	}()
	group := func() r.Group {
		g, err := repomysql.NewMySQLGroupFromDB(db, repomysql.MySQLGroupOptions{DatabasePlan: plan})
		if err != nil {
			t.Fatal(err)
		}
		return g
	}
	g := group()
	fixture, _, cmd := forestEntryFixture(t)
	a, _, _ := fixture.Account.Load(ctx, "a")
	if err = g.Account.Save(ctx, a); err != nil {
		t.Fatal(err)
	}
	for i, id := range []string{"31", "32"} {
		c, _, _ := fixture.Character.Load(ctx, id)
		c.Slot = i
		c.Name = "Forest" + id
		if err = g.Character.Save(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	fault := errors.New("late transactional failure")
	broken := g
	broken.AccountAssets = forestFailureAssets{g.AccountAssets, fault}
	o, _ := NewOwner(broken)
	if _, err = o.ConsumeSilentForestEntry(ctx, cmd, 10); !errors.Is(err, fault) {
		t.Fatalf("late SQL callback: %v", err)
	}
	a, _, err = group().Account.Load(ctx, "a")
	if err != nil {
		t.Fatal(err)
	}
	if used, err := SilentForestEntryUsed(a, cmd.UpdatedAt); err != nil || used != 0 {
		t.Fatal("account rollback", used, err)
	}
	o, _ = NewOwner(g)
	for i := int64(0); i < 9; i++ {
		cmd.ExpectedSequence = i
		cmd.Nonce = i + 1
		if _, err = o.ConsumeSilentForestEntry(ctx, cmd, 10); err != nil {
			t.Fatal(err)
		}
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	for i, id := range []string{"31", "32"} {
		c := cmd
		c.CharacterID = id
		c.ExpectedSequence = 0
		if i == 0 {
			c.ExpectedSequence = 9
		}
		c.Nonce = 100 + int64(i)
		separate := group()
		go func() {
			<-start
			owner, _ := NewOwner(separate)
			_, err := owner.ConsumeSilentForestEntry(ctx, c, 10)
			results <- err
		}()
	}
	close(start)
	success, denied := 0, 0
	for i := 0; i < 2; i++ {
		err := <-results
		if err == nil {
			success++
		} else if errors.Is(err, ErrDailyEntryLimit) {
			denied++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || denied != 1 {
		t.Fatal("concurrent tenth entry", success, denied)
	}
	fresh := group()
	a, _, err = fresh.Account.Load(ctx, "a")
	if err != nil {
		t.Fatal(err)
	}
	if used, err := SilentForestEntryUsed(a, cmd.UpdatedAt); used != 10 || err != nil {
		t.Fatal("reconnected account counter", used, err)
	}
	for _, id := range []string{"31", "32"} {
		c, _, err := fresh.Character.Load(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if c.Stats[epicEntryNonceKey] >= 100 {
			replay := cmd
			replay.CharacterID = id
			replay.ExpectedSequence = c.Stats[EpicEntrySequenceKey] - 1
			replay.Nonce = c.Stats[epicEntryNonceKey]
			owner, _ := NewOwner(fresh)
			out, err := owner.ConsumeSilentForestEntry(ctx, replay, 10)
			if err != nil || !out.Replay {
				t.Fatal("durable tenth replay", err)
			}
		}
	}
	cmd.UpdatedAt = cmd.UpdatedAt.Add(time.Minute)
	cmd.Nonce = 200
	c, _, _ := fresh.Character.Load(ctx, "31")
	cmd.ExpectedSequence = c.Stats[EpicEntrySequenceKey]
	o, _ = NewOwner(fresh)
	if _, err = o.ConsumeSilentForestEntry(ctx, cmd, 10); err != nil {
		t.Fatal("service day reset", err)
	}
	// Real SQL character/inventory transaction: talisman, then invitations,
	// late validation failure, durable receipt and expiration all share owner.
	c, _, _ = fresh.Character.Load(ctx, "31")
	c.Level = 90
	c.Stats["fatigue"] = 156
	if err = fresh.Character.Save(ctx, c); err != nil {
		t.Fatal(err)
	}
	if err = fresh.Inventory.Save(ctx, r.InventoryRecord{CharacterID: "31", Slots: map[string]r.ItemStack{"0:10": {ItemID: 999, Count: 1}, "0:11": {ItemID: 3330, Count: 90}}}); err != nil {
		t.Fatal(err)
	}
	epic := EpicEntryCommand{CharacterID: "31", AccountID: "a", DungeonID: 6063, ExpectedSequence: c.Stats[EpicEntrySequenceKey], Nonce: 201, UpdatedAt: cmd.UpdatedAt, Alternatives: [][]EntryItemRequirement{{{ItemID: 999, Count: 1, ConsumeOnEntry: true}}, {{ItemID: 3330, Count: 30, ConsumeOnEntry: true}}}}
	calls := 0
	epic.Validate = func() error {
		calls++
		if calls == 2 {
			return fault
		}
		return nil
	}
	if _, err = o.ConsumeEpicEntry(ctx, epic); !errors.Is(err, fault) {
		t.Fatal("late epic validation", err)
	}
	inv, _, err := group().Inventory.Load(ctx, "31")
	if err != nil || inv.Slots["0:10"].Count != 1 || inv.Slots["0:11"].Count != 90 {
		t.Fatal("epic rollback", inv, err)
	}
	epic.Validate = nil
	for i := 0; i < 2; i++ {
		owner, _ := NewOwner(group())
		out, err := owner.ConsumeEpicEntry(ctx, epic)
		if err != nil {
			t.Fatal(err)
		}
		want := int64(999)
		if i == 1 {
			want = 3330
		}
		if len(out.Consumed) != 1 || out.Consumed[0].ItemID != want {
			t.Fatal("payment priority", out)
		}
		replayOwner, _ := NewOwner(group())
		replay, err := replayOwner.ConsumeEpicEntry(ctx, epic)
		if err != nil || !replay.Replay {
			t.Fatal("epic replay", err)
		}
		epic.ExpectedSequence++
		epic.Nonce++
	}
	inv, _, err = group().Inventory.Load(ctx, "31")
	if err != nil || inv.Slots["0:10"].Count != 0 || inv.Slots["0:11"].Count != 60 {
		t.Fatal("final charge", inv, err)
	}
}
