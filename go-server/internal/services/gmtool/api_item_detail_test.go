package gmtool

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfquest "longheng.io/server/internal/modules/dnf/quest"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

// P1 只读查询面的测试台：一个内存仓储 + 一份假 PVF + 一份预置物品索引。
// 三个新页面（物品详情 / 邮箱 / 任务）共用它。

type readonlyTestSource map[string]string

func (s readonlyTestSource) ReadText(relativePath string) (string, error) {
	if text, ok := s[relativePath]; ok {
		return text, nil
	}
	return "", &readonlyTestMissingPath{path: relativePath}
}

type readonlyTestMissingPath struct{ path string }

func (e *readonlyTestMissingPath) Error() string { return "missing test PVF path: " + e.path }

type readonlyFixture struct {
	source    readonlyTestSource
	character dnfrepo.CharacterRecord
	quests    *dnfrepo.QuestRecord
	mailbox   *dnfrepo.MailboxRecord
	itemLists []string
	skipItems bool
	withQuest bool
	// pvf 非空时覆盖 source：真实 Script.pvf 的用例（platformpvf.Archive 本身
	// 就实现 dnfpvf.Source）。
	pvf dnfpvf.Source
	// entries 非空时直接作为物品索引内容，不再遍历 .lst（真档 5.4 万条太慢）。
	entries []itemEntry
}

type readonlyHarness struct {
	server *Server
	group  dnfrepo.Group
	source dnfpvf.Source
}

func newReadonlyHarness(t *testing.T, fixture readonlyFixture) *readonlyHarness {
	t.Helper()
	var pvfSource dnfpvf.Source = fixture.source
	if pvfSource == nil {
		pvfSource = readonlyTestSource{}
	}
	if fixture.pvf != nil {
		pvfSource = fixture.pvf
	}
	// 先把 PVF 列表建一遍：列表里引用不到的条目不会进索引，测试台也要一样严格。
	lists := fixture.itemLists
	if lists == nil {
		lists = []string{itemListStackable, itemListEquipment}
	}
	if fixture.withQuest {
		if _, err := dnfpvf.Build(context.Background(), pvfSource, dnfpvf.BuildOptions{Lists: []string{dnfquest.DefaultList}}); err != nil {
			t.Fatalf("test quest PVF is not loadable: %v", err)
		}
	}

	group := memory.NewMemoryGroup()
	character := fixture.character
	character.CharacterID = "1"
	if err := group.Character.Save(context.Background(), character); err != nil {
		t.Fatalf("save character: %v", err)
	}
	if fixture.quests != nil {
		record := *fixture.quests
		record.CharacterID = "1"
		if err := group.Quest.Save(context.Background(), record); err != nil {
			t.Fatalf("save quests: %v", err)
		}
	}
	if fixture.mailbox != nil {
		record := *fixture.mailbox
		record.CharacterID = "1"
		if err := group.Mailbox.Save(context.Background(), record); err != nil {
			t.Fatalf("save mailbox: %v", err)
		}
	}

	server := &Server{
		cfg: Config{Logger: func(string, ...any) {}},
		deps: Deps{
			Repos: func() (dnfrepo.Group, bool) { return group, true },
			PVF:   func() (dnfpvf.Source, error) { return pvfSource, nil },
		},
		pvf:      newPVFCache(),
		sessions: map[string]time.Time{},
	}
	entries := append([]itemEntry(nil), fixture.entries...)
	if !fixture.skipItems && len(entries) == 0 {
		refs := make(map[uint32]itemEntry, 8)
		for _, list := range lists {
			kind := "stackable"
			if list == itemListEquipment {
				kind = "equipment"
			}
			if err := collectItemRefs(pvfSource, list, kind, refs); err != nil {
				t.Fatalf("collect %s: %v", list, err)
			}
		}
		for _, entry := range refs {
			item := entry
			parseItemEntry(pvfSource, &item)
			if strings.TrimSpace(item.Name) == "" {
				continue
			}
			entries = append(entries, item)
		}
	}
	index := newItemIndex("", server, server.cfg.Logger)
	if !fixture.skipItems {
		index.mu.Lock()
		index.setEntriesLocked(entries)
		index.mu.Unlock()
	}
	server.items = index

	return &readonlyHarness{server: server, group: group, source: pvfSource}
}

