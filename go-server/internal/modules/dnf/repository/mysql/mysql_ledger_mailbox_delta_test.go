package mysql

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/repository"
)

func seedLedgerMailboxDelta(t *testing.T, router mysqlRouter) {
	t.Helper()
	seedDeathDeltaState(t, router)
	g := newMySQLGroupFromRouter(router, false)
	i := repository.InventoryRecord{CharacterID: "77", Slots: map[string]repository.ItemStack{}, Warehouse: map[string]repository.ItemStack{}}
	for n := 0; n < 80; n++ {
		i.Slots[bagMoveSlotKey(0, int16(n))] = bagMoveStackable(int64(500000+n), 5)
	}
	i.Warehouse["2:0"] = bagMoveStackable(900000, 3)
	if err := g.Inventory.Save(context.Background(), i); err != nil {
		t.Fatal(err)
	}
}

func applyLedgerMailboxDelta(ctx context.Context, router mysqlRouter, full, twice bool) error {
	return (&mysqlAccountCharacterAssetUnitOfWork{router: router}).WithinAccountCharacterLedgerMailboxAssets(ctx, "acct-77", "77", func(a repository.AccountRepository, _ repository.AccountInventoryRepository, c repository.CharacterRepository, i repository.InventoryRepository, _ repository.EquipmentRepository, _ repository.MailboxRepository) error {
		if full {
			a.(*rentalScopedAccountRepository).base.(*mysqlAccountStore).trackMetadataChanges = false
			c.(*rentalScopedCharacterRepository).base.(*mysqlCharStore).trackStatChanges = false
			i.(*rentalScopedInventoryRepository).base.(*mysqlBagStore).trackSlotChanges = false
		}
		ar, found, err := a.Load(ctx, "acct-77")
		if err != nil || !found {
			return fmt.Errorf("load account found=%t: %w", found, err)
		}
		cr, found, err := c.Load(ctx, "77")
		if err != nil || !found {
			return fmt.Errorf("load character found=%t: %w", found, err)
		}
		ir, found, err := i.Load(ctx, "77")
		if err != nil || !found {
			return fmt.Errorf("load inventory found=%t: %w", found, err)
		}
		for step := 0; step < 1+btoiLedgerDelta(twice); step++ {
			ar.Metadata["seria_luck"] = fmt.Sprint(step + 1)
			cr.Stats["private_activity_counter"] = int64(step + 1)
			stack := ir.Slots["0:1"]
			stack.Count--
			ir.Slots["0:1"] = stack
			if err := repository.SaveInventoryFields(ctx, i, ir, repository.InventoryFieldSlots); err != nil {
				return err
			}
			if err := repository.SaveCharacterFields(ctx, c, cr, repository.CharacterFieldStats); err != nil {
				return err
			}
			if err := a.Save(ctx, ar); err != nil {
				return err
			}
		}
		return nil
	})
}

func btoiLedgerDelta(v bool) int {
	if v {
		return 1
	}
	return 0
}

func TestLedgerMailboxDeltaMatchesFullSave(t *testing.T) {
	for _, batch := range []string{"1", "200"} {
		t.Run("batch_"+batch, func(t *testing.T) {
			t.Setenv(currentMysqlInsertBatchEnv, batch)
			for _, twice := range []bool{false, true} {
				t.Run(fmt.Sprintf("twice_%t", twice), func(t *testing.T) {
					var snapshots []map[string][]string
					var statements, parameters []int
					for _, full := range []bool{true, false} {
						s, _, router := newDeathDeltaSQL(t)
						seedLedgerMailboxDelta(t, router)
						s.calls = nil
						s.commits = 0
						if err := applyLedgerMailboxDelta(context.Background(), router, full, twice); err != nil {
							t.Fatal(err)
						}
						if s.commits != 1 {
							t.Fatalf("commits=%d", s.commits)
						}
						snapshots = append(snapshots, deathDeltaSnapshot(s))
						n, args := mailboxDeltaWriteStats(s.calls)
						statements = append(statements, n)
						parameters = append(parameters, args)
						if !full && !twice {
							for _, call := range mailboxDeltaWrites(s.calls) {
								for _, arg := range call.Args {
									if arg == "0:79" || arg == "账号无关项159" || arg == "角色无关项79" {
										t.Fatalf("rewrote unrelated row: %s", call.Query)
									}
								}
							}
						}
					}
					if !reflect.DeepEqual(snapshots[0], snapshots[1]) {
						t.Fatal("delta and full transaction persisted different rows")
					}
					if statements[1] >= statements[0] || parameters[1] >= parameters[0] || !twice && parameters[1]*4 >= parameters[0] {
						t.Fatalf("no write reduction: statements=%v parameters=%v", statements, parameters)
					}
					t.Logf("SQL statements %d -> %d, bound parameters %d -> %d", statements[0], statements[1], parameters[0], parameters[1])
				})
			}
		})
	}
}

func TestLedgerMailboxDeltaRollbackEveryWrite(t *testing.T) {
	writes := 0
	for fail := 0; fail <= writes; fail++ {
		s, _, router := newDeathDeltaSQL(t)
		seedLedgerMailboxDelta(t, router)
		before := deathDeltaSnapshot(s)
		s.calls = nil
		s.writes, s.commits, s.failAt = 0, 0, fail
		err := applyLedgerMailboxDelta(context.Background(), router, false, true)
		if fail == 0 {
			if err != nil || s.commits != 1 {
				t.Fatalf("successful transaction: %v", err)
			}
			writes = s.writes
			continue
		}
		if err == nil || s.commits != 0 || !reflect.DeepEqual(before, deathDeltaSnapshot(s)) {
			t.Fatalf("write %d did not roll back all assets: %v", fail, err)
		}
	}
	t.Logf("rolled back all %d injected write failures", writes)
}

func TestLedgerMailboxDeltaAccountLockAndOwnership(t *testing.T) {
	s, _, router := newDeathDeltaSQL(t)
	seedLedgerMailboxDelta(t, router)
	s.calls = nil
	s.commits = 0
	err := (&mysqlAccountCharacterAssetUnitOfWork{router: router}).WithinAccountCharacterLedgerMailboxAssets(context.Background(), "acct-77", "77", func(a repository.AccountRepository, _ repository.AccountInventoryRepository, _ repository.CharacterRepository, _ repository.InventoryRepository, _ repository.EquipmentRepository, _ repository.MailboxRepository) error {
		_, _, err := a.Load(context.Background(), "foreign")
		return err
	})
	if err == nil {
		t.Fatal("foreign account accepted")
	}
	if s.commits != 0 || len(s.calls) == 0 || !strings.Contains(s.calls[0].Query, "accounts") || !strings.HasSuffix(s.calls[0].Query, " FOR UPDATE") {
		t.Fatal("account-first locked transaction changed")
	}
}
