package mysql

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/adventuregroup"
	"longheng.io/server/internal/modules/dnf/dungeon"
	"longheng.io/server/internal/modules/dnf/progression"
	"longheng.io/server/internal/modules/dnf/repository"
)

// 仅隐藏窄投影能力，完整读取及字段保存仍完全复用原路径。
type fullSkillLedgerView struct{ repository.SkillRepository }

func (v fullSkillLedgerView) SaveFields(ctx context.Context, r repository.SkillRecord, fields ...repository.SkillField) error {
	return repository.SaveSkillFields(ctx, v.SkillRepository, r, fields...)
}

type ledgerProgressionView struct {
	base repository.CharacterProgressionUnitOfWork
	full bool
}

func (u ledgerProgressionView) WithinCharacterProgression(ctx context.Context, id string, f func(repository.CharacterRepository, repository.SkillRepository) error) error {
	return u.base.WithinCharacterProgression(ctx, id, func(c repository.CharacterRepository, s repository.SkillRepository) error {
		if u.full {
			s = fullSkillLedgerView{s}
		}
		return f(c, s)
	})
}

type ledgerSettlementView struct {
	base repository.CharacterSettlementUnitOfWork
	full bool
}

func (u ledgerSettlementView) WithinCharacterSettlement(ctx context.Context, id string, f func(repository.Group) error) error {
	return u.base.WithinCharacterSettlement(ctx, id, func(g repository.Group) error {
		if u.full {
			g.Skill = fullSkillLedgerView{g.Skill}
		}
		return f(g)
	})
}

