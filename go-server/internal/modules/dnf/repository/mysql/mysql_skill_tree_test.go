package mysql

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	repo "longheng.io/server/internal/modules/dnf/repository"
)

func TestMySQLSkillTreeColdLoadAndScopedFieldSave(t *testing.T) {
	sqlDB := &fakeSQLDB{rows: []fakeSQLRow{
		{values: []any{"31", 100, 60, 10, 8, 10, nil}},
		{values: []any{true, 1, 100, 80, 10, 10, 10}},
	}, rowsets: []fakeSQLRows{
		{rows: []fakeSQLRow{{values: []any{int64(46), 3, true}}}},
		{rows: []fakeSQLRow{{values: []any{0, 4, uint16(46)}}, {values: []any{1, 5, uint16(46)}}}},
		{rows: []fakeSQLRow{{values: []any{int64(46), time.Unix(1234, 0)}}}},
		{rows: []fakeSQLRow{{values: []any{int64(46), 2, true}}}},
	}}
	repos := newTestMySQLGroup(t, sqlDB)
	loaded, found, err := repos.Skill.Load(context.Background(), "31")
	if err != nil || !found {
		t.Fatalf("冷加载: found=%t err=%v", found, err)
	}
	if !loaded.Trees.Expanded || loaded.Trees.Active != 1 || loaded.Skills[46].Level != 3 || loaded.Trees.Other.Skills[46].Level != 2 || loaded.Points.RemainingSP != 60 || loaded.Trees.Other.Points.RemainingSP != 80 || loaded.Layouts[0][4] != 46 || loaded.Layouts[1][5] != 46 || loaded.Cooldowns[46].Unix() != 1234 {
		t.Fatalf("独立页持久化映射: %+v", loaded)
	}
	cloned := repo.CloneSkill(loaded)
	cloned.Trees.Other.Skills[46] = repo.SkillState{Level: 9}
	if reflect.DeepEqual(cloned.Trees.Other.Skills, loaded.Trees.Other.Skills) {
		t.Fatal("仓储克隆共享了另一页")
	}
	sqlDB.execs = nil
	if err := repo.SaveSkillFields(context.Background(), repos.Skill, loaded, repo.SkillFieldTrees); err != nil {
		t.Fatal(err)
	}
	queries := strings.Join(execQueries(sqlDB), "\n")
	if !strings.Contains(queries, "dnf_skill_tree_profiles") || !strings.Contains(queries, "dnf_skill_tree_profile_states") || strings.Contains(queries, "dnf_skill_cooldowns") || strings.Contains(queries, "dnf_skill_states`") || strings.Contains(queries, "_json") {
		t.Fatalf("另页保存越过字段边界: %s", queries)
	}
}

func TestMySQLCeraSkillAssetsUsesOneTransactionAndRejectsCrossCharacter(t *testing.T) {
	for _, scenario := range []string{"success", "rollback", "cross_character"} {
		t.Run(scenario, func(t *testing.T) {
			state := &inventoryGroupSQLState{}
			db := sql.OpenDB(inventoryGroupConnector{state})
			defer db.Close()
			u := &mysqlCeraShopAssetUnitOfWork{router: mysqlRouter{db: SQLDBAdapter{DB: db}, readDBs: []string{"replica"}, writeDBs: []string{"write_a", "write_b"}, tablePrefix: "dnf", now: time.Now}}
			ctx := context.Background()
			err := u.WithinCeraShopSkillAssets(ctx, "account-1", "31", repo.CharacterContainerStateScope("31"), func(a repo.AccountRepository, c repo.CharacterRepository, _ repo.InventoryRepository, _ repo.EquipmentRepository, _ repo.SettingsRepository, s repo.SkillRepository) error {
				if scenario == "cross_character" {
					return s.Save(ctx, repo.SkillRecord{CharacterID: "32"})
				}
				if _, _, err := s.Load(ctx, "31"); err != nil {
					return err
				}
				if err := a.Save(ctx, repo.AccountRecord{AccountID: "account-1", Metadata: map[string]string{"account_cera": "100"}}); err != nil {
					return err
				}
				if err := repo.SaveCharacterFields(ctx, c, repo.CharacterRecord{CharacterID: "31", Stats: map[string]int64{"skill_tree_index": 0}}, repo.CharacterFieldStats); err != nil {
					return err
				}
				if err := s.Save(ctx, repo.SkillRecord{CharacterID: "31", Trees: repo.SkillTreeState{Expanded: true, Other: repo.SkillProfile{Skills: map[int64]repo.SkillState{46: {Level: 1, Enabled: true}}}}}); err != nil {
					return err
				}
				if scenario == "rollback" {
					return errors.New("技能保存后模拟订单失败")
				}
				return nil
			})
			if (err == nil) != (scenario == "success") {
				t.Fatalf("%s: %v", scenario, err)
			}
			if state.begins != 1 || (state.commits == 1) != (scenario == "success") || (state.rollbacks == 1) != (scenario != "success") {
				t.Fatalf("事务边界: %+v", state)
			}
			if scenario == "cross_character" && state.writes != 0 {
				t.Fatal("越权技能写到了SQL")
			}
			for _, q := range state.queries {
				if strings.Contains(q, "replica") || !strings.HasSuffix(q, " FOR UPDATE") {
					t.Fatalf("没有在写库事务锁定: %s", q)
				}
			}
		})
	}
}

func TestSkillTreeLockedMirrorKeepsSignedSentinelOnlyForItsOwnField(t *testing.T) {
	character := repo.CharacterRecord{Stats: map[string]int64{"skill_tree_index": -1, "stat_weight": -1}}
	if characterStatMirrorValue(character, "skill_tree_index", 0) != -1 || characterStatMirrorValue(character, "stat_weight", 0) != 0 {
		t.Fatal("技能页锁定位被夹取，或误改了其他无符号字段")
	}
	for _, column := range mysqlCharacterStatColumns {
		if column.key == "skill_tree_index" {
			if column.fallback != -1 || column.ddl != "SMALLINT NOT NULL DEFAULT -1" {
				t.Fatalf("技能页默认/有符号定义错误: %+v", column)
			}
			return
		}
	}
	t.Fatal("缺少技能页结构字段")
}
