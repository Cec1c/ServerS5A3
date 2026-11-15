package mysql

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/repository"
)

func withinScopedMetadataAssets(ctx context.Context, u *mysqlAccountCharacterAssetUnitOfWork, mode string, apply func(repository.AccountRepository, repository.CharacterRepository) error) error {
	switch mode {
	case "ledger":
		return u.WithinAccountCharacterLedgerAssets(ctx, "acct-77", "77", func(a repository.AccountRepository, _ repository.AccountInventoryRepository, c repository.CharacterRepository, _ repository.InventoryRepository, _ repository.EquipmentRepository) error {
			return apply(a, c)
		})
	case "plain":
		return u.WithinAccountCharacterAssets(ctx, "acct-77", "77", func(_ repository.AccountInventoryRepository, c repository.CharacterRepository, _ repository.InventoryRepository, _ repository.EquipmentRepository) error {
			return apply(nil, c)
		})
	case "pet":
		return u.WithinAccountCharacterPetAssets(ctx, "acct-77", "77", func(_ repository.AccountInventoryRepository, c repository.CharacterRepository, _ repository.InventoryRepository, _ repository.EquipmentRepository, _ repository.PetRepository) error {
			return apply(nil, c)
		})
	default:
		return fmt.Errorf("unknown test route %q", mode)
	}
}

func applyScopedMetadataAssets(ctx context.Context, u *mysqlAccountCharacterAssetUnitOfWork, mode string, full, twice bool) error {
	return withinScopedMetadataAssets(ctx, u, mode, func(a repository.AccountRepository, c repository.CharacterRepository) error {
		if full {
			c.(*rentalScopedCharacterRepository).base.(*mysqlCharStore).trackStatChanges = false
			if a != nil {
				a.(*rentalScopedAccountRepository).base.(*mysqlAccountStore).trackMetadataChanges = false
			}
		}
		cr, found, err := c.Load(ctx, "77")
		if err != nil || !found {
			return fmt.Errorf("load character found=%t: %w", found, err)
		}
		var ar repository.AccountRecord
		if a != nil {
			ar, found, err = a.Load(ctx, "acct-77")
			if err != nil || !found {
				return fmt.Errorf("load account found=%t: %w", found, err)
			}
		}
		for step := 0; step < 1+btoiLedgerDelta(twice); step++ {
			cr.Stats["asset_action_count"] = int64(step + 1)
			cr.Stats["exp"] += 3
			delete(cr.Stats, "角色无关项0")
			if err := repository.SaveCharacterFields(ctx, c, cr, repository.CharacterFieldStats); err != nil {
				return err
			}
			if a != nil {
				ar.HonorExp += 2
				ar.Metadata["asset_action_receipt"] = fmt.Sprint(step + 1)
				delete(ar.Metadata, "adventure_group_runtime")
				if err := a.Save(ctx, ar); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func TestScopedAssetMetadataDeltaMatchesFullSave(t *testing.T) {
	for _, batch := range []string{"1", "200"} {
		t.Run("batch_"+batch, func(t *testing.T) {
			t.Setenv(currentMysqlInsertBatchEnv, batch)
			for _, mode := range []string{"ledger", "plain", "pet"} {
				for _, twice := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/twice_%t", mode, twice), func(t *testing.T) {
						var snapshots []map[string][]string
						var writes, parameters []int
						for _, full := range []bool{true, false} {
							s, _, router := newDeathDeltaSQL(t)
							seedDeathDeltaState(t, router)
							s.calls, s.commits = nil, 0
							if err := applyScopedMetadataAssets(context.Background(), &mysqlAccountCharacterAssetUnitOfWork{router: router}, mode, full, twice); err != nil {
								t.Fatal(err)
							}
							if s.commits != 1 {
								t.Fatalf("commits=%d", s.commits)
							}
							snapshots = append(snapshots, deathDeltaSnapshot(s))
							n, args := mailboxDeltaWriteStats(s.calls)
							writes, parameters = append(writes, n), append(parameters, args)
							if !full && !twice {
								for _, call := range mailboxDeltaWrites(s.calls) {
									for _, arg := range call.Args {
										if arg == "角色无关项79" || arg == "账号无关项159" {
											t.Fatal("rewrote unchanged metadata/stat", call.Query)
										}
									}
								}
							}
							if mode == "ledger" && (!strings.Contains(s.calls[0].Query, "accounts") || !strings.HasSuffix(s.calls[0].Query, " FOR UPDATE")) {
								t.Fatal("account-first lock order changed")
							}
						}
						if !reflect.DeepEqual(snapshots[0], snapshots[1]) || parameters[1] >= parameters[0] {
							t.Fatalf("full/delta state differs or writes not reduced: writes=%v parameters=%v", writes, parameters)
						}
						t.Logf("SQL writes %d -> %d, parameters %d -> %d", writes[0], writes[1], parameters[0], parameters[1])
					})
				}
			}
		})
	}
}

func TestScopedAssetMetadataDeltaRollbackEveryWrite(t *testing.T) {
	for _, mode := range []string{"ledger", "plain", "pet"} {
		t.Run(mode, func(t *testing.T) {
			writes := 0
			for fail := 0; fail <= writes; fail++ {
				s, _, router := newDeathDeltaSQL(t)
				seedDeathDeltaState(t, router)
				before := deathDeltaSnapshot(s)
				s.calls, s.writes, s.commits, s.failAt = nil, 0, 0, fail
				err := applyScopedMetadataAssets(context.Background(), &mysqlAccountCharacterAssetUnitOfWork{router: router}, mode, false, true)
				if fail == 0 {
					if err != nil || s.commits != 1 {
						t.Fatalf("success path err=%v commits=%d", err, s.commits)
					}
					writes = s.writes
					continue
				}
				if err == nil || s.commits != 0 || !reflect.DeepEqual(before, deathDeltaSnapshot(s)) {
					t.Fatalf("write %d did not roll back: %v", fail, err)
				}
			}
			t.Logf("all %d injected writes rolled back", writes)
		})
	}
}

func TestScopedAssetMetadataDeltaSnapshotAndScopeGuards(t *testing.T) {
	for _, mode := range []string{"ledger", "plain", "pet"} {
		for _, scenario := range []string{"foreign_character", "missing_snapshot"} {
			t.Run(mode+"/"+scenario, func(t *testing.T) {
				s, _, router := newDeathDeltaSQL(t)
				seedDeathDeltaState(t, router)
				ctx := context.Background()
				g := newMySQLGroupFromRouter(router, false)
				cr, found, err := g.Character.Load(ctx, "77")
				if err != nil || !found {
					t.Fatal("load fixture", err)
				}
				before := deathDeltaSnapshot(s)
				s.calls, s.commits = nil, 0
				err = withinScopedMetadataAssets(ctx, &mysqlAccountCharacterAssetUnitOfWork{router: router}, mode, func(_ repository.AccountRepository, c repository.CharacterRepository) error {
					if scenario == "foreign_character" {
						cr.CharacterID = "foreign"
					}
					return repository.SaveCharacterFields(ctx, c, cr, repository.CharacterFieldStats)
				})
				if scenario == "foreign_character" {
					if err == nil || s.commits != 0 || !reflect.DeepEqual(before, deathDeltaSnapshot(s)) {
						t.Fatal("foreign character accepted", err)
					}
				} else {
					if err != nil || s.commits != 1 || !reflect.DeepEqual(before, deathDeltaSnapshot(s)) {
						t.Fatal("missing before-image did not preserve full data", err)
					}
				}
			})
		}
	}
}
