package dnfbridge

import (
	"fmt"
	"testing"
)

func TestGeneratedEquipmentZeroUpLevelWindow(t *testing.T) {
	source := clearCardEquipmentTestSource()
	source[dungeonDropEquipmentList] = ""
	levels := map[uint32]int{}
	for rarity := 0; rarity <= 3; rarity++ {
		for _, level := range []int{82, 83, 88, 89} {
			id := uint32(10000 + rarity*100 + level)
			file := fmt.Sprintf("weapon/zero_up_%d.equ", id)
			source[dungeonDropEquipmentList] += fmt.Sprintf("%d `%s`\n", id, file)
			source["equipment/"+file] = fmt.Sprintf("[equipment type] `[weapon]` 1\n[minimum level] %d\n[rarity] %d\n[creation rate] 10\n[durability] 40", level, rarity)
			levels[id] = level
		}
	}
	catalog, err := newPVFDungeonDropCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	for _, selectItem := range []struct {
		name string
		fn   func(*currentDungeonDropLCG, int, int64, int64, int64) (uint32, bool, error)
	}{{"普通怪", catalog.selectOrdinaryMonsterEquipment}, {"翻牌", catalog.selectClearCardEquipment}} {
		t.Run(selectItem.name, func(t *testing.T) {
			for rarity := int64(0); rarity <= 3; rarity++ {
				seen := map[int]bool{}
				for seed := uint32(1); seed <= 128; seed++ {
					rng, equivalent := newCurrentDungeonDropLCG(seed), newCurrentDungeonDropLCG(seed)
					id, found, err := selectItem.fn(rng, 89, 6, 0, rarity)
					if err != nil || !found || (levels[id] != 83 && levels[id] != 88) {
						t.Fatalf("89级合法零上幅未选到区间内装备：rarity=%d id=%d found=%t err=%v", rarity, id, found, err)
					}
					seen[levels[id]] = true
					// 两种参数表达同一[83,89)窗口，应选到同一件装备并消耗相同随机数。
					want, wantFound, wantErr := selectItem.fn(equivalent, 88, 5, 1, rarity)
					if wantErr != nil || !wantFound || id != want || rng.Seed() != equivalent.Seed() {
						t.Fatalf("等价窗口的选物或随机数不一致：got=%d want=%d err=%v", id, want, wantErr)
					}
				}
				if !seen[83] || !seen[88] {
					t.Fatalf("区间两端未参与选择：rarity=%d seen=%v", rarity, seen)
				}
			}
		})
	}
}

func TestGeneratedEquipmentZeroUpRejectsInvalidAndEmptyWindows(t *testing.T) {
	catalog, err := newPVFDungeonDropCatalog(clearCardEquipmentTestSource())
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name     string
		level    int
		down, up int64
	}{{"负上幅", 89, 6, -1}, {"负下幅", 89, -1, 0}, {"超上幅", 89, 6, 201}, {"超下幅", 89, 201, 0}, {"空窗口", 89, 0, 0}, {"零等级", 0, 6, 0}, {"超等级", 201, 6, 0}} {
		t.Run(tc.name, func(t *testing.T) {
			rng := newCurrentDungeonDropLCG(7)
			_, found, err := catalog.selectGeneratedEquipment(rng, tc.level, tc.down, tc.up, 2)
			if err != nil || found || rng.Seed() != 7 {
				t.Fatalf("无效或空窗口产出装备/消耗随机数：found=%t seed=%d err=%v", found, rng.Seed(), err)
			}
		})
	}
}
