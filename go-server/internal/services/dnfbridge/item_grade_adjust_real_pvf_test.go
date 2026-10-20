package dnfbridge

import (
	"os"
	"testing"

	dnfitemgrade "longheng.io/server/internal/modules/dnf/itemgrade"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// 真实运行时 PVF 回归：「耳环无法使用装备品级调整器」的判据链。
//
// 现场（2026-09-25 香港 packet_log.20260925-162932.txt）：操作者手上就是四只
// 黄金品级调整箱 —— material_item_id 2683895(武器)/2683896(防具)/2683897(首饰)/
// 2683898(特殊装备)，op81 目标覆盖 [weapon] [pants] [amulet] [ring] [wrist]
// [support] [magic stone]，**没有任何一次目标是 [earring]**。原因是材料自己的
// [usable equip type] 白名单：首饰箱只列 [amulet] [ring] [wrist]、特殊装备箱只列
// [support] [magic stone]，两份都不含 [earring]（客户端因此根本不发 op81）。
//
// 本 PVF 里显式列了 [earring] 的是后来出的两只：
//
//	490701689 黄金品级调整箱 (Lv1~90)   … [support] [magic stone] [earring]
//	490701814 黄金品级调整箱 (特殊装备)   [support] [magic stone] [earring]
//
// 归因结论：不是"服务端白名单写死了首饰/防具"（服务端此前完全没有白名单），而是
// 旧款分箱道具的 PVF 数据里没有 earring。服务端本轮补上的白名单与 PVF 一致：
// 表为空 = 不限部位，表非空才拦，目标部位解析不出来时不新拦。
func TestRealPVFGradeAdjustUsableEquipTypeWhitelist(t *testing.T) {
	pvfPath := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to verify the runtime-PVF grade-adjust whitelist")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: pvfPath})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}

	resolve := func(itemID uint32) dungeonDropItemDefinition {
		t.Helper()
		definition, err := catalog.ResolveItem(itemID)
		if err != nil {
			t.Fatalf("resolve item=%d: %v", itemID, err)
		}
		return definition
	}

	// 材料侧白名单（PVF [usable equip type]）
	cases := []struct {
		itemID    uint32
		path      string
		hasTypes  bool
		wantTypes []string
	}{
		{itemID: 15, path: "stackable/cash/box_kaleido.stk", hasTypes: false},
		{itemID: 2683895, path: "stackable/cash/chn_20140819_gold_kaleido/chn_03_gold_kaleido_weapon_2683895.stk", hasTypes: true, wantTypes: []string{"[weapon]"}},
		{itemID: 2683897, path: "stackable/cash/chn_20140819_gold_kaleido/chn_05_gold_kaleido_acc_2683897.stk", hasTypes: true, wantTypes: []string{"[amulet]", "[ring]", "[wrist]"}},
		{itemID: 2683898, path: "stackable/cash/chn_20140819_gold_kaleido/chn_06_gold_kaleido_sub_2683898.stk", hasTypes: true, wantTypes: []string{"[support]", "[magic stone]"}},
		{itemID: 490701689, path: "stackable/cash/chn_490701689.stk", hasTypes: true, wantTypes: []string{"[weapon]", "[coat]", "[pants]", "[shoulder]", "[waist]", "[shoes]", "[amulet]", "[ring]", "[wrist]", "[support]", "[magic stone]", "[earring]"}},
		{itemID: 490701814, path: "stackable/cash/chn_490701814.stk", hasTypes: true, wantTypes: []string{"[support]", "[magic stone]", "[earring]"}},
	}
	whitelists := make(map[uint32]dnfitemgrade.ItemDefinition, len(cases))
	for _, test := range cases {
		definition := resolve(test.itemID)
		if definition.PVFPath != test.path {
			t.Fatalf("item=%d path=%q, want %q", test.itemID, definition.PVFPath, test.path)
		}
		if test.hasTypes {
			if len(definition.UsableEquipTypes) != len(test.wantTypes) {
				t.Fatalf("item=%d usable_equip_types=%v, want %v", test.itemID, definition.UsableEquipTypes, test.wantTypes)
			}
			for index, want := range test.wantTypes {
				if got := dnfitemgrade.NormalizeEquipmentType(definition.UsableEquipTypes[index]); got != want {
					t.Fatalf("item=%d usable_equip_types[%d]=%q, want %q", test.itemID, index, got, want)
				}
			}
		} else if len(definition.UsableEquipTypes) != 0 {
			t.Fatalf("item=%d must not declare [usable equip type]: %v", test.itemID, definition.UsableEquipTypes)
		}
		whitelists[test.itemID] = dnfitemgrade.ItemDefinition{
			StackableType:    definition.StackableType,
			UsableEquipTypes: definition.UsableEquipTypes,
		}
		t.Logf("material=%d path=%s stackable_type=%q usable_equip_types=%v",
			test.itemID, definition.PVFPath, definition.StackableType, definition.UsableEquipTypes)
	}

	// 目标侧部位（PVF [equipment type]）
	earring := resolve(100390011)
	if got := dnfitemgrade.NormalizeEquipmentType(earring.EquipmentType); got != "[earring]" {
		t.Fatalf("earring 100390011 equipment_type=%q, want [earring] (path=%s)", got, earring.PVFPath)
	}
	t.Logf("earring item=100390011 path=%s equipment_type=%q", earring.PVFPath, earring.EquipmentType)

	matrix := []struct {
		name     string
		material uint32
		target   string
		want     bool
	}{
		{name: "普通装备品级调整箱(无白名单) × 耳环", material: 15, target: earring.EquipmentType, want: true},
		{name: "黄金(首饰) × 耳环", material: 2683897, target: earring.EquipmentType, want: false},
		{name: "黄金(特殊装备 2014) × 耳环", material: 2683898, target: earring.EquipmentType, want: false},
		{name: "黄金(Lv1~90) × 耳环", material: 490701689, target: earring.EquipmentType, want: true},
		{name: "黄金(特殊装备 积分商城) × 耳环", material: 490701814, target: earring.EquipmentType, want: true},
		{name: "黄金(武器) × 耳环", material: 2683895, target: earring.EquipmentType, want: false},
		{name: "黄金(首饰) × 目标部位不可解析", material: 2683897, target: "", want: true},
	}
	for _, test := range matrix {
		t.Run(test.name, func(t *testing.T) {
			if got := dnfitemgrade.AllowsTargetEquipmentType(whitelists[test.material], test.target); got != test.want {
				t.Fatalf("material=%d target=%q allow=%t want %t", test.material, test.target, got, test.want)
			}
		})
	}
}
