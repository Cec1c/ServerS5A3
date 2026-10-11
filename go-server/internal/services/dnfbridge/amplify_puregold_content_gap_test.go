package dnfbridge

import (
	"os"
	"reflect"
	"sort"
	"strconv"
	"testing"

	platformpvf "longheng.io/server/internal/platform/pvf"
)

// 现场报障（操作者原话）："无效道具 2683801 纯净的黄金增幅书"。
//
// 结论：这是**运行时 PVF 的内容缺口，不是 Go 缺陷**。服务器侧的"黄金增幅"
// 链路已经实现且有据可依——
//   - `[random invest upgrade option]`（etc/amplifyitem.etc）→ `config.PureGold`，
//   - 材料道具自己的 `[amplification random value]` → 权重表（见
//     amplify_item_pvf.go 的 `resolveCurrentAmplifyItemMetadata`），
//
// 而运行时 `etc/amplifyitem.etc` 的这个段是**空的**，道具 2683801 也没有出现在
// 任何一份增幅材料表里（purify / purify only / purify only cera / invest /
// reinvest / gold purify）。所以客户端就算把道具交上来，服务端也找不到它的材料
// 身份，道具就是"无效"。
//
// 本用例的硬断言是"内容缺口仍然存在"：**一旦它失败，说明运行时 PVF 补上了这条
// 记录**——那时不需要改 Go 代码（链路已实现），只需现场复测一次使用/增幅，然后
// 把这里改成正向断言。
func TestRealPVFAmplifyPureGoldItemIsNotRegisteredAsMaterial(t *testing.T) {
	pvfPath := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to audit the live amplification material tables")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: pvfPath})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	const itemID = uint32(2683801)
	definition, err := catalog.ResolveItem(itemID)
	if err != nil {
		t.Fatalf("item %d is not even defined in the runtime PVF item index: %v", itemID, err)
	}
	if definition.Kind != dungeonDropItemStackable ||
		definition.PVFPath != "stackable/cash/chn_20140701_hotsummer_package/chn_stk_puregold_amplifying_scroll_2683801.stk" {
		t.Fatalf("item %d resolves to kind=%s path=%s", itemID, definition.Kind, definition.PVFPath)
	}
	document, err := parseDungeonCardPVFDocument(catalog.source, definition.PVFPath)
	if err != nil {
		t.Fatal(err)
	}
	name, _ := document.Text("name")
	if name != "纯净的黄金增幅书" {
		t.Fatalf("item %d name=%q", itemID, name)
	}
	// 道具自带"随机增幅等级权重表"——这正是服务端 PureGold 分支读取的那张表，
	// 说明道具本身是按"黄金增幅材料"设计的，缺的只是目录登记。
	if got := document.Ints("amplification random value"); !reflect.DeepEqual(got, []int64{3, 50, 4, 30, 5, 15, 6, 5}) {
		t.Fatalf("item %d [amplification random value]=%v", itemID, got)
	}

	config, err := currentAmplifyItemConfigFromSource(archive)
	if err != nil {
		t.Fatalf("parse etc/amplifyitem.etc: %v", err)
	}
	tables := []struct {
		name  string
		table map[int64]currentAmplifyMaterialOption
	}{
		{"random invest upgrade option", config.PureGold},
		{"invest option", config.Invest},
		{"reinvest option", config.Reinvest},
	}
	for _, entry := range tables {
		if option, found := entry.table[int64(itemID)]; found {
			t.Fatalf("item %d is now registered in [%s] as option=%d count=%d: the PVF content gap is closed; "+
				"the Go amplification path already reads this table, so re-verify a live use and turn this test into a positive assertion",
				itemID, entry.name, option.Option, option.Count)
		}
		t.Logf("[%s] entries=%d, item %d absent", entry.name, len(entry.table), itemID)
	}
	if count, found := config.Purify[int64(itemID)]; found {
		t.Fatalf("item %d is now registered in [purify material] count=%d; same tripwire as above", itemID, count)
	}
	if count, found := config.Clear[int64(itemID)]; found {
		t.Fatalf("item %d is now registered in a purify-only table count=%d; same tripwire as above", itemID, count)
	}
	t.Logf("item %d (%s) is defined but absent from every amplification material table; pure gold entries=%d",
		itemID, name, len(config.PureGold))

	// 关键对照：**同样的"纯净的黄金增幅书"已经在售的表里有一件**（item 8238，
	// stackable/event/tcg/event_8238.stk，同名同描述同 [amplification random value]），
	// 所以"服务器不支持这个功能"不成立——缺的只是 2683801 这一条目录登记。
	twins := make([]string, 0, 4)
	for registered := range config.PureGold {
		twinDefinition, resolveErr := catalog.ResolveItem(uint32(registered))
		if resolveErr != nil {
			continue
		}
		twinDocument, documentErr := parseDungeonCardPVFDocument(catalog.source, twinDefinition.PVFPath)
		if documentErr != nil {
			continue
		}
		twinName, _ := twinDocument.Text("name")
		if twinName != name {
			continue
		}
		if !reflect.DeepEqual(twinDocument.Ints("amplification random value"), document.Ints("amplification random value")) {
			continue
		}
		twins = append(twins, strconv.FormatInt(registered, 10)+"="+twinDefinition.PVFPath)
	}
	if len(twins) == 0 {
		t.Fatalf("no registered pure-gold item shares item %d's name %q and random-value table; the Go path may be untested for this family", itemID, name)
	}
	sort.Strings(twins)
	t.Logf("registered twin(s) of %q: %v", name, twins)
}
