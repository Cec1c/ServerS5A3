package tagtournament

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	repo "longheng.io/server/internal/modules/dnf/repository"
)

var beijing = time.FixedZone("北京时间", 8*60*60)

// 合成配置故意使用不同编号和费用，检验规则确实由PVF输入驱动。
const testConfig = `
[daily default reward count]
1
[daily default extra reward count]
0
[tag tournament reward]
0 100 2 50 0 100 3 50 1 101 3 100
[/tag tournament reward]
[tag tournament special reward]
0 100 5 100 1 101 3 100
[/tag tournament special reward]
[tag tournament return village time]
5 30 10
[tag tournament hard reward]
0 100 3 0 101 3 1 100 6 1 101 6 2 100 9 2 101 10 3 100 13 3 101 14 4 100 17 4 101 19 5 100 21 5 101 24 6 100 27 6 101 30
[/tag tournament reward hard]
[tag tournament random map]
4 11 21 12 22 13 23 14 24 15 25
[/tag tournament random map]
[tag tournament special map]
1 16 26 17 27
[/tag tournament special map]
[tag tournament hard map]
7 31 41 32 42 33 43 34 44 35 45 36 46 37 47
[/tag tournament hard map]
[tag tournament stackable]
200 201 202 203 204 205
[/tag tournament stackable]
[tag tournament enter gold]
120000
[tag tournament buff add stat]
4000
[tag tournament hard mode buff add state]
10000
[tag tournament hard mode balance]
7 1.00 1.10 1.30 1.70 2.10 2.50 3.10
[/tag tournament hard mode balance]
[tag tournament hard mode time attack standard rank]
20000 240000
[tag tournament hard mode time attack reward]
102 1668 1669 15
[tag tournament hard mode min clear time]
30000
[tag tournament dungeon list]
300 301
[/tag tournament dungeon list]
[tag tournament dungeon charac tag cool time]
5
`

func candidateCatalog(t *testing.T) *Catalog {
	t.Helper()
	config, err := ParseConfig(testConfig)
	if err != nil {
		t.Fatal(err)
	}
	return &Catalog{config: config, dungeons: [2]Dungeon{
		{Resource: Resource{ID: 300}, MinimumLevel: 90, BasisLevel: 100},
		{Resource: Resource{ID: 301}, MinimumLevel: 90, BasisLevel: 120},
	}}
}

func TestConfigRejectsCorruptRewardAndMapBoundaries(t *testing.T) {
	for name, text := range map[string]string{
		"缺少配置":   "",
		"重复标量":   testConfig + "\n[tag tournament enter gold]\n1\n",
		"溢出":     strings.Replace(testConfig, "120000", "4294967296", 1),
		"负数":     strings.Replace(testConfig, "120000", "-1", 1),
		"字段类型":   strings.Replace(testConfig, "120000", "120000.5", 1),
		"奖励截断":   strings.Replace(testConfig, "0 100 2 50", "0 100 2", 1),
		"奖励权重":   strings.Replace(testConfig, "0 100 2 50", "0 100 2 49", 1),
		"奖励数量":   strings.Replace(testConfig, "0 100 2 50", "0 100 65536 50", 1),
		"重复困难奖励": strings.Replace(testConfig, "0 101 3", "0 100 3", 1),
		"缺少困难阶段": strings.Replace(testConfig, "6 100 27 6 101 30", "", 1),
		"地图重复":   strings.Replace(testConfig, "12 22", "11 22", 1),
		"怪物重复":   strings.Replace(testConfig, "12 22", "12 21", 1),
		"抽取越界":   strings.Replace(testConfig, "4 11 21", "6 11 21", 1),
		"困难倍率错位": strings.Replace(testConfig, "7 1.00", "6 1.00", 1),
		"缺少闭合":   strings.Replace(testConfig, "[/tag tournament random map]", "", 1),
		"错误困难闭合": strings.Replace(testConfig, "[/tag tournament reward hard]", "[/tag tournament other]", 1),
	} {
		t.Run(name, func(t *testing.T) {
			config, err := ParseConfig(text)
			if err == nil || !reflect.DeepEqual(config, Config{}) {
				t.Fatalf("损坏配置被接纳: %+v / %v", config, err)
			}
		})
	}
	if _, err := ParseConfig(strings.Replace(testConfig, "[/tag tournament reward hard]", "[/tag tournament hard reward]", 1)); err != nil {
		t.Fatal(err)
	}
}

