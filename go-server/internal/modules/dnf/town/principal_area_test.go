package town

import (
	"context"
	"strings"
	"testing"
)

// 真档形态（Script.pvf sha256 cc517a6e2b7c77a4ba0f30a1869a3ab234e281fe6251e35d60f0f04a82aacee1，
// 见 principal_area_real_pvf_test.go 的全量扫描）：
//
//	town/CentralPark.twn  area 0 `Town/CentralPark/Gate_CentralPark.map` `[gate]` 474 234
//	                      area 1 `Town/CentralPark/CentralPark.map`      `[normal]`
//	                      area 2 `Town/CentralPark/CentralPark_Waiting.map` `[dungeon gate]` 39
//	town/MetroCenter.twn  area 0 `Town/MetroCenter/Gate_MetroCenter.map` `[gate]` 474 234
//	                      area 1 `Town/MetroCenter/Metro_Time.map`       `[normal]`
func centralParkShapeDocument() string {
	return strings.Join([]string{
		"[area]",
		"0 `Town/CentralPark/Gate_CentralPark.map`",
		"`[gate]` 474 234",
		"[/area]",
		"[area]",
		"1 `Town/CentralPark/CentralPark.map`",
		"`[normal]`",
		"[/area]",
		"[area]",
		"2 `Town/CentralPark/CentralPark_Waiting.map`",
		"`[dungeon gate]` 39",
		"[/area]",
		"[name]",
		"`中央公园`",
	}, "\n")
}

func metroCenterShapeDocument() string {
	return strings.Join([]string{
		"[area]",
		"0 `Town/MetroCenter/Gate_MetroCenter.map`",
		"`[gate]` 474 234",
		"[/area]",
		"[area]",
		"1 `Town/MetroCenter/Metro_Time.map`",
		"`[normal]`",
		"[/area]",
		"[area]",
		"2 `Town/MetroCenter/Metro_Time_Waiting.map`",
		"`[dungeon gate]` 39",
		"[/area]",
		"[name]",
		"`魔界营地`",
	}, "\n")
}

// 真档 town/new_Elvengard.twn 的形态：area 0 是主街，带 `[gate]` 的是赛丽亚房间（area 1）。
func elvengardShapeDocument() string {
	return strings.Join([]string{
		"[area]",
		"0 `Cataclysm/Town/Elvengard/new_Elvengard.map`",
		"`[normal]`",
		"[/area]",
		"[area]",
		"1 `Cataclysm/Town/Elvengard/new_seria_room.map`",
		"`[gate]` 450 234",
		"[/area]",
		"[name]",
		"`艾尔文防线`",
	}, "\n")
}

func loadPrincipalAreaFixture(t *testing.T, list string, documents map[string]string) *Table {
	t.Helper()
	source := memorySource{"town/town.lst": list}
	for name, text := range documents {
		source[name] = text
	}
	table, err := Load(context.Background(), source, Options{})
	if err != nil {
		t.Fatal(err)
	}
	return table
}

