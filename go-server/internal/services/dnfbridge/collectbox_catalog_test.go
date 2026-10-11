package dnfbridge

import (
	"strings"
	"testing"
	"time"
)

// 收集箱目录（localization/collectbox/collectboxinfo.etc）的解析口径用例。
// 夹具用真档语法（含 [Extend Item]/[/Extend Item]、[Item Position Info] 的
// "行数 每行个数 + 行数×每行 对数据" 形态）。

const collectboxCatalogFixture = `
[Collect Box Info]

[Index]
1 
[base expiration date]
` + "`2014-12-30 06:00:00`" + ` 
[max expiration date]
` + "`2015-02-01 06:00:00`" + ` 
[Extend Item]
10007344 1 
[/Extend Item]

[Item Position Info]
2 3 10007322 1 10007327 2 10007332 2 10007323 1 10007328 2 10007333 2 
[/Item Position Info]

[UI Back Image]
` + "`interface2/CollectBox/SAO_UI.img`" + ` 0 
[UI Btn Image]
` + "`interface2/CollectBox/SAO_UI.img`" + ` 1 2 3 4 
[Box Explain]
` + "`SAO收集箱的属性有效期至2014年12月30日。`" + ` 
[UI Explain 1]
` + "`SAO收集箱`" + ` 
[UI Explain 2]
` + "`力量 +%d、 智力 +%d、 体力 +%d、 精神 +%d`" + ` 
[UI Explain 3]
` + "`有效期限还剩%d天 %d小时`" + ` 255 0 0 255 
[/Collect Box Info]

[Collect Box Info]

[Index]
2 
[base expiration date]
` + "`2028-12-31 06:00:00`" + ` 
[max expiration date]
` + "`2028-12-31 06:00:00`" + ` 
[Extend Item]

[/Extend Item]

[Item Position Info]
3 3 490007148 3 490007147 3 490007146 3 490007150 3 490007149 3 490007151 3 490007144 3 490007145 3 490007143 3 
[/Item Position Info]

[UI Back Image]
` + "`interface2/CollectBox/chn_wild_forest.img`" + ` 0 
[UI Btn Image]
` + "`interface2/CollectBox/chn_wild_forest.img`" + ` 1 2 3 4 
[Box Explain]
` + "`    兽人族英雄宝珠收集箱的属性有效期至2018年12月31日维护前。`" + ` 
[UI Explain 1]
` + "`兽人守护神`" + ` 255 177 0 255 
[UI Explain 2]
` + "`物理、魔法攻击力 +%d、 独立攻击力 +%d`" + ` 255 255 0 255 
[UI Explain 3]
` + "`有效期限还剩%d天 %d小时`" + ` 255 0 0 255 
[/Collect Box Info]
`

func TestCurrentCollectboxCatalogParsesRepeatedCollectBoxInfoSections(t *testing.T) {
	catalog, err := parseCurrentCollectboxCatalog(currentCollectboxInfoPVFPath, collectboxCatalogFixture)
	if err != nil {
		t.Fatalf("parse collectbox catalog: %v", err)
	}
	if got := catalog.Indexes(); len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Fatalf("indexes=%v want [1 2]", got)
	}
	box1, ok := catalog.Box(1)
	if !ok {
		t.Fatal("box 1 missing")
	}
	if box1.Title != "SAO收集箱" {
		t.Fatalf("box1 title=%q want SAO收集箱", box1.Title)
	}
	if !box1.HasBaseExpiration || !box1.HasMaxExpiration {
		t.Fatalf("box1 expiration flags base=%v max=%v want true", box1.HasBaseExpiration, box1.HasMaxExpiration)
	}
	if len(box1.ExtendItemIDs) != 1 || box1.ExtendItemIDs[0] != 10007344 {
		t.Fatalf("box1 extend items=%v want [10007344]", box1.ExtendItemIDs)
	}
	if box1.SlotRows != 2 || box1.SlotsPerRow != 3 || box1.SlotCount() != 6 || len(box1.Slots) != 6 {
		t.Fatalf("box1 layout rows=%d perRow=%d count=%d slots=%d want 2/3/6/6",
			box1.SlotRows, box1.SlotsPerRow, box1.SlotCount(), len(box1.Slots))
	}
	box2, ok := catalog.Box(2)
	if !ok {
		t.Fatal("box 2 missing")
	}
	if box2.Title != "兽人守护神" {
		t.Fatalf("box2 title=%q want 兽人守护神", box2.Title)
	}
	if box2.BuffFormat != "物理、魔法攻击力 +%d、 独立攻击力 +%d" {
		t.Fatalf("box2 buff format=%q", box2.BuffFormat)
	}
	if box2.BackImage != "interface2/CollectBox/chn_wild_forest.img" {
		t.Fatalf("box2 back image=%q", box2.BackImage)
	}
	// [Item Position Info] 的槛位 id 必须按 PVF 顺序落进 Slots，供下发时过滤。
	wantSlots := []int32{490007148, 490007147, 490007146, 490007150, 490007149, 490007151, 490007144, 490007145, 490007143}
	if len(box2.Slots) != len(wantSlots) {
		t.Fatalf("box2 slots=%d want %d", len(box2.Slots), len(wantSlots))
	}
	for idx, want := range wantSlots {
		if box2.Slots[idx].ItemID != want {
			t.Fatalf("box2 slot[%d]=%d want %d", idx, box2.Slots[idx].ItemID, want)
		}
	}
	if !box2.HasSlotItem(490007143) || box2.HasSlotItem(490700761) {
		t.Fatal("box2 HasSlotItem must accept PVF ids only")
	}
}

