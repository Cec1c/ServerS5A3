package worldmap

import (
	"testing"
)

// pvfBacktick 让测试文档里的 PVF 引号字符串不与 Go 原始字符串定界符冲突。
const pvfBacktick = "\x60"

// 厂商语义（A21 参考只读核对 DfoGmTool-src/PvfLib/Models/DungeonFile.cs:570-571）：
//
//	case "tower of despair":
//	    dgn.TowerOfDespair = string.IsNullOrWhiteSpace(data) ? 1 : ParseInt(data);
//
// 真档 dungeon/towers/ztowerofdespair001.dgn 带的是"空标记"（段内没有任何值），
// 解析后必须保留塔身份 IntegerValues["tower of despair"]=1；显式数字沿现行为；
// 非整数内容仍然报缺值诊断；这条特例不得扩散到其它整数段。

func TestTowerOfDespairEmptyMarkerTakesVendorDefault(t *testing.T) {
	doc := parseTestDocument(t, "dungeon/towers/ztowerofdespair001.dgn", "[name]\n"+
		pvfBacktick+"Tower Of Despair Floor 1"+pvfBacktick+"\n"+
		"[tower of despair]\n"+
		"[maze info]\n[size]\n1 1\n[greed]\n"+pvfBacktick+"BB"+pvfBacktick+"\n"+
		"[start map]\n0 0\n[boss map]\n0 0\n")
	dungeon := ParseDungeon(11008, "dungeon/towers/ztowerofdespair001.dgn", doc)

	value, ok := dungeon.Metadata.IntegerValues[towerOfDespairSectionKey]
	if !ok || value != 1 {
		t.Fatalf("empty [tower of despair] marker = %v ok=%v, want 1 (integer values=%+v)", value, ok, dungeon.Metadata.IntegerValues)
	}
	for _, diag := range dungeon.Diagnostics {
		if sectionKey(diag.Section) == towerOfDespairSectionKey {
			t.Fatalf("empty marker must not be diagnosed: %+v", diag)
		}
	}
	if dungeon.Metadata.Name != "Tower Of Despair Floor 1" {
		t.Fatalf("neighbour [name] section changed: %q", dungeon.Metadata.Name)
	}
	if len(dungeon.Metadata.IntegerValues) != 1 {
		t.Fatalf("empty marker added unrelated integer values: %+v", dungeon.Metadata.IntegerValues)
	}
	if len(dungeon.Mazes) != 1 || len(dungeon.OpaqueSections) != 0 {
		t.Fatalf("empty marker changed section ownership: mazes=%d opaque=%+v unknown=%+v extensions=%+v",
			len(dungeon.Mazes), dungeon.OpaqueSections, dungeon.UnknownSections, dungeon.Extensions)
	}
	for _, section := range append(append([]RawSection(nil), dungeon.UnknownSections...), dungeon.OpaqueSections...) {
		if sectionKey(section.Name) == towerOfDespairSectionKey {
			t.Fatalf("tower marker leaked into raw buckets: %+v", section)
		}
	}
}

func TestTowerOfDespairExplicitValuesPreserved(t *testing.T) {
	cases := []struct {
		name string
		body string
		want int64
	}{
		{name: "explicit zero", body: "[tower of despair]\n0\n", want: 0},
		{name: "explicit one", body: "[tower of despair]\n1\n", want: 1},
		{name: "explicit two", body: "[tower of despair]\n2\n", want: 2},
	}
	for _, testCase := range cases {
		doc := parseTestDocument(t, "dungeon/towers/explicit.dgn", testCase.body)
		dungeon := ParseDungeon(11008, "dungeon/towers/explicit.dgn", doc)
		value, ok := dungeon.Metadata.IntegerValues[towerOfDespairSectionKey]
		if !ok || value != testCase.want {
			t.Errorf("%s: value=%v ok=%v, want %d (values=%+v)", testCase.name, value, ok, testCase.want, dungeon.Metadata.IntegerValues)
		}
		if len(dungeon.Diagnostics) != 0 {
			t.Errorf("%s: unexpected diagnostics %+v", testCase.name, dungeon.Diagnostics)
		}
	}
}

