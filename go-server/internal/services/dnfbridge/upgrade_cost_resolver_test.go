package dnfbridge

import (
	"context"
	"longheng.io/server/internal/modules/dnf/inventory"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	memory "longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
	"os"
	"testing"
)

func TestNativeUpgradeCostUsesGradeRatherThanMinimumLevel(t *testing.T) {
	table := &currentUpgradeTable{Costs: []int{10, 100, 1000}, TypeWeights: []float64{1}, CostWeightsByRarity: []float64{1}, CostWeightsByUpgradeLevel: map[int][2]float64{0: {1, 1}}}
	for _, tc := range []struct {
		grade string
		want  int64
		bad   bool
	}{{"1", 100, false}, {"2", 1000, false}, {"1 2", 0, true}, {"1.5", 0, true}, {"-1", 0, true}, {"3", 0, true}} {
		doc, err := dnfpvf.Parse("equipment/test.equ", "[grade]\n"+tc.grade+"\n[minimum level]\n0\n[rarity]\n0\n[equipment type]\n`[weapon]` 22\n")
		if err != nil {
			t.Fatal(err)
		}
		got, err := resolveNativeUpgradeEquipmentCost(table, doc, 0)
		if tc.bad {
			if err == nil {
				t.Fatalf("accepted grade %q", tc.grade)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Fatalf("grade %s cost=%d err=%v", tc.grade, got, err)
		}
	}
}

func TestRealPVFJonathanTargetCostInputs(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	def, err := catalog.ResolveItem(28309)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := parseDungeonCardPVFDocument(archive, def.PVFPath)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := archive.ReadText(currentUpgradePVFPath)
	if err != nil {
		t.Fatal(err)
	}
	td, err := dnfpvf.Parse(currentUpgradePVFPath, raw)
	if err != nil {
		t.Fatal(err)
	}
	table := parseCurrentUpgradeTable(td, raw)
	for level, want := range map[int]int64{0: 84975, 4: 169950, 10: 254925, 12: 679800} {
		cost, err := resolveNativeUpgradeEquipmentCost(table, doc, level)
		if err != nil || cost != want {
			t.Fatalf("level %d cost=%d err=%v", level, cost, err)
		}
		t.Logf("item=28309 grade=%v minimum=%v rarity=%v current=%d gold=%d materialCeil=%d", doc.Ints("grade"), doc.Ints("minimum level"), doc.Ints("rarity"), level, cost, (cost+99999)/100000)
	}
}

func TestRealPVFJonathanAtomicPaymentCandidate(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	service := &Service{initialEquipmentArchive: archive}
	resolver := service.alignedNativeUpgradeCostResolver()
	for _, tc := range []struct {
		name     string
		npc      uint16
		material int64
		success  bool
		gold     int64
	}{{"jonathan", 476, 1, true, 100000}, {"missing material", 476, 0, false, 100000}, {"ordinary", 1, 1, true, 15025}, {"unknown NPC", 65534, 1, false, 100000}} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			repos := memory.NewMemoryGroup()
			if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "77", AccountID: "a", Stats: map[string]int64{"gold": 100000}}); err != nil {
				t.Fatal(err)
			}
			if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "77", Slots: map[string]dnfrepo.ItemStack{
				"0:54":  {ItemID: 28309, Count: 1, Extra: map[string]string{"item_kind": "equipment", "durability": "45", "max_durability": "45"}},
				"0:135": {ItemID: 10158964, Count: tc.material},
			}}); err != nil {
				t.Fatal(err)
			}
			if err := repos.AccountInventory.Save(ctx, dnfrepo.AccountInventoryRecord{AccountID: "a", Slots: map[string]dnfrepo.ItemStack{dnfrepo.AccountSharedInventorySlotKey(358): {ItemID: 3037, Count: 20}}}); err != nil {
				t.Fatal(err)
			}
			owner, err := inventory.NewOwner(repos)
			if err != nil {
				t.Fatal(err)
			}
			got, err := owner.Upgrade(ctx, inventory.Command{Mode: "reinforce", AccountID: "a", SelectedCharacterID: 77, TargetSlotIndex: 54, TargetItemTemplateID: 28309, MaterialSlotIndex: 358, OptionalTicketSlot: -1, UpgradeSuccessWeight: 100000, UpgradeMaterialItemID: 3037, UpgradeMaterialCount: 10, UpgradeNPCPresent: true, UpgradeNPCID: tc.npc, UpgradeNPCMaterialSlot: 135, UpgradeCostResolver: resolver})
			if got.Success != tc.success {
				t.Fatalf("result=%+v err=%v", got, err)
			}
			if tc.success && err != nil {
				t.Fatal(err)
			}
			wallet, _, _ := repos.Character.Load(ctx, "77")
			bag, _, _ := repos.Inventory.Load(ctx, "77")
			account, _, _ := repos.AccountInventory.Load(ctx, "a")
			if wallet.Stats["gold"] != tc.gold {
				t.Fatalf("gold=%d want=%d", wallet.Stats["gold"], tc.gold)
			}
			wantNormal := int64(20)
			wantConversion := tc.material
			if tc.success {
				wantNormal = 10
				if tc.npc == 476 {
					wantConversion--
				}
			}
			if account.Slots[dnfrepo.AccountSharedInventorySlotKey(358)].Count != wantNormal || bag.Slots["0:135"].Count != wantConversion {
				t.Fatalf("material mismatch bag=%+v shared=%+v", bag.Slots, account.Slots)
			}
		})
	}
}