// area 0 是城镇边界门图（地图名 `Gate_*`，真档 15/53 个城镇）时，主城区必须是
// 该城镇真正的城镇主图，而不是 area 0。
func TestFindPrincipalAreaSkipsBoundaryGateAtAreaZero(t *testing.T) {
	table := loadPrincipalAreaFixture(t,
		"31 `CentralPark.twn` 30 `MetroCenter.twn` 38 `new_Elvengard.twn`",
		map[string]string{
			"town/CentralPark.twn":   centralParkShapeDocument(),
			"town/MetroCenter.twn":   metroCenterShapeDocument(),
			"town/new_Elvengard.twn": elvengardShapeDocument(),
		})

	area, found := table.FindPrincipalArea(31)
	if !found || area.ID != 1 || area.MapPath != "Town/CentralPark/CentralPark.map" {
		t.Fatalf("town 31 principal area=(%+v,%t) want area 1 Town/CentralPark/CentralPark.map", area, found)
	}
	area, found = table.FindPrincipalArea(30)
	if !found || area.ID != 1 || area.MapPath != "Town/MetroCenter/Metro_Time.map" {
		t.Fatalf("town 30 principal area=(%+v,%t) want area 1 Town/MetroCenter/Metro_Time.map", area, found)
	}
	// area 0 不是边界图的城镇一字不改：今天选的就是 area 0。
	area, found = table.FindPrincipalArea(38)
	if !found || area.ID != 0 || area.MapPath != "Cataclysm/Town/Elvengard/new_Elvengard.map" {
		t.Fatalf("town 38 principal area=(%+v,%t) want area 0 new_Elvengard.map", area, found)
	}

	boundary, found := table.FindArea(31, 0)
	if !found || !boundary.IsBoundaryArea() || boundary.BoundarySignals() != "gate_map_name+pvf_gate_marker" {
		t.Fatalf("town 31 area 0 boundary=(%+v,%t) signals=%q", boundary, found, boundary.BoundarySignals())
	}
	if room, found := table.FindArea(38, 1); !found || room.IsBoundaryArea() {
		// 赛丽亚房间带 `[gate]` 入口坐标，但**不是** `Gate_*` 边界图 —— 这条交叉核对
		// 保证判据没有被"任何带 [gate] 的区域都算边界"污染。
		t.Fatalf("town 38 area 1 must not be a boundary map: (%+v,%t) signals=%q", room, found, room.BoundarySignals())
	}
}

// 带 PVF `[gate] <x> <y>` 入口坐标、但文件名不是 `Gate_*` 的城镇主图**不能**被当成
// 边界图：真档 town 33 `Town/Returnee/ReaturneeTown0.map`、town 103
// `chn_knight_of_round_vil/chn_knight_of_round_vil.map` 都是城镇主图
// （`[minimap rect]` + 入口坐标 594 234，与真档主城 town/180201_Wildforest.twn
// area 0 的 `[normal]` 594 234 同形）。
func TestFindPrincipalAreaIgnoresPVFGateMarkerOnMainTownMap(t *testing.T) {
	table := loadPrincipalAreaFixture(t, "33 `ReturneeCare.twn`", map[string]string{
		"town/ReturneeCare.twn": strings.Join([]string{
			"[area]",
			"0 `Town/Returnee/ReaturneeTown0.map`",
			"`[minimap rect]` 258 149 271 157 `[/minimap rect]` `[gate]` 594 234",
			"[/area]",
			"[name]",
			"`生化禁区`",
		}, "\n"),
	})
	zero, _ := table.FindArea(33, 0)
	if !zero.HasPVFGateMarker() || zero.IsBoundaryArea() {
		t.Fatalf("fixture town 33 area 0 must carry [gate] but not be a boundary map: %+v", zero)
	}
	area, found := table.FindPrincipalArea(33)
	if !found || area.ID != 0 || area.MapPath != "Town/Returnee/ReaturneeTown0.map" {
		t.Fatalf("town 33 principal=(%+v,%t) want the unchanged area 0 village map", area, found)
	}
}

// area 0 只带 `[dungeon gate]`（真档 town 39 的 new_Hendon_Main.map 就是这样）时
// **不**算边界门图：主城区仍然是 area 0，改动前后一致。
func TestFindPrincipalAreaKeepsDungeonGateMainStreetAtAreaZero(t *testing.T) {
	table := loadPrincipalAreaFixture(t, "39 `new_HendonMyre.twn`", map[string]string{
		"town/new_HendonMyre.twn": strings.Join([]string{
			"[area]",
			"0 `Cataclysm/Town/HendonMyre/new_Hendon_Main.map`",
			"`[minimap rect]` 175 201 325 230 `[/minimap rect]` `[dungeon gate]` 102",
			"[/area]",
			"[area]",
			"2 `Cataclysm/Town/HendonMyre/new_backstreet_1.map`",
			"`[normal]`",
			"[/area]",
			"[name]",
			"`赫顿玛尔`",
		}, "\n"),
	})
	area, found := table.FindPrincipalArea(39)
	if !found || area.ID != 0 || area.MapPath != "Cataclysm/Town/HendonMyre/new_Hendon_Main.map" {
		t.Fatalf("town 39 principal area=(%+v,%t) want area 0 new_Hendon_Main.map", area, found)
	}
}