func TestTowerOfDespairAbsentMarkerIsNotInvented(t *testing.T) {
	doc := parseTestDocument(t, "dungeon/plain.dgn", "[name]\n`Plain`\n[fatigue]\n8\n")
	dungeon := ParseDungeon(11009, "dungeon/plain.dgn", doc)
	if _, ok := dungeon.Metadata.IntegerValues[towerOfDespairSectionKey]; ok {
		t.Fatalf("dungeon without the section gained a tower marker: %+v", dungeon.Metadata.IntegerValues)
	}
	if dungeon.Metadata.IntegerValues["fatigue"] != 8 {
		t.Fatalf("existing integer metadata changed: %+v", dungeon.Metadata.IntegerValues)
	}
	for _, diag := range dungeon.Diagnostics {
		if sectionKey(diag.Section) == towerOfDespairSectionKey {
			t.Fatalf("absent marker produced a diagnostic: %+v", diag)
		}
	}
}

func TestTowerOfDespairNonNumericStillDiagnoses(t *testing.T) {
	doc := parseTestDocument(t, "dungeon/towers/bad.dgn", "[tower of despair]\n`auto`\n")
	dungeon := ParseDungeon(11008, "dungeon/towers/bad.dgn", doc)
	if _, ok := dungeon.Metadata.IntegerValues[towerOfDespairSectionKey]; ok {
		t.Fatalf("non-numeric value accepted as integer: %+v", dungeon.Metadata.IntegerValues)
	}
	if len(dungeon.Diagnostics) != 1 || sectionKey(dungeon.Diagnostics[0].Section) != towerOfDespairSectionKey ||
		dungeon.Diagnostics[0].Message != "expected integer metadata value" {
		t.Fatalf("non-numeric tower marker diagnostics = %+v", dungeon.Diagnostics)
	}
}

func TestTowerOfDespairEmptyMarkerDoesNotDefaultOtherIntegerSections(t *testing.T) {
	doc := parseTestDocument(t, "dungeon/towers/scope.dgn", "[tower of despair]\n[fatigue]\n[fatigue result]\n4\n")
	dungeon := ParseDungeon(11008, "dungeon/towers/scope.dgn", doc)
	if value, ok := dungeon.Metadata.IntegerValues[towerOfDespairSectionKey]; !ok || value != 1 {
		t.Fatalf("tower of despair marker = %v ok=%v, want 1", value, ok)
	}
	if value, ok := dungeon.Metadata.IntegerValues["fatigue"]; ok {
		t.Fatalf("empty [fatigue] was defaulted to %v; only [tower of despair] may take the vendor default", value)
	}
	if dungeon.Metadata.IntegerValues["fatigue result"] != 4 {
		t.Fatalf("following [fatigue result] section polluted: %+v", dungeon.Metadata.IntegerValues)
	}
	if len(dungeon.Metadata.IntegerValues) != 2 {
		t.Fatalf("unexpected integer values: %+v", dungeon.Metadata.IntegerValues)
	}
	if len(dungeon.Diagnostics) != 1 || sectionKey(dungeon.Diagnostics[0].Section) != "fatigue" {
		t.Fatalf("neighbour empty integer section diagnostics = %+v", dungeon.Diagnostics)
	}
}

// 符号 != 空白：段里只有符号（如 `{ }`）时不算空标记，不得默认成 1。
func TestTowerOfDespairSymbolOnlySectionIsNotAnEmptyMarker(t *testing.T) {
	doc := parseTestDocument(t, "dungeon/towers/symbols.dgn", "[tower of despair]\n{ }\n")
	dungeon := ParseDungeon(11008, "dungeon/towers/symbols.dgn", doc)
	if _, ok := dungeon.Metadata.IntegerValues[towerOfDespairSectionKey]; ok {
		t.Fatalf("symbol-only marker was defaulted to 1: %+v", dungeon.Metadata.IntegerValues)
	}
	if len(dungeon.Diagnostics) != 1 || sectionKey(dungeon.Diagnostics[0].Section) != towerOfDespairSectionKey ||
		dungeon.Diagnostics[0].Message != "expected integer metadata value" {
		t.Fatalf("symbol-only tower marker diagnostics = %+v", dungeon.Diagnostics)
	}
	tokens := 0
	for _, section := range dungeon.SourceSections {
		if sectionKey(section.Name) == towerOfDespairSectionKey {
			tokens = len(section.Tokens)
		}
	}
	if tokens == 0 {
		t.Fatalf("fixture carried no symbol tokens; the empty-marker guard would be vacuous")
	}
}
