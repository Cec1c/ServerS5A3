//go:build mysql_integration

package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"net"
	"os"
	"reflect"
	"testing"
	"time"

	driver "github.com/go-sql-driver/mysql"
	r "longheng.io/server/internal/modules/dnf/repository"
)

func TestPetTokenOrderDisposableMySQL(t *testing.T) {
	if os.Getenv("DNF_RUNTIME_DISPOSABLE_DB") != "1" {
		t.Fatal("requires disposable database")
	}
	cfg, err := driver.ParseDSN(os.Getenv("DNFBRIDGE_PLAYER_TEST_DSN"))
	if err != nil || cfg.Net != "tcp" || cfg.DBName != "" {
		t.Fatal("requires isolated DSN")
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
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	name := fmt.Sprintf("pet_order_%d", time.Now().UnixNano())
	defer db.Exec("DROP DATABASE IF EXISTS `" + name + "`")
	plan := r.DatabasePlan{ReadDatabases: []string{name}, WriteDatabases: []string{name}}
	if err := EnsureMySQLSchema(ctx, db, SchemaOptions{AutoCreate: true, CreateDatabases: true, DatabasePlan: plan}); err != nil {
		t.Fatal(err)
	}
	g, err := NewMySQLGroupFromDB(db, MySQLGroupOptions{DatabasePlan: plan})
	if err != nil {
		t.Fatal(err)
	}
	table := mysqlTable(name, "dnf", mysqlPetTokensTable)
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec("CREATE TABLE `" + name + "`.token_updates (ordinal_before INT,ordinal_after INT) ENGINE=InnoDB")
	exec("CREATE TRIGGER `" + name + "`.audit_token_updates AFTER UPDATE ON " + table + " FOR EACH ROW INSERT INTO `" + name + "`.token_updates VALUES (OLD.token_order,NEW.token_order)")
	seed := func() {
		t.Helper()
		entry := r.PetEntry{PetKey: "p", ItemID: 1, Satiety: 100, Extra: map[string]string{"keep": "yes"}, AppliedClearTokens: map[string]bool{}}
		for i := 0; i < 128; i++ {
			token := fmt.Sprintf("t%03d", i)
			entry.AppliedClearTokens[token] = true
			entry.AppliedClearTokenOrder = append(entry.AppliedClearTokenOrder, token)
		}
		if err := g.Pet.Save(ctx, r.PetRecord{CharacterID: "c", EquippedKey: "p", Entries: map[string]r.PetEntry{"p": entry, "other": {PetKey: "other", ItemID: 2, Extra: map[string]string{"other": "kept"}}}, Artifacts: map[string]r.ItemStack{"red": {ItemID: 3, Count: 1}}}); err != nil {
			t.Fatal(err)
		}
	}
	update := func(token string, fail bool) error {
		return g.CharacterPets.WithinCharacterPets(ctx, "c", func(_ r.InventoryRepository, _ r.EquipmentRepository, pets r.PetRepository) error {
			record, _, err := pets.Load(ctx, "c")
			if err != nil {
				return err
			}
			before := r.ClonePet(record).Entries["p"]
			next := record.Entries["p"]
			if !next.AppliedClearTokens[token] {
				delete(next.AppliedClearTokens, next.AppliedClearTokenOrder[0])
				next.AppliedClearTokenOrder = append(next.AppliedClearTokenOrder[1:], token)
				next.AppliedClearTokens[token] = true
				next.Satiety--
			}
			record.Entries["p"] = next
			record.UpdatedAt = time.Now().UTC()
			if err := r.SavePetGrowth(ctx, pets, record, "p", before); err != nil {
				return err
			}
			if fail {
				return errors.New("late isolated rollback")
			}
			return nil
		})
	}
	physical := func() map[string]int64 {
		t.Helper()
		rows, err := db.QueryContext(ctx, "SELECT token,token_order FROM "+table+" WHERE character_id='c' AND pet_key='p' ORDER BY token_order")
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		out := map[string]int64{}
		for rows.Next() {
			var token string
			var n int64
			if err := rows.Scan(&token, &n); err != nil {
				t.Fatal(err)
			}
			out[token] = n
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return out
	}
	for _, layout := range []string{"zero", "one", "sparse", "boundary"} {
		t.Run(layout, func(t *testing.T) {
			seed()
			switch layout {
			case "one":
				exec("UPDATE " + table + " SET token_order=token_order+1 WHERE character_id='c' AND pet_key='p' ORDER BY token_order DESC")
			case "sparse":
				exec("UPDATE " + table + " SET token_order=token_order*2+5 WHERE character_id='c' AND pet_key='p' ORDER BY token_order DESC")
			case "boundary":
				exec("UPDATE "+table+" SET token_order=token_order+? WHERE character_id='c' AND pet_key='p' ORDER BY token_order DESC", int64(math.MaxInt32)-127)
			}
			exec("DELETE FROM `" + name + "`.token_updates")
			before := physical()
			if err := update("t128", false); err != nil {
				t.Fatal(err)
			}
			after := physical()
			if len(after) != 128 {
				t.Fatal("window size", len(after))
			}
			if layout != "boundary" {
				for token, n := range before {
					if token != "t000" && after[token] != n {
						t.Fatal("surviving ordinal rewritten", token, n, after[token])
					}
				}
			} else if after["t001"] != 0 || after["t128"] != 127 {
				t.Fatal("overflow did not compact safely", after["t001"], after["t128"])
			}
			var updates int
			if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM `"+name+"`.token_updates").Scan(&updates); err != nil || updates != 0 {
				t.Fatal("unexpected token UPDATE rows", updates, err)
			}
			if err := update("t129", true); err == nil {
				t.Fatal("rollback not injected")
			}
			if !reflect.DeepEqual(physical(), after) {
				t.Fatal("rollback changed physical queue")
			}
			// Fail the append after eviction. Both the removed head and all growth
			// fields must roll back under the real transaction and unique index.
			exec("CREATE TRIGGER `" + name + "`.fail_append BEFORE INSERT ON " + table + " FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='isolated append failure'")
			if err := update("t129", false); err == nil {
				t.Fatal("append failure not injected")
			}
			exec("DROP TRIGGER `" + name + "`.fail_append")
			if !reflect.DeepEqual(physical(), after) {
				t.Fatal("failed append lost queue head")
			}
			if err := update("t128", false); err != nil {
				t.Fatal("replay", err)
			}
			if !reflect.DeepEqual(physical(), after) {
				t.Fatal("replay changed queue")
			}
			loaded, _, err := g.Pet.Load(ctx, "c")
			if err != nil {
				t.Fatal(err)
			}
			p := loaded.Entries["p"]
			if p.Satiety != 99 || p.AppliedClearTokenOrder[0] != "t001" || p.AppliedClearTokenOrder[127] != "t128" || p.Extra["keep"] != "yes" || loaded.Entries["other"].Extra["other"] != "kept" || loaded.Artifacts["red"].ItemID != 3 {
				t.Fatal("old reader/other pet contract changed")
			}
		})
	}
}
