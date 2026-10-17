package dnfbridge

// op271（装备解放）解析器的假 PVF 单元用例。只覆盖请求 19 字节布局与槽位边界，
// 以及规则文档 [emancipate]..[/emancipate] 严格嵌套块的形状拒绝和 17/20 票种匹配。
// 不触碰交易、仓储、真实 PVF；PVF 文本里的反引号一律写成 \x60 转义。

import (
	"encoding/binary"
	"math"
	"reflect"
	"strings"
	"testing"

	dnfexpertjob "longheng.io/server/internal/modules/dnf/expertjob"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
)

// equipmentEmancipateParserTestRequest 按当前 EXE 的 19 字节布局拼请求体：
// 0:ticket slot(u16) 2:ticket id(u32) 6:target slot(u16) 8:target id(u32)
// 12:不透明字节 13:material slot(u16) 15:material id(u32)。
func equipmentEmancipateParserTestRequest(ticketSlot int16, ticketID uint32, targetSlot int16, targetID uint32, materialSlot int16, materialID uint32) []byte {
	body := make([]byte, 19)
	binary.LittleEndian.PutUint16(body[0:], uint16(ticketSlot))
	binary.LittleEndian.PutUint32(body[2:], ticketID)
	binary.LittleEndian.PutUint16(body[6:], uint16(targetSlot))
	binary.LittleEndian.PutUint32(body[8:], targetID)
	body[12] = 0x5a
	binary.LittleEndian.PutUint16(body[13:], uint16(materialSlot))
	binary.LittleEndian.PutUint32(body[15:], materialID)
	return body
}

func equipmentEmancipateParserTestTicketDoc(typ string) string {
	return "[emancipate ticket]\n" + typ + "\n"
}

func equipmentEmancipateParserTestSourceDoc(typ, input, output string) string {
	return "[emancipate]\n" +
		"[input]\n" + input + "\n[/input]\n" +
		"[output]\n" + output + "\n[/output]\n" +
		"[emancipate type]\n" + typ + "\n[/emancipate]\n"
}

func equipmentEmancipateParserTestParseRule(t *testing.T, ticketText, sourceText string) (currentEquipmentEmancipateRule, error) {
	t.Helper()
	ticket, err := dnfpvf.Parse("stackable/material/emancipate_book.stk", ticketText)
	if err != nil {
		t.Fatalf("ticket document parse: %v", err)
	}
	source, err := dnfpvf.Parse("equipment/weapon/emancipate_test.equ", sourceText)
	if err != nil {
		t.Fatalf("source document parse: %v", err)
	}
	return parseCurrentEquipmentEmancipateRule(ticket, source)
}

