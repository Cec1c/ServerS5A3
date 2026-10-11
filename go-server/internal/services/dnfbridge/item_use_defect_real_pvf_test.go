package dnfbridge

import (
	"os"
	"strings"
	"testing"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// 本文件把"道具使用"三条现场缺陷的真档 PVF 依据钉下来。它只读 Script.pvf，
// 不依赖数据库，也不改任何东西。跑法：
//
//	DNFBRIDGE_REAL_PVF_SMOKE=<repo>\runtime\data\dnf\Script.pvf \
//	  go test -count=1 -run TestRealPVFItemUseDefectFixture ./internal/services/dnfbridge
func TestRealPVFItemUseDefectFixture(t *testing.T) {
	pvfPath := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to the runtime Script.pvf")
	}
	archive, err := platformpvf.Open(pvfPath)
	if err != nil {
		t.Fatalf("open PVF: %v", err)
	}
	catalog, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatalf("build item catalog: %v", err)
	}

	// ── #1「用完变成金币」：运行时 Script.pvf 的 item 0 就是「金币」────────────
	//
	// 客户端用行里的 itemID 查自己的物品表：itemID=0 的行会被画成
	// `[name] 金币` / `[rarity] 1`(高级) / `[stackable type] `[waste]`(消耗品)——
	// 与操作者截图逐项一致。所以"消耗品用完变成金币"= 我们把那一行的 itemID 写成了 0
	// （见 internal/modules/dnf/inventory/body.go 的
	//  buildCommonItemListUpdateBody / emptyItemListEntryItemID 注释）。
	goldText, err := archive.ReadText("stackable/gold.stk")
	if err != nil {
		t.Fatalf("read stackable/gold.stk: %v", err)
	}
	gold, err := dnfpvf.Parse("stackable/gold.stk", goldText)
	if err != nil {
		t.Fatalf("parse stackable/gold.stk: %v", err)
	}
	goldName, _ := gold.Text("name")
	if strings.TrimSpace(strings.ReplaceAll(goldName, "`", "")) != "金币" {
		t.Fatalf("stackable/gold.stk name=%q want 金币 (this is the item the client paints for itemID=0)", goldName)
	}
	goldRarity, ok := gold.Int("rarity")
	if !ok || goldRarity != 1 {
		t.Fatalf("stackable/gold.stk rarity=%d found=%t want 1 (高级)", goldRarity, ok)
	}
	goldType, _ := gold.Text("stackable type")
	if !strings.EqualFold(strings.TrimSpace(strings.ReplaceAll(goldType, "`", "")), "[waste]") {
		t.Fatalf("stackable/gold.stk stackable type=%q want [waste] (消耗品)", goldType)
	}

	listText, err := archive.ReadText(dungeonDropStackableList)
	if err != nil {
		t.Fatalf("read %s: %v", dungeonDropStackableList, err)
	}
	listDocument, err := dnfpvf.Parse(dungeonDropStackableList, listText)
	if err != nil {
		t.Fatalf("parse %s: %v", dungeonDropStackableList, err)
	}
	goldListed := false
	for _, entry := range dnfpvf.ParseList(listDocument) {
		if entry.ID != 0 {
			continue
		}
		goldListed = true
		if !strings.Contains(strings.ToLower(entry.Path), "gold.stk") {
			t.Fatalf("stackable.lst id 0 path=%q want gold.stk", entry.Path)
		}
	}
	if !goldListed {
		t.Fatalf("%s has no id-0 entry; the client resolves itemID=0 through this table", dungeonDropStackableList)
	}

	// ── #2「瞬间移动药水点不动」：op237 携带的 itemCode 就是 2600014 ────────────
	//
	// stackable.lst 里 `2600014 \`professional/potion/ptn_instantmovement.stk\``；
	// 注意文件名不含 id，用路径子串是搜不到的，只能读 lst。
	potion, err := catalog.ResolveItem(2600014)
	if err != nil {
		t.Fatalf("resolve teleport potion 2600014: %v", err)
	}
	if !strings.HasSuffix(strings.ToLower(potion.PVFPath), "professional/potion/ptn_instantmovement.stk") {
		t.Fatalf("item 2600014 path=%q want professional/potion/ptn_instantmovement.stk", potion.PVFPath)
	}
	if !strings.EqualFold(strings.TrimSpace(potion.StackableType), "[teleport potion]") {
		t.Fatalf("item 2600014 stackable type=%q want [teleport potion]", potion.StackableType)
	}
	if !currentTeleportPotionIsValid(catalog, 2600014) {
		t.Fatalf("currentTeleportPotionIsValid(2600014)=false; the live op237 request would be rejected as a non-potion")
	}

	// ── #3「副职业分解机放不下去」：便携式分解机的真档声明 ────────────────────
	//
	// 它们没有 `[action type]` / `[use action packet]`，客户端的放置流程完全本地发起，
	// 关键声明是 `[portable disjoint] N` + `[expert type] `[disjointer]``。
	portable := map[uint32]string{
		2600468: "disjoint_portable1.stk", // 破旧的便携式分解机
		2600469: "disjoint_portable2.stk", // 普通的便携式分解机
		2600470: "disjoint_portable3.stk", // 坚固的便携式分解机
		2600548: "disjoint_portable4.stk", // 优质的便携式分解机
	}
	for itemID, wantSuffix := range portable {
		definition, err := catalog.ResolveItem(itemID)
		if err != nil {
			t.Fatalf("resolve portable disassembler %d: %v", itemID, err)
		}
		if !strings.HasSuffix(strings.ToLower(definition.PVFPath), wantSuffix) {
			t.Fatalf("item %d path=%q want suffix %s", itemID, definition.PVFPath, wantSuffix)
		}
		if !strings.EqualFold(strings.TrimSpace(definition.StackableType), "[town and dungeon]") {
			t.Fatalf("item %d stackable type=%q want [town and dungeon]", itemID, definition.StackableType)
		}
		text, err := archive.ReadText(definition.PVFPath)
		if err != nil {
			t.Fatalf("read %s: %v", definition.PVFPath, err)
		}
		document, err := dnfpvf.Parse(definition.PVFPath, text)
		if err != nil {
			t.Fatalf("parse %s: %v", definition.PVFPath, err)
		}
		if _, found := document.Int("portable disjoint"); !found {
			t.Fatalf("%s has no [portable disjoint] declaration", definition.PVFPath)
		}
		expertType, _ := document.Text("expert type")
		if !strings.EqualFold(strings.TrimSpace(strings.ReplaceAll(expertType, "`", "")), "[disjointer]") {
			t.Fatalf("%s expert type=%q want [disjointer]", definition.PVFPath, expertType)
		}
	}
}