// get 打一个带路径变量的 GET，返回 HTTP 状态与解码后的 JSON。
func (h *readonlyHarness) get(t *testing.T, target string, pathValues map[string]string, handler http.HandlerFunc) (int, map[string]any) {
	t.Helper()
	recorder := h.getRaw(t, target, pathValues, handler)
	response := map[string]any{}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response %q: %v", recorder.Body.String(), err)
	}
	return recorder.Code, response
}

func (h *readonlyHarness) getRaw(t *testing.T, target string, pathValues map[string]string, handler http.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, target, nil)
	for key, value := range pathValues {
		request.SetPathValue(key, value)
	}
	recorder := httptest.NewRecorder()
	handler(recorder, request)
	return recorder
}

func requireOK(t *testing.T, status int, response map[string]any) map[string]any {
	t.Helper()
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (本仓惯例：业务失败也是 200 + success:false)", status)
	}
	if response["success"] != true {
		t.Fatalf("response = %+v, want success:true", response)
	}
	return response
}

func requireFailure(t *testing.T, status int, response map[string]any) string {
	t.Helper()
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (本仓惯例：业务失败也是 200 + success:false)", status)
	}
	if response["success"] != false {
		t.Fatalf("response = %+v, want success:false", response)
	}
	message, _ := response["error"].(string)
	if strings.TrimSpace(message) == "" {
		t.Fatalf("failure response carries no error message: %+v", response)
	}
	return message
}

func responseMap(t *testing.T, parent map[string]any, key string) map[string]any {
	t.Helper()
	value, ok := parent[key].(map[string]any)
	if !ok {
		t.Fatalf("response[%q] is not an object: %+v", key, parent)
	}
	return value
}

func responseList(t *testing.T, parent map[string]any, key string) []any {
	t.Helper()
	value, ok := parent[key].([]any)
	if !ok {
		t.Fatalf("response[%q] is not a list: %+v", key, parent)
	}
	return value
}

func itemDetailHarness(t *testing.T, source readonlyTestSource) *readonlyHarness {
	t.Helper()
	return newReadonlyHarness(t, readonlyFixture{
		source: source,
		character: dnfrepo.CharacterRecord{
			AccountID: "acc-1",
			Name:      "tester",
			Job:       "11",
			Level:     90,
			Stats:     map[string]int64{},
		},
	})
}

// readonlyTestStackable 的段集合来自真档取证：`[explain]` / `[flavor text]` /
// `[weight]` / `[icon]`（路径 + 序号）/ `[attach type]` / `[price]` 都存在，
// 而 `[basic explain]` / `[detail explain]` / `[repair price]` / `[value]` /
// `[part set index]` 在 stackable 侧实测为 0 命中 —— 这里刻意都不写。
const readonlyTestStackable = "[name]\n`测试消耗品` \n" +
	"[explain]\n`说明文字 100%% 加成` \n" +
	"[flavor text]\n`风味文字` \n" +
	"[grade]\n1 \n[rarity]\n2 \n[weight]\n7 \n" +
	"[icon]\n`Item/stackable/consumption.img` 2066 \n" +
	"[attach type]\n`[account]` \n" +
	"[stackable type]\n`[material]` 0 \n" +
	"[price]\n1234 \n"

// readonlyTestWeapon 带 `[part set index]`，用来验证套装的两级降级。
const readonlyTestWeapon = "[name]\n`测试套装武器` \n" +
	"[grade]\n3 \n[rarity]\n4 \n" +
	"[equipment type]\n`[weapon]` \n" +
	"[minimum level]\n50 \n" +
	"[weight]\n12 \n[value]\n5000 \n[repair price]\n800 \n" +
	"[durability]\n45 \n" +
	"[attack speed]\n60 \n" +
	"[elemental property]\n`[fire element]` \n`[water element]` \n" +
	"[equipment physical attack]\n100 \n" +
	"[add equipment physical attack]\n20 \n" +
	"[item group name]\n`测试武器组` \n" +
	"[part set index]\n10 \n" +
	"[icon]\n`Item/equipment/weapon.img` 11 \n" +
	"[usable job]\n`[swordman]` \n"