func TestEquipmentEmancipateParserRequestLayout(t *testing.T) {
	valid := equipmentEmancipateParserTestRequest(5, 0x0a0b0c0d, 40, 0x11223344, 12, 0x55667788)
	got, err := parseCurrentEquipmentEmancipateRequest(valid)
	if err != nil {
		t.Fatalf("19-byte valid body rejected: %v", err)
	}
	want := currentEquipmentEmancipateRequest{
		TicketSlot: 5, TicketID: 0x0a0b0c0d,
		TargetSlot: 40, TargetID: 0x11223344,
		MaterialSlot: 12, MaterialID: 0x55667788,
	}
	if got != want {
		t.Fatalf("decoded request = %+v, want %+v", got, want)
	}
	if err := got.validate(); err != nil {
		t.Fatalf("decoded request validate = %v, want nil", err)
	}

	// 长度必须恰好 19 字节：0/18/20 一律拒绝。只要求 err 非 nil，
	// 不规定被拒时返回的是零值还是已解码结构。
	for _, size := range []int{0, 18, 20} {
		body := append([]byte(nil), valid...)
		if size <= len(valid) {
			body = body[:size]
		} else {
			body = append(body, 0x00)
		}
		decoded, err := parseCurrentEquipmentEmancipateRequest(body)
		if err == nil {
			t.Fatalf("length %d: decoded=%+v err=nil, want rejection", len(body), decoded)
		}
	}

	// 第 12 字节在当前 EXE 中不透明：改写它不得影响解码结果。
	rewritten := append([]byte(nil), valid...)
	rewritten[12] = 0x00
	other, err := parseCurrentEquipmentEmancipateRequest(rewritten)
	if err != nil || other != got {
		t.Fatalf("opaque byte 12 changed decode: %+v err=%v", other, err)
	}

	boundaries := []struct {
		name string
		body []byte
		ok   bool
	}{
		{"ticket slot 2 below 3", equipmentEmancipateParserTestRequest(2, 1000, 9, 2000, 4, 3000), false},
		{"ticket slot 3 minimum", equipmentEmancipateParserTestRequest(3, 1000, 9, 2000, 4, 3000), true},
		{"ticket slot 353", equipmentEmancipateParserTestRequest(353, 1000, 9, 2000, 4, 3000), true},
		{"ticket slot 354 account shared", equipmentEmancipateParserTestRequest(354, 1000, 9, 2000, 4, 3000), false},
		{"ticket slot 365 account shared", equipmentEmancipateParserTestRequest(365, 1000, 9, 2000, 4, 3000), false},
		{"ticket slot 366", equipmentEmancipateParserTestRequest(366, 1000, 9, 2000, 4, 3000), true},
		{"target slot 8 below 9", equipmentEmancipateParserTestRequest(3, 1000, 8, 2000, 4, 3000), false},
		{"target slot 9 minimum", equipmentEmancipateParserTestRequest(3, 1000, 9, 2000, 4, 3000), true},
		{"target slot 64 maximum", equipmentEmancipateParserTestRequest(3, 1000, 64, 2000, 4, 3000), true},
		{"target slot 65 above 64", equipmentEmancipateParserTestRequest(3, 1000, 65, 2000, 4, 3000), false},
		{"ticket slot equals target slot", equipmentEmancipateParserTestRequest(9, 1000, 9, 2000, 4, 3000), false},
		{"ticket id zero", equipmentEmancipateParserTestRequest(3, 0, 9, 2000, 4, 3000), false},
		{"target id zero", equipmentEmancipateParserTestRequest(3, 1000, 9, 0, 4, 3000), false},
		{"material slot 2 below 3", equipmentEmancipateParserTestRequest(3, 1000, 9, 2000, 2, 3000), false},
		{"material slot equals target", equipmentEmancipateParserTestRequest(3, 1000, 9, 2000, 9, 3000), false},
		{"material slot equals ticket", equipmentEmancipateParserTestRequest(3, 1000, 9, 2000, 3, 3000), false},
		{"material id zero", equipmentEmancipateParserTestRequest(3, 1000, 9, 2000, 4, 0), false},
		{"optional material hint -1/0xffff", equipmentEmancipateParserTestRequest(3, 1000, 9, 2000, -1, math.MaxUint16), true},
		{"optional hint slot without 0xffff id", equipmentEmancipateParserTestRequest(3, 1000, 9, 2000, -1, 3000), false},
		{"optional hint id above 0xffff", equipmentEmancipateParserTestRequest(3, 1000, 9, 2000, -1, math.MaxUint16+1), false},
		{"material id 0xffff with real slot", equipmentEmancipateParserTestRequest(3, 1000, 9, 2000, 4, math.MaxUint16), true},
	}
	for _, tc := range boundaries {
		// 拒绝分支只要求 err 非 nil；被拒返回的结构体内容不作断言。
		decoded, err := parseCurrentEquipmentEmancipateRequest(tc.body)
		if tc.ok {
			if err != nil {
				t.Fatalf("%s: rejected: %v", tc.name, err)
			}
			if err := decoded.validate(); err != nil {
				t.Fatalf("%s: validate = %v", tc.name, err)
			}
			continue
		}
		if err == nil {
			t.Fatalf("%s: decoded=%+v err=nil, want rejection", tc.name, decoded)
		}
	}
}

