//go:build mysql_integration

package mysql

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"reflect"
	"sync"
	"testing"
	"time"

	driver "github.com/go-sql-driver/mysql"
	b "longheng.io/server/internal/modules/dnf/buffswap"
	r "longheng.io/server/internal/modules/dnf/repository"
)

// Exercise actual registration writers, not just a read callback on a snapshot.
func TestBuffRegistrationDisposableMySQL(t *testing.T) {
	if os.Getenv("DNF_RUNTIME_DISPOSABLE_DB") != "1" {
		t.Fatal("disposable DB required")
	}
	cfg, err := driver.ParseDSN(os.Getenv("DNFBRIDGE_PLAYER_TEST_DSN"))
	if err != nil || cfg.Net != "tcp" || cfg.DBName != "" {
		t.Fatal("isolated TCP DSN required")
	}
	host, _, err := net.SplitHostPort(cfg.Addr)
	if err != nil || host != "127.0.0.1" {
		t.Fatal("loopback required")
	}
	cfg.ParseTime, cfg.InterpolateParams = true, true
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(4)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	schema := fmt.Sprintf("buff_audit_%d", time.Now().UnixNano())
	defer db.Exec("DROP DATABASE IF EXISTS `" + schema + "`")
	plan := r.DatabasePlan{ReadDatabases: []string{schema}, WriteDatabases: []string{schema}}
	if err = EnsureMySQLSchema(ctx, db, SchemaOptions{AutoCreate: true, CreateDatabases: true, IncludeCSharpLegacySchema: true, DatabasePlan: plan}); err != nil {
		t.Fatal(err)
	}
	g, err := NewMySQLGroup(SQLDBAdapter{DB: db}, MySQLGroupOptions{DatabasePlan: plan})
	if err != nil {
		t.Fatal(err)
	}
	if err = g.Account.Save(ctx, r.AccountRecord{AccountID: "audit"}); err != nil {
		t.Fatal(err)
	}
	add := b.Command{Skill: 38, Slot: 12, List: 0, Index: 55}
	cases := []struct {
		name string
		read bool
		run  func(string) error
	}{
		{"historical_add", false, func(id string) error { return b.Apply(ctx, g, id, "audit", add) }},
		{"current_add", false, func(id string) error { return b.ApplyCurrent(ctx, g, id, "audit", add) }},
		{"group0_add", false, func(id string) error { return b.ApplyCurrentGroup(ctx, g, id, "audit", 0, add) }},
		{"group1_add", false, func(id string) error { return b.ApplyCurrentGroup(ctx, g, id, "audit", 1, add) }},
		{"worn_add", false, func(id string) error {
			return b.ApplyCurrent(ctx, g, id, "audit", b.Command{Skill: 38, Slot: 12, List: 3, Index: 12})
		}},
		{"costume_add", false, func(id string) error {
			return b.ApplyCurrent(ctx, g, id, "audit", b.Command{Skill: 38, Slot: 23, List: 1, Index: 5})
		}},
		{"pet_add", false, func(id string) error {
			return b.ApplyCurrent(ctx, g, id, "audit", b.Command{Skill: 38, Slot: 22, List: 7, Index: 5})
		}},
		{"remove", false, func(id string) error {
			return b.ApplyCurrentGroup(ctx, g, id, "audit", 0, b.Command{Skill: 38, Slot: 12, List: 44, Index: 65535})
		}},
		{"select_skill", false, func(id string) error {
			return b.ApplyCurrentGroup(ctx, g, id, "audit", 1, b.Command{Skill: 39, Slot: 33, List: 44, Index: 65535})
		}},
		{"disable_group", false, func(id string) error {
			return b.ApplyCurrentGroup(ctx, g, id, "audit", 0, b.Command{Skill: 0, Slot: 33, List: 44, Index: 65535})
		}},
		{"migrate_old_binding", false, func(id string) error {
			_, err := b.LoadResolvedOwnedWithOptions(ctx, g, id, "audit", b.OwnedProjectionOptions{MigrateCurrentInstances: true})
			return err
		}},
		{"existing_projection", true, func(id string) error {
			v, err := b.LoadResolvedOwnedWithOptions(ctx, g, id, "audit", b.OwnedProjectionOptions{})
			if err == nil && (!v.Found || v.State.Slots[12].ItemID != 101040493) {
				return fmt.Errorf("existing projection lost: %+v", v)
			}
			return err
		}},
	}
	for n, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id := fmt.Sprint(n + 1)
			if err := g.Character.Save(ctx, r.CharacterRecord{CharacterID: id, AccountID: "audit", Name: "audit" + id, Job: "0", Level: 90, Slot: n}); err != nil {
				t.Fatal(err)
			}
			inv := r.InventoryRecord{CharacterID: id, Slots: map[string]r.ItemStack{"0:54": {ItemID: 101040493, Count: 1}, "0:55": {ItemID: 101040494, Count: 1}, "1:5": {ItemID: 101040495, Count: 1}, "7:5": {ItemID: 101040496, Count: 1}}}
			eq := r.EquipmentRecord{CharacterID: id, Entries: map[string]r.EquipmentEntry{"12": {ItemID: 101040497, SlotIndex: 12}}}
			if err := g.Inventory.Save(ctx, inv); err != nil {
				t.Fatal(err)
			}
			if err := g.Equipment.Save(ctx, eq); err != nil {
				t.Fatal(err)
			}
			state := b.State{WireProfile: b.CurrentWireProfile, Skill: 38, Slots: map[byte]b.Binding{12: {List: 0, Index: 54, ItemID: 101040493}}, SelectedSkills: map[byte]uint16{0: 38}}
			raw, _ := json.Marshal(state)
			if err := g.Settings.Save(ctx, r.SettingsRecord{Scope: b.Scope(id), Values: map[string]string{"state": string(raw)}}); err != nil {
				t.Fatal(err)
			}
			beforeI, _, _ := g.Inventory.Load(ctx, id)
			beforeE, _, _ := g.Equipment.Load(ctx, id)
			beforeS, _, _ := g.Settings.Load(ctx, b.Scope(id))
			err := tc.run(id)
			afterI, _, ie := g.Inventory.Load(ctx, id)
			afterE, _, ee := g.Equipment.Load(ctx, id)
			afterS, _, se := g.Settings.Load(ctx, b.Scope(id))
			if ie != nil || ee != nil || se != nil {
				t.Fatal(ie, ee, se)
			}
			unchanged := reflect.DeepEqual(beforeI, afterI) && reflect.DeepEqual(beforeE, afterE) && reflect.DeepEqual(beforeS, afterS)
			if err != nil {
				t.Fatalf("operation rejected: %v; inventory/equipment/settings unchanged=%v", err, unchanged)
			}
			if tc.read && !unchanged {
				t.Fatal("read mutated persisted state")
			}
			if !tc.read && unchanged {
				t.Fatal("write did not persist")
			}
		})
	}
	t.Run("rollback_all_three_stores", func(t *testing.T) {
		beforeI, _, _ := g.Inventory.Load(ctx, "1")
		beforeE, _, _ := g.Equipment.Load(ctx, "1")
		beforeS, _, _ := g.Settings.Load(ctx, b.Scope("1"))
		stop := errors.New("late callback failure")
		err := r.WithinBuffOwnedSnapshot(ctx, g, "1", "audit", func(tx r.Group) error {
			i, _, e := tx.Inventory.Load(ctx, "1")
			if e != nil {
				return e
			}
			delete(i.Slots, "0:54")
			if e = tx.Inventory.Save(ctx, i); e != nil {
				return e
			}
			w, _, e := tx.Equipment.Load(ctx, "1")
			if e != nil {
				return e
			}
			delete(w.Entries, "12")
			if e = tx.Equipment.Save(ctx, w); e != nil {
				return e
			}
			s, _, e := tx.Settings.Load(ctx, b.Scope("1"))
			if e != nil {
				return e
			}
			s.Values["state"] = "rollback-marker"
			if e = tx.Settings.Save(ctx, s); e != nil {
				return e
			}
			return stop
		})
		afterI, _, _ := g.Inventory.Load(ctx, "1")
		afterE, _, _ := g.Equipment.Load(ctx, "1")
		afterS, _, _ := g.Settings.Load(ctx, b.Scope("1"))
		if !errors.Is(err, stop) || !reflect.DeepEqual(beforeI, afterI) || !reflect.DeepEqual(beforeE, afterE) || !reflect.DeepEqual(beforeS, afterS) {
			t.Fatal("partial writes escaped rollback", err)
		}
	})
	t.Run("read_transaction_rejects_write", func(t *testing.T) {
		before, _, _ := g.Settings.Load(ctx, b.Scope("1"))
		err := r.WithinBuffOwnedReadSnapshot(ctx, g, "1", "audit", func(tx r.Group) error {
			v, _, err := tx.Settings.Load(ctx, b.Scope("1"))
			if err != nil {
				return err
			}
			v.Values["state"] = "must-not-save"
			return tx.Settings.Save(ctx, v)
		})
		var sqlErr *driver.MySQLError
		after, _, _ := g.Settings.Load(ctx, b.Scope("1"))
		if !errors.As(err, &sqlErr) || sqlErr.Number != 1792 || !reflect.DeepEqual(before, after) {
			t.Fatal("read contract did not reject mutation", err)
		}
	})
	t.Run("foreign_owner_rejected", func(t *testing.T) {
		before, _, _ := g.Settings.Load(ctx, b.Scope("1"))
		if err := b.ApplyCurrent(ctx, g, "1", "foreign", add); err == nil {
			t.Fatal("foreign owner accepted")
		}
		after, _, _ := g.Settings.Load(ctx, b.Scope("1"))
		if !reflect.DeepEqual(before, after) {
			t.Fatal("foreign owner mutated settings")
		}
	})
	t.Run("concurrent_skill_registrations_are_not_lost", func(t *testing.T) {
		start := make(chan struct{})
		errs := make(chan error, 8)
		var wg sync.WaitGroup
		for n := 0; n < 8; n++ {
			wg.Add(1)
			go func(n int) {
				defer wg.Done()
				<-start
				errs <- b.ApplyCurrent(ctx, g, "1", "audit", b.Command{Skill: uint16(100 + n), Slot: 12, List: 0, Index: 55})
			}(n)
		}
		close(start)
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatal(err)
			}
		}
		state, found, err := b.Load(ctx, g, "1", "audit")
		if err != nil || !found {
			t.Fatal(err)
		}
		for n := 0; n < 8; n++ {
			if state.SkillSlots[uint16(100+n)][12].ItemID != 101040494 {
				t.Fatal("concurrent update lost", n, state)
			}
		}
	})
}