func seedPointLedgerSkills(t *testing.T, router mysqlRouter, level int) repository.SkillRecord {
	t.Helper()
	r := repository.SkillRecord{CharacterID: "77", Points: repository.SkillPointState{SyncedLevel: level, TotalSP: 100, RemainingSP: 80, TotalTP: 10, RemainingTP: 8},
		Skills: map[int64]repository.SkillState{40: {Level: 3, Enabled: true}}, Layouts: map[int]repository.SkillLayout{0: {3: 40}},
		Cooldowns: map[int64]time.Time{40: time.Unix(20000, 0).UTC()},
		Trees:     repository.SkillTreeState{Expanded: true, Active: 1, Other: repository.SkillProfile{Skills: map[int64]repository.SkillState{41: {Level: 2, Enabled: true}}, Points: repository.SkillPointState{SyncedLevel: level, TotalSP: 90, RemainingSP: 50, TotalTP: 10, RemainingTP: 5}}}}
	if err := newMySQLGroupFromRouter(router, false).Skill.Save(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	return r
}
func pointLedgerReader(router mysqlRouter) repository.Group {
	router.readDBs = router.writeDBs
	return newMySQLGroupFromRouter(router, false)
}
func pointLedgerStableRows(s *deathDeltaSQL) map[string][]string {
	copy := deathDeltaClone(s.rows)
	for _, rows := range copy {
		for _, row := range rows {
			if _, found := row["updated_at"]; found {
				row["updated_at"] = "时间戳另行验证"
			}
			if key, ok := row["stat_key"].(string); ok && strings.HasPrefix(key, repository.LevelUpEventStatPrefix) {
				row["stat_value"] = "升级事件时间戳另行验证"
			}
		}
	}
	return (&rowModel{tables: copy}).snapshot()
}

func TestMySQLSkillPointLedgerProgressionMatchesFullReadAndKeepsAllSkillData(t *testing.T) {
	for _, scenario := range []string{"同级经验", "89升90", "满级熟练度与胶囊", "历史无技能页", "技能记录缺失", "技能点过期", "技能页无效", "技能点写入失败"} {
		t.Run(scenario, func(t *testing.T) {
			var results []map[string][]string
			var reads []int
			for _, full := range []bool{true, false} {
				s, _, router := newDeathDeltaSQL(t)
				seedDeathDeltaState(t, router)
				level := 89
				if scenario == "满级熟练度与胶囊" {
					level = 90
				}
				seed := seedPointLedgerSkills(t, router, level)
				reader := pointLedgerReader(router)
				c, _, err := reader.Character.Load(context.Background(), "77")
				if err != nil {
					t.Fatal(err)
				}
				c.Level = level
				if err := reader.Character.Save(context.Background(), c); err != nil {
					t.Fatal(err)
				}
				a, _, err := reader.Account.Load(context.Background(), "acct-77")
				if err != nil {
					t.Fatal(err)
				}
				delete(a.Metadata, adventuregroup.RuntimeStateMetadataKey)
				if err := reader.Account.Save(context.Background(), a); err != nil {
					t.Fatal(err)
				}
				beforeSkill, _, err := reader.Skill.Load(context.Background(), "77")
				if err != nil {
					t.Fatal(err)
				}
				points := seed.Points
				plan := progression.CommitPlan{CharacterID: "77", AccountID: "acct-77", Expected: progression.CommitState{Level: level, Experience: 100, SkillPoints: points}, Next: progression.CommitState{Level: level, Experience: 101, SkillPoints: points}}
				if scenario == "89升90" || scenario == "技能点写入失败" {
					plan.Next.Level = 90
					plan.Next.SkillPoints.SyncedLevel = 90
					plan.Next.SkillPoints.TotalSP += 10
					plan.Next.SkillPoints.RemainingSP += 10
				}
				if scenario == "满级熟练度与胶囊" {
					plan.Next.Experience = 100
					plan.ExpectedCharacterStats = map[string]int64{"dungeon_capped_award_revision": 0}
					plan.NextCharacterStats = map[string]int64{"dungeon_capped_award_revision": 1}
				}
				if scenario == "技能点过期" {
					plan.Expected.SkillPoints.RemainingSP--
					plan.Next.SkillPoints.RemainingSP--
				}
				if scenario == "技能页无效" {
					table, _ := router.writeTable(mysqlSkillTreeTable, "77")
					for _, row := range s.rows[rowModelTableName(table)] {
						row["active_tree"] = int64(2)
					}
				}
				if scenario == "历史无技能页" || scenario == "技能记录缺失" {
					tableName := mysqlSkillTreeTable
					if scenario == "技能记录缺失" {
						tableName = mysqlSkillTable
					}
					table, err := router.writeTable(tableName, "77")
					if err != nil {
						t.Fatal(err)
					}
					s.rows[rowModelTableName(table)] = map[string]map[string]any{}
				}
				initial := deathDeltaSnapshot(s)
				s.calls = nil
				s.writes = 0
				s.commits = 0
				if scenario == "技能点写入失败" {
					s.failAt = 3
				}
				ctx, timing := repository.WithTransactionTiming(context.Background())
				if scenario == "满级熟练度与胶囊" {
					g := newMySQLGroupFromRouter(router, true)
					g.CharacterSettlement = ledgerSettlementView{g.CharacterSettlement, full}
					owner, err := dungeon.NewOwner(g)
					if err != nil {
						t.Fatal(err)
					}
					_, err = owner.CommitExperienceAward(ctx, dungeon.ExperienceAwardCommand{Plan: plan, MaximumLevelGains: []uint32{1}, AdventureRuntime: adventuregroup.RuntimeConfig{Capsule: adventuregroup.CapsuleConfig{MaximumExperience: 1000}}, UpdatedAt: time.Unix(10000, 0)})
					if err != nil {
						t.Fatal(err)
					}
				} else {
					_, err = progression.Commit(ctx, ledgerProgressionView{&mysqlCharacterProgressionUnitOfWork{router: router}, full}, plan)
				}
				failed := scenario == "技能记录缺失" || scenario == "技能点过期" || scenario == "技能页无效" || scenario == "技能点写入失败"
				if failed {
					if err == nil || s.commits != 0 || !reflect.DeepEqual(initial, deathDeltaSnapshot(s)) {
						t.Fatal("失败必须完整回滚", err, s.commits)
					}
				} else {
					if err != nil || s.commits != 1 {
						t.Fatal(err, s.commits)
					}
					after, _, err := reader.Skill.Load(context.Background(), "77")
					if err != nil {
						t.Fatal(err)
					}
					if after.Points != plan.Next.SkillPoints {
						t.Fatal("技能点与计划不符", after.Points, plan.Next.SkillPoints)
					}
					if scenario == "89升90" && (after.UpdatedAt.IsZero() || after.UpdatedAt.Equal(beforeSkill.UpdatedAt)) {
						t.Fatal("实际升级必须更新技能时间戳")
					}
					if plan.Expected.SkillPoints == plan.Next.SkillPoints && !after.UpdatedAt.Equal(beforeSkill.UpdatedAt) {
						t.Fatal("未变技能点仍被重复保存")
					}
					after.Points = beforeSkill.Points
					after.UpdatedAt = beforeSkill.UpdatedAt
					if scenario == "历史无技能页" {
						beforeSkill.Trees = repository.SkillTreeState{}
					}
					if !reflect.DeepEqual(after, beforeSkill) {
						t.Fatal("技能等级、双技能页、布局或冷却被改写", after, beforeSkill)
					}
				}
				reads = append(reads, timing.Snapshot().Calls[repository.TransactionLockRead])
				results = append(results, pointLedgerStableRows(s))
			}
			if !reflect.DeepEqual(results[0], results[1]) {
				t.Fatal("窄读与完整读取结果不等价")
			}
			if !strings.Contains(scenario, "过期") && !strings.Contains(scenario, "无效") && scenario != "技能记录缺失" {
				wantDifference := 4
				if scenario == "历史无技能页" {
					wantDifference = 3
				}
				if reads[0]-reads[1] != wantDifference {
					t.Fatal("无关技能明细读取减少数量不符", reads)
				}
			}
			t.Logf("%s：事务锁定读取%d→%d；角色、技能点及全部技能明细结果一致", scenario, reads[0], reads[1])
		})
	}
}

func TestMySQLSkillPointLedgerScopedOwnershipAndFullAccessRemain(t *testing.T) {
	for _, settlement := range []bool{false, true} {
		s, _, router := newDeathDeltaSQL(t)
		seedDeathDeltaState(t, router)
		seedPointLedgerSkills(t, router, 89)
		ctx := context.Background()
		check := func(skills repository.SkillRepository) error {
			points, ok := skills.(repository.SkillPointLedgerRepository)
			if !ok {
				return fmt.Errorf("没有窄读能力")
			}
			if _, _, err := points.LoadPointLedger(ctx, "other"); err == nil {
				t.Fatal("跨角色窄读未拒绝")
			}
			if err := points.SavePointLedger(ctx, repository.SkillPointLedger{CharacterID: "other"}); err == nil {
				t.Fatal("跨角色窄写未拒绝")
			}
			ledger, found, err := points.LoadPointLedger(ctx, "77")
			if err != nil || !found {
				t.Fatal(err)
			}
			full, found, err := skills.Load(ctx, "77")
			if err != nil || !found || len(full.Skills) != 1 || !full.Trees.Expanded || len(full.Trees.Other.Skills) != 1 || full.Points != ledger.Points {
				t.Fatal("原完整读取能力受影响", full, err)
			}
			full.Layouts[0][3] = 41
			return repository.SaveSkillFields(ctx, skills, full, repository.SkillFieldLayouts)
		}
		var err error
		if settlement {
			err = (&mysqlCharacterSettlementUnitOfWork{router: router}).WithinCharacterSettlement(ctx, "77", func(g repository.Group) error { return check(g.Skill) })
		} else {
			err = (&mysqlCharacterProgressionUnitOfWork{router: router}).WithinCharacterProgression(ctx, "77", func(_ repository.CharacterRepository, s repository.SkillRepository) error { return check(s) })
		}
		if err != nil {
			t.Fatal(err)
		}
		for _, call := range s.calls {
			if strings.HasPrefix(call.Query, "SELECT character_id, total_sp,") || strings.HasPrefix(call.Query, "SELECT expanded, active_tree,") {
				if !strings.HasSuffix(call.Query, " FOR UPDATE") {
					t.Fatal("技能点及页头必须保留原锁定读取", call.Query)
				}
			}
		}
	}
}