// 判据只认"文件名 `Gate_*`"这条命名约定，**不认** PVF `[gate] <x> <y>` 入口坐标标记：
//   - town 60：area 0 叫 `Gate_Example.map`（这份夹具里连 `[gate]` 都没写）⇒ 边界图 ⇒ 主城区是 area 1；
//   - town 61：area 0 带 `[gate]` 但文件是赛丽亚房间（真档 town 38 area 1 的形态）⇒ 不是边界图 ⇒ 主城区仍是 area 0。
func TestFindPrincipalAreaUsesGateMapNamingNotThePVFGateMarker(t *testing.T) {
	table := loadPrincipalAreaFixture(t, "60 `name_only.twn` 61 `marker_only.twn`", map[string]string{
		"town/name_only.twn": strings.Join([]string{
			"[area]", "0 `Town/Example/Gate_Example.map`", "`[normal]`", "[/area]",
			"[area]", "1 `Town/Example/Example_Main.map`", "`[normal]`", "[/area]",
			"[name]", "`Example`",
		}, "\n"),
		"town/marker_only.twn": strings.Join([]string{
			"[area]", "0 `Cataclysm/Town/Elvengard/new_seria_room.map`", "`[gate]` 450 234", "[/area]",
			"[area]", "1 `Cataclysm/Town/Elvengard/new_Elvengard.map`", "`[normal]`", "[/area]",
			"[name]", "`Elvengard`",
		}, "\n"),
	})
	if area, found := table.FindPrincipalArea(60); !found || area.ID != 1 || area.MapPath != "Town/Example/Example_Main.map" {
		t.Fatalf("naming-convention boundary at area 0: principal=(%+v,%t) want area 1", area, found)
	}
	if area, found := table.FindPrincipalArea(61); !found || area.ID != 0 || area.MapPath != "Cataclysm/Town/Elvengard/new_seria_room.map" {
		t.Fatalf("pvf [gate] marker alone must not move the principal area: (%+v,%t) want area 0", area, found)
	}
}

// 边界图之后的候选里优先 `[normal]`：等待室（`[dungeon gate]`）排在主图前面时
// 不能把玩家丢进等待室。真档 15 个城镇的第一个非边界区恰好都是 `[normal]`，
// 这条规则是对档形变体的兜底。
func TestFindPrincipalAreaPrefersNormalMarkerAfterBoundaryGate(t *testing.T) {
	table := loadPrincipalAreaFixture(t, "62 `waiting_first.twn`", map[string]string{
		"town/waiting_first.twn": strings.Join([]string{
			"[area]", "0 `Town/Example/Gate_Example.map`", "`[gate]` 1 2", "[/area]",
			"[area]", "1 `Town/Example/Example_Waiting.map`", "`[dungeon gate]` 7", "[/area]",
			"[area]", "2 `Town/Example/Example_Main.map`", "`[normal]`", "[/area]",
			"[name]", "`Example`",
		}, "\n"),
	})
	area, found := table.FindPrincipalArea(62)
	if !found || area.ID != 2 || area.MapPath != "Town/Example/Example_Main.map" {
		t.Fatalf("principal=(%+v,%t) want area 2 Example_Main.map", area, found)
	}
}

// 整座城镇只有一张边界图（真档里不存在：15 个边界图城镇每个都还有非边界图区域）时，
// FindPrincipalArea 原样返回 area 0 —— 调用方据此逐字节回退到改动前的选择。
func TestFindPrincipalAreaKeepsAreaZeroWhenTownHasOnlyBoundaryArea(t *testing.T) {
	table := loadPrincipalAreaFixture(t, "63 `gate_only.twn`", map[string]string{
		"town/gate_only.twn": strings.Join([]string{
			"[area]", "0 `Town/Example/Gate_Example.map`", "`[gate]` 10 20", "[/area]",
			"[name]", "`Example`",
		}, "\n"),
	})
	area, found := table.FindPrincipalArea(63)
	if !found || area.ID != 0 || area.MapPath != "Town/Example/Gate_Example.map" {
		t.Fatalf("principal=(%+v,%t) want the unchanged area 0 fallback", area, found)
	}
	if _, found := table.FindPrincipalArea(99); found {
		t.Fatal("unknown town must not resolve a principal area")
	}
}