func TestEquipmentEmancipateParserRuleNestedBlockShape(t *testing.T) {
	ticket17 := equipmentEmancipateParserTestTicketDoc("17")
	source17 := equipmentEmancipateParserTestSourceDoc("17", "900 2 901 1", "1500 1")
	replace := func(source, old, new string) string { return strings.Replace(source, old, new, 1) }

	// 支持形状：票 17 + 内嵌块 type 17；材料按 item id 升序归并，产出 count 必须为 1。
	rule, err := equipmentEmancipateParserTestParseRule(t, ticket17, equipmentEmancipateParserTestSourceDoc("17", "901 1 900 2", "1500 1"))
	if err != nil {
		t.Fatalf("supported 17 shape rejected: %v", err)
	}
	want := currentEquipmentEmancipateRule{
		TicketType: 17,
		OutputID:   1500,
		Materials:  []dnfexpertjob.RecipeEntry{{ItemID: 900, Count: 2}, {ItemID: 901, Count: 1}},
	}
	if !reflect.DeepEqual(rule, want) {
		t.Fatalf("rule = %+v, want %+v", rule, want)
	}

	// 票 20 只与内嵌块 type 20 配对。
	rule20, err := equipmentEmancipateParserTestParseRule(t, equipmentEmancipateParserTestTicketDoc("20"), equipmentEmancipateParserTestSourceDoc("20", "900 2", "1500 1"))
	if err != nil {
		t.Fatalf("supported 20 shape rejected: %v", err)
	}
	want20 := currentEquipmentEmancipateRule{TicketType: 20, OutputID: 1500, Materials: []dnfexpertjob.RecipeEntry{{ItemID: 900, Count: 2}}}
	if !reflect.DeepEqual(rule20, want20) {
		t.Fatalf("rule20 = %+v, want %+v", rule20, want20)
	}

	// 同一 id 重复出现要累加成一格材料，不得各自成行。
	merged, err := equipmentEmancipateParserTestParseRule(t, ticket17, equipmentEmancipateParserTestSourceDoc("17", "900 2 901 1 900 3", "1500 1"))
	if err != nil {
		t.Fatalf("merged material shape rejected: %v", err)
	}
	if !reflect.DeepEqual(merged.Materials, []dnfexpertjob.RecipeEntry{{ItemID: 900, Count: 5}, {ItemID: 901, Count: 1}}) {
		t.Fatalf("merged materials = %+v, want [{900 5} {901 1}]", merged.Materials)
	}

	// 内嵌块是唯一来源：文档里别的功能再写全局 [input]/[output] 也不得被当成本规则。
	scopedSource := "[name] \x60practice\x60\n[input]\n5 5\n[/input]\n" + source17
	scoped, err := equipmentEmancipateParserTestParseRule(t, ticket17, scopedSource)
	if err != nil || !reflect.DeepEqual(scoped.Materials, want.Materials) || scoped.OutputID != 1500 {
		t.Fatalf("nested block not authoritative: %+v err=%v", scoped, err)
	}

	rejects := []struct {
		name   string
		ticket string
		source string
	}{
		{"ticket type 18", equipmentEmancipateParserTestTicketDoc("18"), source17},
		{"ticket type text", equipmentEmancipateParserTestTicketDoc("\x60[emancipate]\x60"), source17},
		{"ticket type two ints", equipmentEmancipateParserTestTicketDoc("17 17"), source17},
		{"ticket section missing", "[name] \x60book\x60\n", source17},
		{"duplicate ticket section", equipmentEmancipateParserTestTicketDoc("17") + equipmentEmancipateParserTestTicketDoc("17"), source17},
		{"ticket grade max present", ticket17 + "[emancipate grade max]\n0\n", source17},
		{"ticket amplify max present", ticket17 + "[emancipate amplify max]\n0\n", source17},
		{"ticket genuine grade max present", ticket17 + "[emancipate genuineGrade max]\n0\n", source17},
		{"ticket equipment level present", ticket17 + "[equipment level emancipate]\n0\n", source17},
		{"block lives in ticket document", ticket17 + source17, "[name] \x60practice\x60\n"},
		{"open tag carries value", ticket17, replace(source17, "[emancipate]\n", "[emancipate] 7\n")},
		{"close tag carries value", ticket17, replace(source17, "[/emancipate]\n", "[/emancipate] 1\n")},
		{"duplicate open tag", ticket17, replace(source17, "[emancipate]\n", "[emancipate]\n[emancipate]\n")},
		{"duplicate close tag", ticket17, replace(source17, "[/emancipate]\n", "[/emancipate]\n[/emancipate]\n")},
		{"close tag missing", ticket17, replace(source17, "[/emancipate]\n", "")},
		{"close input missing", ticket17, replace(source17, "[/input]\n", "")},
		{"close input carries value", ticket17, replace(source17, "[/input]\n", "[/input] 1\n")},
		{"extra section inside block", ticket17, replace(source17, "[emancipate type]\n", "[name] \x60x\x60\n[emancipate type]\n")},
		{"input after output", ticket17, "[emancipate]\n[output]\n1500 1\n[/output]\n[input]\n900 2\n[/input]\n[emancipate type]\n17\n[/emancipate]\n"},
		{"emancipate type section missing", ticket17, replace(source17, "[emancipate type]\n", "[name]\n")},
		{"emancipate type two ints", ticket17, replace(source17, "[/output]\n[emancipate type]\n17\n", "[/output]\n[emancipate type]\n17 18\n")},
		{"block type 20 for ticket 17", ticket17, equipmentEmancipateParserTestSourceDoc("20", "900 2 901 1", "1500 1")},
		{"block type 17 for ticket 20", equipmentEmancipateParserTestTicketDoc("20"), source17},
		{"global input only", ticket17, "[input]\n900 2 901 1\n[/input]\n" + replace(source17, "900 2 901 1\n", "")},
		{"nested input empty", ticket17, "[input]\n900 2\n[/input]\n" + replace(source17, "900 2 901 1\n", "")},
		{"input odd token count", ticket17, equipmentEmancipateParserTestSourceDoc("17", "900 2 901", "1500 1")},
		{"input non-int count", ticket17, equipmentEmancipateParserTestSourceDoc("17", "900 \x60two\x60", "1500 1")},
		{"input id zero", ticket17, equipmentEmancipateParserTestSourceDoc("17", "0 2", "1500 1")},
		{"input negative count", ticket17, equipmentEmancipateParserTestSourceDoc("17", "900 -1", "1500 1")},
		{"input id above uint32", ticket17, equipmentEmancipateParserTestSourceDoc("17", "4294967296 1", "1500 1")},
		{"output count two", ticket17, equipmentEmancipateParserTestSourceDoc("17", "900 2", "1500 2")},
		{"output count zero", ticket17, equipmentEmancipateParserTestSourceDoc("17", "900 2", "1500 0")},
		{"output single token", ticket17, equipmentEmancipateParserTestSourceDoc("17", "900 2", "1500")},
		{"output three tokens", ticket17, equipmentEmancipateParserTestSourceDoc("17", "900 2", "1500 1 2")},
		{"output id zero", ticket17, equipmentEmancipateParserTestSourceDoc("17", "900 2", "0 1")},
		{"output id above uint32", ticket17, equipmentEmancipateParserTestSourceDoc("17", "900 2", "4294967296 1")},
	}
	for _, tc := range rejects {
		if _, err := equipmentEmancipateParserTestParseRule(t, tc.ticket, tc.source); err == nil {
			t.Fatalf("%s: err=nil, want rejection", tc.name)
		}
	}
}