const readonlyTestPartSetList = "[equipment part set]\n" +
	"10 `character/partset/testset.equ` `测试套装  名` `测试帽子` `[hat avatar]` 3 0 \n" +
	"[/equipment part set]\n"

const readonlyTestPartSetFile = "[piece set ability]\n3 \n" +
	"[parameter basic explain]\n`3件套 力量 +30%%` \n" +
	"[/piece set ability]\n\n" +
	"[piece set ability]\n6 \n" +
	"[parameter detail explain]\n`6件套 详细说明` \n" +
	"[equipment physical attack]\n50 \n" +
	"[/piece set ability]\n"

func readonlyItemSource(weapon string, extra map[string]string) readonlyTestSource {
	source := readonlyTestSource{
		itemListStackable:     "1 `coin.stk`\n",
		itemListEquipment:     "2 `sword.equ`\n",
		"stackable/coin.stk":  readonlyTestStackable,
		"equipment/sword.equ": weapon,
	}
	for key, value := range extra {
		source[key] = value
	}
	return source
}

func TestItemDetailReadsPVFSectionsAndKeepsAbsentOnesEmpty(t *testing.T) {
	harness := itemDetailHarness(t, readonlyItemSource("[name]\n`无套装武器` \n[equipment type]\n`[weapon]` \n", nil))
	_, response := harness.get(t, "/api/items/1/detail", map[string]string{"itemId": "1"}, harness.server.handleItemDetail)
	requireOK(t, http.StatusOK, response)

	if response["explain"] != "说明文字 100% 加成" {
		t.Fatalf("explain = %q, want `%%` 已还原", response["explain"])
	}
	if response["flavorText"] != "风味文字" {
		t.Fatalf("flavorText = %q", response["flavorText"])
	}
	if response["weight"] != float64(7) {
		t.Fatalf("weight = %v, want 7", response["weight"])
	}
	// PVF 里没有的段必须留空/缺省，不许补默认文案、也不许补 0。
	for _, key := range []string{"basicExplain", "detailExplain"} {
		if response[key] != "" {
			t.Fatalf("%s = %v, want empty (PVF 未提供)", key, response[key])
		}
	}
	for _, key := range []string{"repairPrice", "value", "partSetIndex"} {
		if _, present := response[key]; present {
			t.Fatalf("%s = %v, want the field omitted (PVF 未提供)", key, response[key])
		}
	}
	if _, hasSet := response["equipmentSet"]; hasSet {
		t.Fatalf("a stackable item must not carry equipmentSet: %+v", response)
	}
	if _, hasQuality := response["qualityPercent"]; hasQuality {
		t.Fatalf("stackable item must not carry equipment-only fields: %+v", response)
	}

	item := responseMap(t, response, "item")
	if item["name"] != "测试消耗品" || item["kind"] != "stackable" {
		t.Fatalf("item = %+v", item)
	}
	if item["iconPath"] != "Item/stackable/consumption.img" || item["iconIndex"] != float64(2066) {
		t.Fatalf("icon = %v / %v, want the PVF [icon] pair", item["iconPath"], item["iconIndex"])
	}
	if item["attachType"] != "[account]" || item["price"] != float64(1234) {
		t.Fatalf("attachType/price = %v / %v", item["attachType"], item["price"])
	}
	if item["stackableType"] != "[material]" {
		t.Fatalf("stackableType = %v, want [material]", item["stackableType"])
	}
	if item["pvfPath"] != "stackable/coin.stk" {
		t.Fatalf("pvfPath = %v, want stackable/coin.stk", item["pvfPath"])
	}
}