// 段106 修复后：只有**空值**是默认启用档（且只钉已核的 476 材料转换），其余历史值
// （off / vendor / 1 …）保持修复前接线（nil），candidate 仍是显式整链档。
func TestNativeUpgradeCostCandidateIsExplicitOptIn(t *testing.T) {
	service := &Service{}
	t.Setenv("DNFBRIDGE_NATIVE_UPGRADE_COST", "")
	if service.candidateUpgradeCostResolver(nil) == nil {
		t.Fatal("empty value must install the verified NPC conversion resolver")
	}
	for _, value := range []string{"off", "vendor", "1", "disable", "unknown-value"} {
		t.Setenv("DNFBRIDGE_NATIVE_UPGRADE_COST", value)
		if service.candidateUpgradeCostResolver(nil) != nil {
			t.Fatalf("env %q: non-empty non-candidate values must keep the pre-fix wiring", value)
		}
	}
	t.Setenv("DNFBRIDGE_NATIVE_UPGRADE_COST", "candidate")
	if service.candidateUpgradeCostResolver(nil) == nil {
		t.Fatal("candidate resolver missing")
	}
}

// Unsupported metadata is rejected until its native semantics are implemented;
// presence alone does not prove that an ordinary quote would be mispriced.
func TestNativeUpgradeCostRejectsUnsupportedMetadata(t *testing.T) {
	table := &currentUpgradeTable{Costs: []int{10, 100}, TypeWeights: []float64{1}, CostWeightsByRarity: []float64{1}, CostWeightsByUpgradeLevel: map[int][2]float64{0: {1, 1}}}
	base := "[grade]\n1\n[rarity]\n0\n[equipment type]\n\x60[weapon]\x60 22\n"
	for _, name := range []string{"level linear ability", "upgrade cost discount", "custom upgrade type", "upgrade exclusive material"} {
		for _, body := range []string{"", "1 70\n", "\x60unsupported\x60\n"} {
			t.Run(name+"/"+body, func(t *testing.T) {
				doc, err := dnfpvf.Parse("equipment/test.equ", base+"["+name+"]\n"+body+"[/"+name+"]\n")
				if err != nil {
					t.Fatal(err)
				}
				if _, err := resolveNativeUpgradeEquipmentCost(table, doc, 0); err == nil {
					t.Fatal("accepted unsupported metadata")
				}
			})
		}
	}
	for _, suffix := range []string{"[ LeVeL LiNeAr AbIlItY ]\n", "[level linear ability]\n1 70\n[grade]\n1 70\n[/level linear ability]\n"} {
		doc, err := dnfpvf.Parse("equipment/test.equ", base+suffix)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := resolveNativeUpgradeEquipmentCost(table, doc, 0); err == nil {
			t.Fatal("accepted normalized or nested unsupported metadata")
		}
	}
	doc, err := dnfpvf.Parse("equipment/test.equ", base)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := resolveNativeUpgradeEquipmentCost(table, doc, 0); err != nil || got != 100 {
		t.Fatalf("ordinary quote=%d err=%v", got, err)
	}
}

func TestRealPVFUpgradeCostUnsupportedMetadataBoundary(t *testing.T) {
	pvfPath := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE")
	}
	archive, err := platformpvf.Open(pvfPath)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := archive.ReadText(currentUpgradePVFPath)
	if err != nil {
		t.Fatal(err)
	}
	td, err := dnfpvf.Parse(currentUpgradePVFPath, raw)
	if err != nil {
		t.Fatal(err)
	}
	table := parseCurrentUpgradeTable(td, raw)
	for _, tc := range []struct {
		id          int
		path        string
		unsupported string
	}{
		{10194, "equipment/character/common/jacket/cloth/coat_10194.equ", "level linear ability"},
		{104000339, "equipment/character/gunner/weapon/revolver/104000339.equ", "upgrade exclusive material"},
		{28309, "equipment/character/swordman/weapon/beamsword/beamswd_28309.equ", ""},
	} {
		raw, err := archive.ReadText(tc.path)
		if err != nil {
			t.Fatal(err)
		}
		doc, err := dnfpvf.Parse(tc.path, raw)
		if err != nil {
			t.Fatal(err)
		}
		if tc.unsupported != "" {
			if _, present := doc.Section(tc.unsupported); !present {
				t.Fatalf("item %d missing expected metadata", tc.id)
			}
			if _, err := resolveNativeUpgradeEquipmentCost(table, doc, 0); err == nil {
				t.Errorf("item %d accepted unsupported %s", tc.id, tc.unsupported)
			}
			continue
		}
		for level, want := range map[int]int64{0: 84975, 4: 169950, 10: 254925, 12: 679800} {
			got, err := resolveNativeUpgradeEquipmentCost(table, doc, level)
			if err != nil || got != want {
				t.Errorf("item %d +%d gold=%d err=%v want=%d", tc.id, level, got, err, want)
			}
		}
	}
}
