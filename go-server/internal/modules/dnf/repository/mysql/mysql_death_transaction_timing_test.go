package mysql

import (
	"context"
	"errors"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/progression"
	"longheng.io/server/internal/modules/dnf/repository"
)

// 隐藏MySQL具体类型，以原完整Load路径作同事务对照。
type fullOwnershipCharacterReader struct{ repository.CharacterRepository }

func TestMySQLSettlementOwnershipNarrowReadMatchesFullReads(t *testing.T) {
	var readCounts []int
	for _, full := range []bool{true, false} {
		s, _, router := newDeathDeltaSQL(t)
		seedDeathDeltaState(t, router)
		s.calls = nil
		err := (&mysqlCharacterSettlementUnitOfWork{router: router}).WithinCharacterSettlement(context.Background(), "77", func(tx repository.Group) error {
			accounts := tx.Account.(*characterSettlementAccountRepository)
			if full {
				accounts.characters = fullOwnershipCharacterReader{accounts.characters}
			}
			for i := 0; i < 2; i++ {
				if owner, err := accounts.accountID(context.Background()); err != nil || owner != "acct-77" {
					t.Fatal(owner, err)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		readCounts = append(readCounts, len(s.calls))
	}
	if readCounts[0] <= readCounts[1] || readCounts[1] != 2 {
		t.Fatal("归属校验读取未减少", readCounts)
	}
	t.Logf("两次实时归属校验SQL读取：%d→%d，角色行锁和返回账号一致", readCounts[0], readCounts[1])
}

func TestMySQLSettlementAccountOwnershipReadsOnlyLockedOwner(t *testing.T) {
	for _, changedOwner := range []bool{false, true} {
		s, _, router := newDeathDeltaSQL(t)
		seedDeathDeltaState(t, router)
		s.calls = nil
		ctx, timing := repository.WithTransactionTiming(context.Background())
		err := (&mysqlCharacterSettlementUnitOfWork{router: router}).WithinCharacterSettlement(ctx, "77", func(tx repository.Group) error {
			account, found, err := tx.Account.Load(ctx, "acct-77")
			if err != nil || !found {
				t.Fatal("账号读取失败", err)
			}
			if changedOwner {
				// 独立关系夹具按SQL UPDATE模拟本事务内归属改变。
				store := tx.Character.(*characterSettlementScopedCharacterRepository).base.(*mysqlCharStore)
				table, err := store.router.writeTable(mysqlCharTable, "77")
				if err != nil {
					return err
				}
				if _, err := store.router.db.ExecContext(ctx, "UPDATE "+table+" SET account_id = ? WHERE character_id = ?", "other", "77"); err != nil {
					return err
				}
			}
			account.HonorExp++
			return tx.Account.Save(ctx, account)
		})
		v := timing.Snapshot()
		if changedOwner {
			if err == nil || v.Calls[repository.TransactionCommit] != 0 || v.Calls[repository.TransactionRollback] != 1 {
				t.Fatal("保存时必须重新核对实时归属并回滚", err, v)
			}
			continue
		}
		if err != nil || v.Calls[repository.TransactionBegin] != 1 || v.Calls[repository.TransactionCommit] != 1 {
			t.Fatal(err, v)
		}
		var narrow, full int
		for _, call := range s.calls {
			if strings.HasPrefix(call.Query, "SELECT account_id FROM ") {
				narrow++
				if !strings.HasSuffix(call.Query, " FOR UPDATE") {
					t.Fatal("归属读取必须保留原角色行锁")
				}
			}
			if strings.HasPrefix(call.Query, "SELECT character_id, account_id,") {
				full++
			}
		}
		if narrow != 2 || full != 0 {
			t.Fatal("归属校验不应重复读取完整角色", narrow, full)
		}
		if v.Calls[repository.TransactionLockRead]+v.Calls[repository.TransactionRead]+v.Calls[repository.TransactionWrite] != len(s.calls) {
			t.Fatal("SQL调用计数不一致", v, len(s.calls))
		}
	}
}

func TestMySQLProgressionTimingIncludesRollbackWithoutUnchangedSkillWrites(t *testing.T) {
	for _, failWrite := range []bool{false, true} {
		s, _, router := newDeathDeltaSQL(t)
		seedDeathDeltaState(t, router)
		g := newMySQLGroupFromRouter(router, false)
		points := repository.SkillPointState{SyncedLevel: 89, TotalSP: 100, RemainingSP: 80}
		if err := g.Skill.Save(context.Background(), repository.SkillRecord{CharacterID: "77", Points: points}); err != nil {
			t.Fatal(err)
		}
		s.calls, s.writes = nil, 0
		if failWrite {
			s.failAt = 1
		}
		ctx, timing := repository.WithTransactionTiming(context.Background())
		_, err := progression.Commit(ctx, &mysqlCharacterProgressionUnitOfWork{router: router}, progression.CommitPlan{
			CharacterID: "77", AccountID: "acct-77",
			Expected: progression.CommitState{Level: 89, Experience: 100, SkillPoints: points},
			Next:     progression.CommitState{Level: 89, Experience: 101, SkillPoints: points},
		})
		v := timing.Snapshot()
		if failWrite {
			if err == nil || v.Calls[repository.TransactionCommit] != 0 || v.Calls[repository.TransactionRollback] != 1 {
				t.Fatal("失败路径计时或回滚不符", err, v)
			}
		} else {
			if err != nil || v.Calls[repository.TransactionCommit] != 1 {
				t.Fatal(err, v)
			}
			if v.Calls[repository.TransactionWrite] != 1 {
				t.Fatal("同级经验应仅更新角色主行，不重写80条未变化属性或技能点", v)
			}
			for _, call := range s.calls {
				if strings.HasPrefix(call.Query, "INSERT INTO ") && strings.Contains(call.Query, "dnf_skills`") {
					t.Fatal("同级经验不应写技能点", call.Query)
				}
			}
		}
		var totalMeasured int64
		for _, duration := range v.Durations {
			totalMeasured += int64(duration)
		}
		if totalMeasured > int64(v.Total) || v.Calls[repository.TransactionLockRead] == 0 {
			t.Fatal("计时重复归类或遗漏锁读", v)
		}
	}
}

func TestMySQLPetTransactionTimingKeepsCommitAndErrorSemantics(t *testing.T) {
	for _, fail := range []bool{false, true} {
		s, _, router := newDeathDeltaSQL(t)
		seedDeathDeltaPet(t, router)
		ctx, timing := repository.WithTransactionTiming(context.Background())
		injected := errors.New("回调失败")
		err := (&mysqlCharacterPetUnitOfWork{router: router}).WithinCharacterPets(ctx, "77", func(_ repository.InventoryRepository, _ repository.EquipmentRepository, pets repository.PetRepository) error {
			pet, found, err := pets.Load(ctx, "77")
			if err != nil || !found {
				t.Fatal(err)
			}
			if fail {
				return injected
			}
			entry := pet.Entries["37"]
			entry.Satiety--
			pet.Entries["37"] = entry
			return repository.SavePetFields(ctx, pets, pet, repository.PetFieldEntries)
		})
		v := timing.Snapshot()
		if fail {
			if !errors.Is(err, injected) || v.Calls[repository.TransactionRollback] != 1 || v.Calls[repository.TransactionCommit] != 0 {
				t.Fatal(err, v)
			}
		} else if err != nil || v.Calls[repository.TransactionCommit] != 1 || v.Calls[repository.TransactionWrite] != 2 {
			t.Fatal(err, v, s.writes)
		}
	}
}