func TestItemDetailEquipmentAttributesUseTopQualityProjection(t *testing.T) {
	// 列表里存在但**不含** setID 10：套装静默降级，同时验证装备属性。
	source := readonlyItemSource(readonlyTestWeapon, map[string]string{
		equipmentPartSetListPath: "[equipment part set]\n11 `character/partset/other.equ` `别的套装` \n[/equipment part set]\n",
	})
	harness := itemDetailHarness(t, source)
	_, response := harness.get(t, "/api/items/2/detail", map[string]string{"itemId": "2"}, harness.server.handleItemDetail)
	requireOK(t, http.StatusOK, response)

	if response["qualityPercent"] != float64(100) {
		t.Fatalf("qualityPercent = %v, want 100 (上游的满品级展示口径)", response["qualityPercent"])
	}
	// [attack speed] 60 -> fast（上游 gmEquipmentWeaponAttackSpeed 的阈值表）
	if response["weaponAttackSpeed"] != "fast" {
		t.Fatalf("weaponAttackSpeed = %v, want fast", response["weaponAttackSpeed"])
	}
	if response["itemGroupName"] != "测试武器组" || response["partSetIndex"] != float64(10) {
		t.Fatalf("group/partSetIndex = %v / %v", response["itemGroupName"], response["partSetIndex"])
	}
	properties := responseList(t, response, "elementalProperties")
	if len(properties) != 2 || properties[0] != "fire element" || properties[1] != "water element" {
		t.Fatalf("elementalProperties = %v", properties)
	}
	attributes := responseList(t, response, "attributes")
	found := map[string]float64{}
	for _, entry := range attributes {
		attribute := entry.(map[string]any)
		found[attribute["name"].(string)] = attribute["value"].(float64)
	}
	// 满品级投影：round(100 * 1.1) + 20 = 130
	if found["physical_attack"] != 130 {
		t.Fatalf("physical_attack = %v, want 130 (round(100*1.1)+20)", found["physical_attack"])
	}
	if _, exists := found["move_speed"]; exists {
		t.Fatalf("zero-valued attributes must be dropped: %v", found)
	}
	// 没有套装的武器（列表里查不到 setID）→ 静默无套装。
	if _, hasSet := response["equipmentSet"]; hasSet {
		t.Fatalf("missing set must degrade silently: %+v", response["equipmentSet"])
	}
}

func TestItemDetailEquipmentSetDegradation(t *testing.T) {
	// (1) 列表命中、被引用文件也在 -> 正常返回，加成按 [piece set ability] 解析。
	source := readonlyItemSource(readonlyTestWeapon, map[string]string{
		equipmentPartSetListPath:                  readonlyTestPartSetList,
		"equipment/character/partset/testset.equ": readonlyTestPartSetFile,
	})
	harness := itemDetailHarness(t, source)
	_, response := harness.get(t, "/api/items/2/detail", map[string]string{"itemId": "2"}, harness.server.handleItemDetail)
	requireOK(t, http.StatusOK, response)
	set := responseMap(t, response, "equipmentSet")
	if set["setId"] != float64(10) || set["name"] != "测试套装 名" {
		t.Fatalf("set = %+v", set)
	}
	if set["pvfPath"] != "equipment/character/partset/testset.equ" {
		t.Fatalf("set pvfPath = %v, want the equipment/-prefixed path", set["pvfPath"])
	}
	bonuses := responseList(t, set, "bonuses")
	if len(bonuses) != 2 {
		t.Fatalf("bonuses = %v, want 2 blocks", bonuses)
	}
	first := bonuses[0].(map[string]any)
	if first["requiredPieces"] != float64(3) || first["basicExplain"] != "3件套 力量 +30%" {
		t.Fatalf("first bonus = %+v", first)
	}
	second := bonuses[1].(map[string]any)
	if second["requiredPieces"] != float64(6) || second["detailExplain"] != "6件套 详细说明" {
		t.Fatalf("second bonus = %+v", second)
	}
	secondAttributes := second["attributes"].([]any)
	if len(secondAttributes) != 1 || secondAttributes[0].(map[string]any)["value"] != float64(50) {
		t.Fatalf("second bonus attributes = %v", secondAttributes)
	}

	// (2) setID 不在列表里 -> 静默无套装（不是错误）。
	source = readonlyItemSource(readonlyTestWeapon, map[string]string{
		equipmentPartSetListPath: "[equipment part set]\n11 `character/partset/other.equ` `别的套装` \n[/equipment part set]\n",
	})
	harness = itemDetailHarness(t, source)
	_, response = harness.get(t, "/api/items/2/detail", map[string]string{"itemId": "2"}, harness.server.handleItemDetail)
	requireOK(t, http.StatusOK, response)
	if _, hasSet := response["equipmentSet"]; hasSet {
		t.Fatalf("set id absent from the list must degrade silently: %+v", response["equipmentSet"])
	}

	// (3) 列表命中、被引用文件读失败 -> 报错（上游 gm.go:1658-1665 的口径）。
	source = readonlyItemSource(readonlyTestWeapon, map[string]string{
		equipmentPartSetListPath: readonlyTestPartSetList,
	})
	harness = itemDetailHarness(t, source)
	_, response = harness.get(t, "/api/items/2/detail", map[string]string{"itemId": "2"}, harness.server.handleItemDetail)
	message := requireFailure(t, http.StatusOK, response)
	if !strings.Contains(message, "测试套装") && !strings.Contains(message, "testset.equ") {
		t.Fatalf("set file read failure must name the file: %q", message)
	}

	// (4) 套装列表文件本身读失败 -> 报错。
	source = readonlyItemSource(readonlyTestWeapon, nil)
	harness = itemDetailHarness(t, source)
	_, response = harness.get(t, "/api/items/2/detail", map[string]string{"itemId": "2"}, harness.server.handleItemDetail)
	message = requireFailure(t, http.StatusOK, response)
	if !strings.Contains(message, equipmentPartSetListPath) {
		t.Fatalf("missing part-set list must be reported with its path: %q", message)
	}
}