func TestDailyPlansAreGlobalStableAndResetAtSix(t *testing.T) {
	c := candidateCatalog(t)
	before := time.Date(2026, 10, 18, 5, 59, 59, 0, beijing)
	boundary := before.Add(time.Second)
	for _, mode := range []Mode{Normal, Hard} {
		first, err := c.DailyPlan(before, mode)
		if err != nil {
			t.Fatal(err)
		}
		utc, err := c.DailyPlan(before.UTC(), mode)
		if err != nil || !reflect.DeepEqual(first, utc) {
			t.Fatal("时区改变了名单")
		}
		again, _ := candidateCatalog(t).DailyPlan(before.Add(-time.Hour), mode)
		if !reflect.DeepEqual(first, again) {
			t.Fatal("重载或同服务日改变了名单")
		}
		next, _ := c.DailyPlan(boundary, mode)
		if next.ServiceDay != first.ServiceDay+1 {
			t.Fatal("06点未切换服务日")
		}
		want := 5
		if mode == Hard {
			want = 7
		}
		if len(first.Encounters) != want {
			t.Fatal(first)
		}
		seen := map[uint32]bool{}
		for i, row := range first.Encounters {
			if seen[row.MapID] {
				t.Fatal("名单重复地图")
			}
			seen[row.MapID] = true
			if mode == Hard {
				if row.Balance != c.config.HardBalance[i] || row.Optional || row.Weakness == "" || first.CommonWeakness == "" {
					t.Fatal("困难顺序/阶段倍率/弱点错误")
				}
			} else if i == 4 {
				if !row.Optional || row.Weakness != "" {
					t.Fatal("普通特殊关未保留可选与无弱点规则")
				}
			} else if row.Optional || row.Weakness == "" {
				t.Fatal("普通关弱点或可选状态错误")
			}
		}
		first.Encounters[0].MonsterID = 0
		untouched, _ := c.DailyPlan(before, mode)
		if untouched.Encounters[0].MonsterID == 0 {
			t.Fatal("调用者改写缓存名单")
		}
	}
	// 不要求任意相邻两日必定不同；检查长期轮换覆盖完整池并存在多种困难顺序。
	seenNormal, hardOrders := map[uint32]bool{}, map[string]bool{}
	for day := 0; day < 60; day++ {
		at := boundary.AddDate(0, 0, day)
		normal, _ := c.DailyPlan(at, Normal)
		hard, _ := c.DailyPlan(at, Hard)
		for _, row := range normal.Encounters {
			seenNormal[row.MonsterID] = true
		}
		order := make([]uint32, len(hard.Encounters))
		for i, encounter := range hard.Encounters {
			order[i] = encounter.MonsterID
		}
		hardOrders[fmt.Sprint(order)] = true
	}
	if len(seenNormal) != 7 || len(hardOrders) < 2 {
		t.Fatal("日轮换没有覆盖怪物池")
	}
}

