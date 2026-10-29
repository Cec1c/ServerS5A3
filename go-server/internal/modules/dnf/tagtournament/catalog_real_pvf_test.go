package tagtournament_test

import (
	"os"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/tagtournament"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestRealPVFTagTournamentResourcesPlansAndRewards(t *testing.T) {
	name := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if name == "" {
		t.Skip("未提供真实PVF，只读专项未执行")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: name})
	if err != nil {
		t.Fatal(err)
	}
	config, err := tagtournament.LoadConfig(archive)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := tagtournament.LoadCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	if config.EntryGold != 150000 || config.DailyRewardCount != 1 || config.DailyExtraRewardCount != 0 || config.NormalBuffStat != 4000 || config.HardBuffStat != 10000 || config.CharacterTagCooldownRaw != 5 {
		t.Fatalf("真实配置不符: %+v", config)
	}
	for mode, wantID := range map[tagtournament.Mode]uint32{tagtournament.Normal: 320, tagtournament.Hard: 336} {
		if mode == tagtournament.Hard {
			reward := catalog.WeeklyReward()
			if config.HardWeeklyRewardItemID != 10304868 || catalog.WeeklyChallengeTimeMS() != 240000 || reward.ID != 10304868 || reward.Name != "帝国竞技场伤害字体" || reward.UsablePeriodDays != 14 || reward.FontPeriodDays != 14 {
				t.Fatal("真实周挑战目标或字体配置未读出", config.HardWeeklyRewardItemID, reward)
			}
		}
		dungeon, ok := catalog.Dungeon(mode)
		basis := uint32(100)
		if mode == tagtournament.Hard {
			basis = 120
		}
		if !ok || dungeon.ID != wantID || dungeon.MinimumLevel != 90 || dungeon.BasisLevel != basis {
			t.Fatal(dungeon)
		}
	}
	for id, name := range map[uint32]string{10304532: "荣耀之德洛斯勋章", 10304533: "异面奖章"} {
		item, ok := catalog.Item(id)
		if !ok || item.Name != name {
			t.Fatalf("奖励%d: %+v", id, item)
		}
	}
	tools := catalog.Consumables()
	for i, seconds := range []time.Duration{20, 20, 180, 60, 180, 60} {
		if tools[i].Cooldown != seconds*time.Second {
			t.Fatal("专用道具冷却时间错误", tools[i])
		}
	}
	if tools[0].RecoverySection != "hp recovery" || tools[0].RecoveryPercent != 30 || tools[1].RecoverySection != "mp recovery" || tools[1].RecoveryPercent != 30 || tools[2].Duration != 2*time.Minute || tools[2].StatChanges[0].Value != -20 || tools[3].Duration != 30*time.Second || tools[3].SuperArmorHits != 5 || tools[4].Duration != 2*time.Minute || tools[5].Duration != 30*time.Second {
		t.Fatal("专用道具效果与真实PVF不符", tools)
	}
	tools[2].StatChanges[0].Value = 0
	if catalog.Consumables()[2].StatChanges[0].Value != -20 {
		t.Fatal("调用者改写了专用道具缓存")
	}
	for mode, want := range map[tagtournament.Mode]int{tagtournament.Normal: 5, tagtournament.Hard: 7} {
		plan, err := catalog.DailyPlan(time.Date(2026, 10, 18, 12, 0, 0, 0, time.FixedZone("北京", 8*3600)), mode)
		if err != nil || len(plan.Encounters) != want {
			t.Fatal(plan, err)
		}
		for _, encounter := range plan.Encounters {
			monster, ok := catalog.Monster(encounter.MonsterID)
			if !ok || monster.Name == "" {
				t.Fatal(encounter)
			}
			t.Logf("模式%d 地图%d 怪物%d %s 弱点%s 阶段倍率%.2f 可选%v", mode, encounter.MapID, encounter.MonsterID, monster.Name, encounter.Weakness, encounter.Balance, encounter.Optional)
		}
	}
	rows, err := catalog.HardRewardTotal(7)
	if err != nil || len(rows) != 2 || rows[0].ItemID != 10304532 || rows[0].Count != 27 || rows[1].ItemID != 10304533 || rows[1].Count != 30 {
		t.Fatal(rows, err)
	}
}
