package town

import (
	"context"
	"strings"
	"testing"
)

// 运行时 PVF 的 town 文档里，`[normal]` 标记后面可以直接跟两个整数：那不是 UI 数据
// （`[minimap rect]` 才是），而是该区域自己声明的默认落点。证据是真实文档
// town/180201_Wildforest.twn 的 area 0：
//
//	`[minimap rect]` 258 149 271 157 `[/minimap rect]` `[normal]` 594 234
//
// 登录落点解析只在区域没有 `[gate]` 时才用它，所以这里同时钉住"只有紧邻的两个整数
// 才算落点"，避免把后面的其它整数误读成坐标。
func TestParseTownAreaDeclaredDefaultPosition(t *testing.T) {
	source := memorySource{
		"town/town.lst": "110 `hill_town.twn` 108 `wildforest.twn` 43 `stormpass.twn`",
		"town/hill_town.twn": strings.Join([]string{
			"[area]",
			"0 `Cataclysm/Town/StormPass/StormPass.map`",
			"`[minimap rect]` 122 106 209 145 `[/minimap rect]` `[normal]`",
			"[/area]",
			"[name]",
			"`StormPass`",
		}, "\n"),
		"town/wildforest.twn": strings.Join([]string{
			"[area]",
			"0 `GContents/180201_Wildforest/Town/180201_Wildforest_00.map`",
			"`[minimap rect]` 258 149 271 157 `[/minimap rect]` `[normal]` 594 234",
			"[/area]",
			"[name]",
			"`WildForest`",
		}, "\n"),
		"town/stormpass.twn": strings.Join([]string{
			"[area]",
			"1 `Cataclysm/Town/StormPass/D_StormPass.map`",
			"`[minimap rect]` 83 98 119 122 `[/minimap rect]` `[dungeon gate]` 6",
			"[/area]",
			"[name]",
			"`StormPass`",
		}, "\n"),
	}
	table, err := Load(context.Background(), source, Options{})
	if err != nil {
		t.Fatal(err)
	}

	area, ok := table.FindArea(110, 0)
	if !ok || area.DefaultPosition != nil {
		t.Fatalf("`[normal]` without coordinates must not declare a default position=(%+v,%t)", area, ok)
	}
	if area.Kind != "normal" {
		t.Fatalf("`[normal]` without coordinates kind=%q", area.Kind)
	}

	area, ok = table.FindArea(108, 0)
	if !ok || area.DefaultPosition == nil || area.DefaultPosition.X != 594 || area.DefaultPosition.Y != 234 {
		t.Fatalf("`[normal]` declared default position=(%+v,%t)", area, ok)
	}
	if area.Gate != nil {
		t.Fatalf("`[normal]` area must not become a gate area=%+v", area)
	}

	// `[dungeon gate]` 后面的整数不是"默认落点"：标记与整数之间还有其它 token，
	// 只有紧邻标记的两个整数才被接受。
	area, ok = table.FindArea(43, 1)
	if !ok || area.DefaultPosition != nil || area.DungeonGate == nil || *area.DungeonGate != 6 {
		t.Fatalf("`[dungeon gate]` area=(%+v,%t)", area, ok)
	}

	// 克隆必须深拷贝默认落点，避免调用方改到共享表。
	area.DefaultPosition = &Gate{X: 1, Y: 2}
	again, ok := table.FindArea(108, 0)
	if !ok || again.DefaultPosition == nil || again.DefaultPosition.X != 594 {
		t.Fatalf("default position clone leaked mutation=(%+v,%t)", again, ok)
	}
}