func TestFirstEntryOnlyModeLockAndDailyReset(t *testing.T) {
	c := candidateCatalog(t)
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, beijing)
	roster := [2]repo.CharacterRecord{{CharacterID: "1", AccountID: "a", Level: 90}, {CharacterID: "2", AccountID: "a", Level: 91}}
	first, err := c.QuoteEntry(now, Normal, "a", roster, 120000, DayState{})
	if err != nil || first.Fee != 120000 || !first.Next.Paid {
		t.Fatalf("首次报价: %+v %v", first, err)
	}
	reentry, err := c.QuoteEntry(now, Normal, "a", roster, 0, first.Next)
	if err != nil || reentry.Fee != 0 {
		t.Fatal("重复入场再次收费", err)
	}
	if _, err := c.QuoteEntry(now, Hard, "a", roster, 120000, first.Next); err == nil {
		t.Fatal("当天换模式")
	}
	for _, corrupt := range []DayState{
		{AccountID: "b", ServiceDay: first.Next.ServiceDay, Paid: true, Mode: Normal, ConfigHash: first.Next.ConfigHash},
		{AccountID: "a", ServiceDay: first.Next.ServiceDay + 1, Paid: true, Mode: Normal, ConfigHash: first.Next.ConfigHash},
		{AccountID: "a", ServiceDay: first.Next.ServiceDay, Paid: true, Mode: Normal, ConfigHash: "其他配置"},
		{AccountID: "a", ServiceDay: first.Next.ServiceDay, Paid: false, Mode: Normal},
	} {
		if _, err := c.QuoteEntry(now, Normal, "a", roster, 120000, corrupt); err == nil {
			t.Fatal("错误账号、未来日或损坏账本被接纳", corrupt)
		}
	}
	if _, err := c.QuoteEntry(now, Normal, "a", roster, 119999, DayState{}); err == nil {
		t.Fatal("金币不足")
	}
	reset, err := c.QuoteEntry(time.Date(2026, 10, 19, 6, 0, 0, 0, beijing), Hard, "a", roster, 120000, first.Next)
	if err != nil || reset.Fee != 120000 || reset.Next.Mode != Hard {
		t.Fatal("次日06点未重置费用与模式", err)
	}
	for _, bad := range [][2]repo.CharacterRecord{
		{roster[0], roster[0]},
		{roster[0], {CharacterID: "2", AccountID: "b", Level: 90}},
		{roster[0], {CharacterID: "2", AccountID: "a", Level: 89}},
	} {
		if _, err := c.QuoteEntry(now, Normal, "a", bad, 120000, DayState{}); err == nil {
			t.Fatal("非法双角色入场")
		}
	}
}

func TestNormalRewardsArePerAccountDayMonsterAndHardTotals(t *testing.T) {
	c := candidateCatalog(t)
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, beijing)
	roster := [2]repo.CharacterRecord{{CharacterID: "1", AccountID: "a", Level: 90}, {CharacterID: "2", AccountID: "a", Level: 90}}
	entry, err := c.QuoteEntry(now, Normal, "a", roster, 120000, DayState{})
	if err != nil {
		t.Fatal(err)
	}
	id := entry.Plan.Encounters[0].MonsterID
	low, next, err := c.QuoteNormalReward(now, id, map[byte]byte{0: 49, 1: 99}, entry.Next)
	if err != nil || len(low) != 2 || low[0].Count != 2 || low[1].Count != 3 || entry.Next.NormalClaims[id] {
		t.Fatal("奖励随机边界或输入隔离", err)
	}
	hi, _, err := c.QuoteNormalReward(now, id, map[byte]byte{0: 50, 1: 0}, entry.Next)
	if err != nil || hi[0].Count != 3 {
		t.Fatal("50分界错误", err)
	}
	replayed, same, err := c.QuoteNormalReward(now, id, nil, next)
	if err != nil || len(replayed) != 0 || !same.NormalClaims[id] {
		t.Fatal("重复击杀再次奖励", err)
	}
	// 换同账号角色后仍使用同一份账号日状态。
	roster[0], roster[1] = roster[1], roster[0]
	reentry, err := c.QuoteEntry(now, Normal, "a", roster, 0, next)
	if err != nil || !reentry.Next.NormalClaims[id] {
		t.Fatal("换角色清空奖励", err)
	}
	if _, _, err := c.QuoteNormalReward(now, 999, map[byte]byte{0: 0, 1: 0}, next); err == nil {
		t.Fatal("非今日怪物奖励")
	}
	if _, _, err := c.QuoteNormalReward(now.Add(24*time.Hour), id, map[byte]byte{0: 0, 1: 0}, next); err == nil {
		t.Fatal("过期战斗跨日领款")
	}
	special := entry.Plan.Encounters[4].MonsterID
	specialRows, _, err := c.QuoteNormalReward(now, special, map[byte]byte{0: 0, 1: 0}, entry.Next)
	if err != nil || specialRows[0].Count != 5 {
		t.Fatal("特殊关奖励", err)
	}
	full, err := c.HardRewardTotal(7)
	if err != nil || len(full) != 2 || full[0].Count != 27 || full[1].Count != 30 {
		t.Fatal("困难错误累计七列", err)
	}
	if _, err := c.HardRewardTotal(0); err == nil {
		t.Fatal("零通关奖励")
	}
}
