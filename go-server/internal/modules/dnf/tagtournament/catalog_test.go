package tagtournament

import (
	"fmt"
	"strings"
	"testing"
)

type testSource map[string]string

func (s testSource) ReadText(name string) (string, error) {
	text, ok := s[name]
	if !ok {
		return "", fmt.Errorf("测试资源缺失: %s", name)
	}
	return text, nil
}

func catalogSource(t *testing.T) testSource {
	t.Helper()
	c := candidateCatalog(t).config
	s := testSource{ConfigPath: testConfig}
	add := func(folder string, id uint32, body string) {
		file := fmt.Sprintf("%d.pvf", id)
		s[folder+"/"+folder+".lst"] += fmt.Sprintf("%d `%s`\n", id, file)
		s[folder+"/"+file] = "[name]\n`测试资源`\n" + body
	}
	for i, id := range c.DungeonIDs {
		add("dungeon", id, fmt.Sprintf("[minimum required level]\n90\n[basis level]\n%d\n[tag tournament dungeon]\n%d\n[disable exp]\n[disable drop item]\n[no fatigue]\n", 100+i*20, i+1))
	}
	for _, pool := range []MapPool{c.NormalPool, c.SpecialPool, c.HardPool} {
		for _, row := range pool.Rows {
			add("map", row.MapID, fmt.Sprintf("[monster]\n%d 1 0 100 100 0 1 1 `[fixed]` `[normal]`\n[/monster]\n", row.MonsterID))
			add("monster", row.MonsterID, "")
		}
	}
	for _, id := range []uint32{100, 101, 102} {
		add("stackable", id, "")
	}
	s["stackable/102.pvf"] += "[attach type] `[account]`\n[usable period] 14\n[action type] `[add damage font skin]`\n[/action type]\n[damage font info]\n[index] 10\n[expiration info] `[period]` 14\n[/damage font info]\n"
	for _, id := range c.Consumables {
		add("stackable", id, "[cool time]\n20000\n[stackable type]\n`[unlimited waste]` 1\n[hp recovery]\n`%` 30 1000 `myself`\n")
	}
	return s
}

func TestCatalogRejectsMissingReferencesAndMismatchedMapBoss(t *testing.T) {
	valid := catalogSource(t)
	if _, err := LoadCatalog(valid); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(testSource){
		"缺少索引": func(s testSource) { delete(s, "monster/monster.lst") },
		"缺少地图": func(s testSource) { delete(s, "map/11.pvf") },
		"缺少奖励": func(s testSource) { delete(s, "stackable/100.pvf") },
		"索引重复": func(s testSource) { s["monster/monster.lst"] += "21 `21.pvf`\n" },
		"跨目录引用": func(s testSource) {
			s["monster/monster.lst"] = strings.Replace(s["monster/monster.lst"], "`21.pvf`", "`../stackable/100.pvf`", 1)
		},
		"地图错怪": func(s testSource) { s["map/11.pvf"] = strings.Replace(s["map/11.pvf"], "21 1 0", "999 1 0", 1) },
		"副本错模式": func(s testSource) {
			s["dungeon/301.pvf"] = strings.Replace(s["dungeon/301.pvf"], "dungeon]\n2", "dungeon]\n1", 1)
		},
		"恢复超量": func(s testSource) {
			s["stackable/200.pvf"] = strings.Replace(s["stackable/200.pvf"], "30 1000", "101 1000", 1)
		},
		"冷却负数": func(s testSource) { s["stackable/200.pvf"] = strings.Replace(s["stackable/200.pvf"], "20000", "-1", 1) },
	} {
		t.Run(name, func(t *testing.T) {
			source := catalogSource(t)
			change(source)
			catalog, err := LoadCatalog(source)
			if err == nil || catalog != nil {
				t.Fatal("错误资源生成了部分可用目录", err)
			}
		})
	}
}

func TestCatalogWeeklyRewardRequiresFontOnlyWhenConfigured(t *testing.T) {
	for _, removeRankingSections := range []bool{false, true} {
		t.Run(fmt.Sprintf("移除排行配置%t", removeRankingSections), func(t *testing.T) {
			source := catalogSource(t)
			delete(source, "stackable/102.pvf")
			source["stackable/stackable.lst"] = strings.Replace(source["stackable/stackable.lst"], "102 `102.pvf`\n", "", 1)
			if removeRankingSections {
				for _, section := range []string{
					"[tag tournament hard mode time attack standard rank]\n20000 240000\n",
					"[tag tournament hard mode time attack reward]\n102 1668 1669 15\n",
					"[tag tournament hard mode min clear time]\n30000\n",
				} {
					source[ConfigPath] = strings.Replace(source[ConfigPath], section, "", 1)
				}
			}
			catalog, err := LoadCatalog(source)
			if !removeRankingSections {
				if err == nil {
					t.Fatal("配置周挑战奖励却遗漏字体未拒绝")
				}
				return
			}
			if err != nil {
				t.Fatal("取消排行后仍依赖排行配置或字体", err)
			}
			rewards, err := catalog.HardRewardTotal(7)
			if err != nil || len(rewards) != 2 || rewards[0].Count != 27 || rewards[1].Count != 30 {
				t.Fatal("取消排行改变了困难七关通关奖励", rewards, err)
			}
		})
	}
}