func TestItemDetailWithoutPartSetIndexNeverReadsTheSetList(t *testing.T) {
	// part set index == 0 时**根本不该去碰** etc/equipmentpartset.etc：
	// 真档里那个文件 939 KB，没套装的物品不该为它付解析代价。
	source := readonlyItemSource("[name]\n`无套装` \n[equipment type]\n`[coat]` \n[part set index]\n0 \n", nil)
	harness := itemDetailHarness(t, source)
	before := time.Now()
	_, response := harness.get(t, "/api/items/2/detail", map[string]string{"itemId": "2"}, harness.server.handleItemDetail)
	requireOK(t, http.StatusOK, response)
	if response["partSetIndex"] != float64(0) {
		t.Fatalf("partSetIndex = %v", response["partSetIndex"])
	}
	if _, hasSet := response["equipmentSet"]; hasSet {
		t.Fatalf("part set index 0 must not produce a set: %+v", response["equipmentSet"])
	}
	if elapsed := time.Since(before); elapsed > 2*time.Second {
		t.Fatalf("part-set lookup was attempted for a setless item (took %s)", elapsed)
	}
	if harness.server.pvf.partSetDone {
		t.Fatal("part set list must stay unread for items with part set index 0")
	}
}

func TestItemDetailRejectsUnknownAndInvalidIDs(t *testing.T) {
	harness := itemDetailHarness(t, readonlyItemSource(readonlyTestWeapon, nil))
	for _, itemID := range []string{"0", "999", "abc", "-1"} {
		_, response := harness.get(t, "/api/items/"+itemID+"/detail", map[string]string{"itemId": itemID}, harness.server.handleItemDetail)
		requireFailure(t, http.StatusOK, response)
	}
}

func TestItemDetailReportsIndexNotReady(t *testing.T) {
	harness := newReadonlyHarness(t, readonlyFixture{
		source:    readonlyItemSource(readonlyTestWeapon, nil),
		character: dnfrepo.CharacterRecord{Name: "tester", Job: "11", Level: 90, Stats: map[string]int64{}},
		skipItems: true,
	})
	_, response := harness.get(t, "/api/items/2/detail", map[string]string{"itemId": "2"}, harness.server.handleItemDetail)
	if message := requireFailure(t, http.StatusOK, response); !strings.Contains(message, "构建中") {
		t.Fatalf("message = %q, want the index-not-ready wording", message)
	}
	harness.server.items.mu.Lock()
	harness.server.items.buildEr = context.Canceled
	harness.server.items.mu.Unlock()
	_, response = harness.get(t, "/api/items/2/detail", map[string]string{"itemId": "2"}, harness.server.handleItemDetail)
	if message := requireFailure(t, http.StatusOK, response); !strings.Contains(message, "索引构建失败") {
		t.Fatalf("message = %q, want the build-failure wording", message)
	}
}