// TestCurrentCollectboxCatalogSlotsAreRowsTimesPerRow 钉住参考实现踩过的坑：
// [Item Position Info] 的首两个数是"行数"与"每行槛位数"，总槛位数 = 行数 × 每行。
func TestCurrentCollectboxCatalogSlotsAreRowsTimesPerRow(t *testing.T) {
	const text = "[Collect Box Info]\n[Index]\n4\n[Item Position Info]\n4 3 1 1 2 1 3 1 4 1 5 1 6 1 7 1 8 1 9 1 10 1 11 1 12 1\n[/Item Position Info]\n[/Collect Box Info]\n"
	catalog, err := parseCurrentCollectboxCatalog("fixture.etc", text)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	box, ok := catalog.Box(4)
	if !ok {
		t.Fatal("box 4 missing")
	}
	if box.SlotCount() != 12 || len(box.Slots) != 12 {
		t.Fatalf("slot count=%d slots=%d want 12/12 (4 rows x 3 per row)", box.SlotCount(), len(box.Slots))
	}
	if box.Slots[11].ItemID != 12 {
		t.Fatalf("last slot=%d want 12", box.Slots[11].ItemID)
	}
}

// TestCurrentCollectboxCatalogHasNoClientPlaceholderText 是"不出现 test string"
// 的服务端侧钉子：`test string1/2/3` 只存在于客户端
// ui/collectbox/collectbox.xui 的三个文本控件默认值里，服务端目录（真档）里
// 一个都不许有 —— 否则客户端即便收到状态也还是占位串。
func TestCurrentCollectboxCatalogHasNoClientPlaceholderText(t *testing.T) {
	catalog, err := parseCurrentCollectboxCatalog(currentCollectboxInfoPVFPath, collectboxCatalogFixture)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	for _, box := range catalog.Boxes {
		for name, value := range map[string]string{
			"title":       box.Title,
			"buff_format": box.BuffFormat,
			"expiry":      box.ExpiryFormat,
			"explain":     box.Explain,
			"back_image":  box.BackImage,
		} {
			if strings.Contains(strings.ToLower(value), "test string") {
				t.Fatalf("box %d %s=%q contains the client placeholder", box.Index, name, value)
			}
		}
	}
}

// TestParseCurrentCollectboxDateUsesUTCOffset8 钉住到期时间的时区口径
// （判据：厂商实测箱 5 的 u32 = 71494823 s 恰好等于 2028-12-30T22:00:00Z 减去
// 下发时刻，即 PVF 的 2028-12-31 06:00:00 是东八区）。
func TestParseCurrentCollectboxDateUsesUTCOffset8(t *testing.T) {
	value, ok := parseCurrentCollectboxDate("2028-12-31 06:00:00")
	if !ok {
		t.Fatal("date must parse")
	}
	want := time.Date(2028, 12, 30, 22, 0, 0, 0, time.UTC)
	if !value.Equal(want) {
		t.Fatalf("parsed=%s want %s", value.UTC(), want)
	}
	if _, ok := parseCurrentCollectboxDate("not a date"); ok {
		t.Fatal("garbage date must not parse")
	}
}
